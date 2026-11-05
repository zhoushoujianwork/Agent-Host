package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func TestProgressDeliveryUsesOriginalReceiptAndLeavesFinalIndependent(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown"} {
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
			p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "检索资料", "one")
			if err != nil {
				t.Fatal(err)
			}
			f.replier.reply = func(context.Context, replyCall) (string, error) {
				t.Error("progress used standalone reply")
				return "failed", errors.New("unexpected text")
			}
			reactor := &serviceReactor{send: func(_ context.Context, c reactionCall) error {
				if c.conversation != receipt.ConversationID || c.message != receipt.MessageID {
					t.Errorf("wrong target: %+v", c)
				}
				if c.label == p.Text && c.action == "add" && outcome != "accepted" {
					if outcome == "failed" {
						return (&dingtalk.Reactor{}).AddProgressReaction(t.Context(), receipt.ConversationID, receipt.MessageID, p.Text)
					}
					return errors.New("secret https://provider.invalid/?token=private")
				}
				return nil
			}}
			f.service.Reactor = reactor
			drainReactions(t, f.service)
			saved, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
			// The adapter distinguishes confirmed failures from uncertain mutations.
			want := outcome
			if err != nil || saved.Status != want || strings.Contains(saved.Error, "secret") {
				t.Fatal(saved, err)
			}
			final, err := f.service.Store.ChannelMessage(t.Context(), receipt.ID)
			if err != nil || final.ReplyStatus != "pending" || final.ReplyText != "" {
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
	reactor := &testAPIReactor{}
	f.service.APIReactor = reactor
	f.service.DisableReactions = true
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
		if !strings.HasPrefix(text, "Final answer") {
			t.Errorf("unexpected intermediate text %q", text)
		}
		return "accepted", nil
	}
	reactor.send = func(_ context.Context, c apiReactionCall) (string, string, error) {
		if c.action == "add" {
			if c.value != "处理中" || c.target.ID != input.ReplyTarget.ID || c.target.MessageID != input.ReplyTarget.MessageID {
				t.Errorf("wrong reaction: %+v", c)
			}
			delivered <- struct{}{}
		}
		return "provider-progress", "accepted", nil
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
		if _, err = f.service.Store.ReserveChannelProgress(ctx, b, "Working", "one"); err != nil {
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

func progressReactionFixture(t *testing.T) (*serviceFixture, state.ChannelMessage, state.ChannelProgressBinding) {
	t.Helper()
	f := newServiceFixture(t)
	m := f.message(t, "progress-replace", "work", "text", "dm-original")
	f.receive(t, m)
	drainReactions(t, f.service)
	receipt := f.next(t)
	session, err := f.service.session(t.Context(), receipt.Binding, false)
	if err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "replace-run", SessionID: session.ID, Scope: session.Scope, NativeSessionID: session.NativeSessionID, Input: receipt.Text, Workspace: session.Workspace, RuntimeFingerprint: session.RuntimeFingerprint, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Projects: session.Projects, AdditionalDirectories: session.AdditionalDirectories}
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
	return f, receipt, b
}

func TestProgressCustomCleanupAndTerminalWins(t *testing.T) {
	f, m, b := progressReactionFixture(t)
	p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "检索资料", "one")
	if err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	got, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "accepted" {
		t.Fatal(got, err)
	}
	// Only this final delivery invokes the ordinary reply adapter.
	if err = f.service.Store.FinishChannelMessage(t.Context(), m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.PrepareChannelReply(t.Context(), m.ID, "Final answer"); err != nil {
		t.Fatal(err)
	}
	if err = f.service.deliverChannelReply(t.Context(), m, dingtalk.ReplyMessage{Text: "Final answer"}); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	calls := f.service.Reactor.(*serviceReactor).snapshot()
	var customAdd, customRemove int
	for _, c := range calls {
		if c.conversation != m.ConversationID || c.message != m.MessageID {
			t.Fatal(c)
		}
		if c.label == p.Text {
			if c.action == "add" {
				customAdd++
			}
			if c.action == "remove" {
				customRemove++
			}
		}
	}
	if customAdd != 1 || customRemove != 1 || calls[len(calls)-1].label != "已完成" || len(f.replier.snapshot()) != 1 {
		t.Fatal(calls, f.replier.snapshot())
	}
	if _, err = f.service.Store.ReserveChannelProgress(t.Context(), b, "Late", "late"); err == nil {
		t.Fatal("late progress admitted")
	}
}

func TestProgressRevocationAfterCleanupPreventsAddition(t *testing.T) {
	f, _, b := progressReactionFixture(t)
	p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "Reading", "one")
	if err != nil {
		t.Fatal(err)
	}
	r := f.service.Reactor.(*serviceReactor)
	r.send = func(_ context.Context, c reactionCall) error {
		if c.action == "remove" {
			return f.service.Store.RevokeChannelProgress(t.Context(), b.RunID)
		}
		return nil
	}
	drainReactions(t, f.service)
	got, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "failed" {
		t.Fatal(got, err)
	}
	for _, c := range r.snapshot() {
		if c.label == p.Text && c.action == "add" {
			t.Fatal("added after revocation", c)
		}
	}
	if len(f.replier.snapshot()) != 0 {
		t.Fatal("progress fell back to text")
	}
}

func TestProgressUnknownAttachmentBlocksTerminalReplacement(t *testing.T) {
	f, m, b := progressReactionFixture(t)
	p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "Reading", "one")
	if err != nil {
		t.Fatal(err)
	}
	r := f.service.Reactor.(*serviceReactor)
	r.send = func(_ context.Context, c reactionCall) error {
		if c.label == p.Text && c.action == "add" {
			return errors.New("secret https://provider/?token=private")
		}
		return nil
	}
	drainReactions(t, f.service)
	before := len(r.snapshot())
	if err = f.service.Store.FinishChannelMessage(t.Context(), m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.PrepareChannelReply(t.Context(), m.ID, "Final answer"); err != nil {
		t.Fatal(err)
	}
	if err = f.service.deliverChannelReply(t.Context(), m, dingtalk.ReplyMessage{Text: "Final answer"}); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	if len(r.snapshot()) != before || len(f.replier.snapshot()) != 1 {
		t.Fatal("unknown attachment replayed or final lost")
	}
	got, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "unknown" || strings.Contains(got.Error, "secret") {
		t.Fatal(got, err)
	}
}

func TestProgressAuthorizedInflightFinishesThenTerminalReplacesLast(t *testing.T) {
	f, m, b := progressReactionFixture(t)
	p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "Reading", "one")
	if err != nil {
		t.Fatal(err)
	}
	r := f.service.Reactor.(*serviceReactor)
	r.send = func(_ context.Context, c reactionCall) error {
		if c.label == p.Text && c.action == "add" {
			if err := f.service.Store.FinishChannelMessage(t.Context(), m.ID, "completed", ""); err != nil {
				return err
			}
			if err := f.service.Store.PrepareChannelReply(t.Context(), m.ID, "Final answer"); err != nil {
				return err
			}
			return f.service.deliverChannelReply(t.Context(), m, dingtalk.ReplyMessage{Text: "Final answer"})
		}
		return nil
	}
	drainReactions(t, f.service)
	got, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "accepted" {
		t.Fatal(got, err)
	}
	calls := r.snapshot()
	last := calls[len(calls)-1]
	if last.action != "add" || last.label != "已完成" || len(f.replier.snapshot()) != 1 {
		t.Fatal(calls, f.replier.snapshot())
	}
	rs, err := f.service.Store.ChannelReactions(t.Context(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Active && r.Stage != "completed" {
			t.Fatal("stale active feedback", r)
		}
	}
}

func TestProgressDingTalkDisabledLifecycleRetainsExplicitReaction(t *testing.T) {
	f := newServiceFixture(t)
	f.service.DisableReactions = true
	if err := f.service.Validate(); err != nil {
		t.Fatal(err)
	}
	input := f.message(t, "disabled-progress", "work", "text", "dm-original")
	f.receive(t, input)
	m := f.next(t)
	rs, err := f.service.Store.ChannelReactions(t.Context(), m.ID)
	if err != nil || len(rs) != 0 || !m.DisableReactions {
		t.Fatal("automatic markers enabled", rs, m.DisableReactions, err)
	}
	session, err := f.service.session(t.Context(), m.Binding, false)
	if err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "disabled-run", SessionID: session.ID, Scope: session.Scope, NativeSessionID: session.NativeSessionID, Input: m.Text, Workspace: session.Workspace, RuntimeFingerprint: session.RuntimeFingerprint, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Projects: session.Projects, AdditionalDirectories: session.AdditionalDirectories}
	if err = f.service.Store.CreateChannelRun(t.Context(), run, m.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.EnableChannelProgress(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	b, err := f.service.Store.ChannelProgressBinding(t.Context(), m.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.service.Store.ReserveChannelProgress(t.Context(), b, "Reading", "one")
	if err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	got, err := f.service.Store.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "accepted" {
		t.Fatal(got, err)
	}
	calls := f.service.Reactor.(*serviceReactor).snapshot()
	if len(calls) != 1 || calls[0].label != p.Text || len(f.replier.snapshot()) != 0 {
		t.Fatal(calls)
	}
}
