package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type forkServiceRunner struct{ *serviceRunner }

func (*forkServiceRunner) SupportsFork() bool { return true }

func TestRoomWorkersOverlapMembersAndKeepRepliesLinked(t *testing.T) {
	for _, sender := range []string{"alice", "bob"} {
		t.Run(sender, func(t *testing.T) {
			f := newServiceFixture(t)
			f.host.Runner = &forkServiceRunner{f.runner}
			// Use the authenticated audience resolver, also used in production.
			base := *f.service
			f.service.ResolveAgent = func(_ context.Context, m state.ChannelMessage) (*Service, error) {
				child := base
				h := *f.host
				h.Config.AgentID = m.AgentID
				h.Config.AudienceID = m.AudienceID
				child.Store = f.service.Store.WithPartition(m.AgentID, m.AudienceID)
				h.Store = child.Store
				child.Host = &h
				return &child, nil
			}
			first := groupInput(t, f, "room-a", "alice", "room", "first message")
			second := groupInput(t, f, "room-b", sender, "room", "second message")
			first.SenderName = "Alice"
			f.receive(t, first)
			f.receive(t, second)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			entered := make(chan harness.Request, 2)
			release := make(chan struct{})
			f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
				entered <- req
				select {
				case <-release:
				case <-ctx.Done():
					return harness.Result{Status: "canceled"}, ctx.Err()
				}
				return harness.Result{Status: "completed", Text: "reply to " + req.NativeSessionID, NativeSessionID: req.NativeSessionID, Model: req.Model}, nil
			}
			delivered := make(chan replyCall, 2)
			f.replier.reply = func(_ context.Context, call replyCall) (string, error) { delivered <- call; return "accepted", nil }
			done := make(chan error, 1)
			go func() { done <- f.service.Work(ctx) }()
			for range 2 {
				waitForWorkerSignal(t, ctx, entered)
			}
			a, b := f.receipt(t, first.ID), f.receipt(t, second.ID)
			if a.SessionID == b.SessionID || a.RunID == "" || b.RunID == "" || a.Binding != b.Binding {
				t.Fatal("messages did not overlap in separate native sessions", a, b)
			}
			close(release)
			for range 2 {
				call := waitForWorkerSignal(t, ctx, delivered)
				if strings.Contains(call.url, "cap-room-a") {
					if call.atUserID != "alice" || !strings.Contains(call.text, "first message") {
						t.Fatal(call)
					}
				} else if strings.Contains(call.url, "cap-room-b") {
					if call.atUserID != sender || !strings.Contains(call.text, "second message") {
						t.Fatal(call)
					}
				} else {
					t.Fatal("wrong original reply target", call)
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			for _, m := range []string{first.ID, second.ID} {
				receipt := f.receipt(t, m)
				run, err := f.service.Store.WithPartition(receipt.AgentID, receipt.AudienceID).Run(t.Context(), receipt.RunID)
				if err != nil || run.Source == nil || run.Room == nil || run.Source.SenderID != run.Room.SenderID || run.Room.RequestID != receipt.MessageID {
					t.Fatal("sender or original message lost", run, err)
				}
			}
		})
	}
}
