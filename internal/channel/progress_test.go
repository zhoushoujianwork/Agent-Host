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

func TestProgressDeliveryUsesOriginalReceiptAndLeavesFinalIndependent(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			f := newServiceFixture(t)
			m := f.message(t, "progress", "work", "text", "dm-original")
			f.receive(t, m)
			receipt := f.next(t)
			session, err := f.service.session(t.Context(), receipt.Binding, false)
			if err != nil {
				t.Fatal(err)
			}
			run := state.Run{ID: "progress-run", SessionID: session.ID, Scope: session.Scope, NativeSessionID: session.NativeSessionID, Input: receipt.Text, Workspace: session.Workspace, RuntimeFingerprint: session.RuntimeFingerprint, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Projects: session.Projects, AdditionalDirectories: session.AdditionalDirectories}
			if err = f.service.Store.CreateChannelRun(t.Context(), run, receipt.ID); err != nil {
				t.Fatal(err)
			}
			if err = f.service.Store.EnableChannelProgress(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			b, err := f.service.Store.ChannelProgressBinding(t.Context(), receipt.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "Working on it", "one")
			if err != nil {
				t.Fatal(err)
			}
			f.replier.reply = func(_ context.Context, call replyCall) (string, error) {
				if call.url != m.ReplyURL || !call.expires.Equal(m.ReplyExpiresAt) || call.text != p.Text {
					t.Errorf("wrong progress target: %+v", call)
				}
				if outcome == "accepted" {
					return outcome, nil
				}
				return outcome, errors.New("provider failed at https://secret.invalid/?token=private")
			}
			p, target, err := f.service.Store.ClaimNextChannelProgress(t.Context(), receipt.Profile)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.processProgress(t.Context(), p, target); err != nil {
				t.Fatal(err)
			}
			saved, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
			want := outcome
			if want == "invalid" {
				want = "unknown"
			}
			if err != nil || saved.Status != want || strings.Contains(saved.Error, "secret") || strings.Contains(saved.Error, "https") {
				t.Fatal(saved, err)
			}
			final, err := f.service.Store.ChannelMessage(t.Context(), receipt.ID)
			if err != nil || final.ReplyStatus != "pending" || final.ReplyText != "" || final.Status != "claimed" {
				t.Fatal(final, err)
			}
			r, err := f.service.Store.Run(t.Context(), run.ID)
			if err != nil || r.ExecutionStatus != "running" || r.AcceptanceStatus != "unverified" || r.MemoryStatus != "pending_execution" {
				t.Fatal(r, err)
			}
		})
	}
}

func TestProgressWorkerRunsWithDisabledReactionsAndJoinsOnCancel(t *testing.T) {
	f, reply := newFeishuFixture(t)
	f.service.SelectMessageAgent = func(context.Context, Message) (AgentSelection, error) {
		return AgentSelection{AgentID: state.DefaultAgentID, AudienceID: state.OwnerAudienceID}, nil
	}
	f.host.Config.ToolsDisabled = false
	f.host.Config.ProgressHome = f.home
	f.host.Config.ProgressExecutable = "/fixture/agent-host"
	delivered := make(chan struct{}, 1)
	returned := make(chan struct{}, 1)
	input := feishuMessage("progress", "1", "oc_original", "ou_owner", "work")
	reply.reply = func(target state.ChannelReplyTarget, text string) (string, error) {
		if text == "Still working" {
			if target.Kind != input.ReplyTarget.Kind || target.ID != input.ReplyTarget.ID || target.MessageID != input.ReplyTarget.MessageID || target.UUID == "" {
				t.Errorf("wrong target: %+v", target)
			}
			delivered <- struct{}{}
		}
		return "accepted", nil
	}
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		r, err := f.service.Store.LatestSessionRun(ctx, req.HostSessionID)
		if err != nil {
			return harness.Result{}, err
		}
		if err = f.service.Store.EnableChannelProgress(ctx, r.ID); err != nil {
			return harness.Result{}, err
		}
		b, err := f.service.Store.ChannelProgressBinding(ctx, r.Source.ChannelMessageID, r.ID)
		if err != nil {
			return harness.Result{}, err
		}
		if _, err = f.service.Store.ReserveChannelProgress(ctx, b, "Still working", "one"); err != nil {
			return harness.Result{}, err
		}
		select {
		case <-delivered:
		case <-ctx.Done():
			return harness.Result{}, ctx.Err()
		}
		returned <- struct{}{}
		return harness.Result{Status: "completed", Text: "Final answer", Model: "fixture-model", NativeSessionID: req.NativeSessionID}, nil
	}
	if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	select {
	case <-returned:
	case err := <-done:
		t.Fatalf("worker exited: %v", err)
	case <-time.After(5 * time.Second):

		t.Fatal("disabled reactions suppressed progress")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not join")
	}
}
