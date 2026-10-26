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

func TestRoomChannelHistorySurvivesRuntimeDiscontinuity(t *testing.T) {
	f := newServiceFixture(t)
	f.host.Runner = &forkServiceRunner{f.runner}
	f.host.Config.RuntimeFingerprint = "A"
	calls := 0
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		calls++
		status, output := "completed", "service healthy: evidence-314159"
		if calls == 2 {
			status, output = "canceled", ""
		}
		return harness.Result{Status: status, Text: output, NativeSessionID: req.NativeSessionID, Model: req.Model}, nil
	}
	first := f.process(t, f.message(t, "service-check", "check service", "text", "owner-room"))
	if first.Status != "completed" {
		seed, err := f.service.Store.Run(t.Context(), first.RunID)
		t.Fatalf("seed did not complete: status=%s run_error=%s read_error=%v", first.Status, seed.Error, err)
	}
	f.host.Config.RuntimeFingerprint = "B"
	f.process(t, f.message(t, "hi", "hi-only-request", "text", "owner-room"))
	quote := f.message(t, "forward", "send the result to the colleague", "text", "owner-room")
	quote.ReplyToID = "unknown-bot-message"
	followup := f.process(t, quote)
	run, err := f.service.Store.Run(t.Context(), followup.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Room == nil || run.Room.ParentSessionID != "" || run.Room.ForkFromNativeSessionID != "" || run.Room.ReplyToID != quote.ReplyToID || len(run.Room.History) != 2 {
		t.Fatalf("invalid channel context: %+v", run.Room)
	}
	entry := run.Room.History[0]
	if entry.RunID != first.RunID || entry.SenderID != first.SenderID || entry.Status != "completed" || entry.Reply != "service healthy: evidence-314159" || entry.TextOnlyReason != "runtime_or_model_changed" {
		t.Fatalf("lost channel history: %+v", entry)
	}
	requests := f.runner.snapshot()
	got := requests[len(requests)-1]
	if got.Resume || got.ForkFromNativeSessionID != "" || !strings.Contains(got.Prompt, entry.Reply) || !strings.Contains(got.Prompt, "ask if ambiguous") {
		t.Fatalf("invalid harness context: %+v", got)
	}
}
