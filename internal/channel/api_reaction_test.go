package channel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/state"
)

type apiReactionCall struct {
	action string
	target state.ChannelReplyTarget
	value  string
}

type testAPIReactor struct {
	mu    sync.Mutex
	calls []apiReactionCall
	send  func(context.Context, apiReactionCall) (string, string, error)
}

func (r *testAPIReactor) call(ctx context.Context, call apiReactionCall) (string, string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.send != nil {
		return r.send(ctx, call)
	}
	return "provider-id-" + call.value, "accepted", nil
}

func (r *testAPIReactor) AddReaction(ctx context.Context, target state.ChannelReplyTarget, label string) (string, string, error) {
	return r.call(ctx, apiReactionCall{"add", target, label})
}

func (r *testAPIReactor) RemoveReaction(ctx context.Context, target state.ChannelReplyTarget, id string) (string, error) {
	_, status, err := r.call(ctx, apiReactionCall{"remove", target, id})
	return status, err
}

func (r *testAPIReactor) snapshot() []apiReactionCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apiReactionCall(nil), r.calls...)
}

func newAPIReactionFixture(t *testing.T) (*serviceFixture, *meetingTestReplier, *testAPIReactor) {
	t.Helper()
	f, reply := newFeishuFixture(t)
	r := &testAPIReactor{}
	f.service.DisableReactions, f.service.APIReactor = false, r
	resolve := f.service.ResolveAgent
	f.service.ResolveAgent = func(ctx context.Context, m state.ChannelMessage) (*Service, error) {
		child, err := resolve(ctx, m)
		if child != nil {
			// A child with feedback enabled must inherit the parent's adapter.
			child.DisableReactions, child.APIReactor = false, nil
		}
		return child, err
	}
	return f, reply, r
}

func TestFeishuAPIReactionReplacesPersistedIDsWithoutMeetingFeedback(t *testing.T) {
	f, reply, reactor := newAPIReactionFixture(t)
	input := feishuMessage("progress", "1", "oc_private", "ou_owner", "Explain the project.")
	if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	m := f.next(t)
	drainReactions(t, f.service)
	if err := f.service.process(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	want := []apiReactionCall{
		{"add", *m.ReplyTarget, "已收到"},
		{"remove", *m.ReplyTarget, "provider-id-已收到"},
		{"add", *m.ReplyTarget, "处理中"},
		{"remove", *m.ReplyTarget, "provider-id-处理中"},
		{"add", *m.ReplyTarget, "已完成"},
	}
	if got := reactor.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("original target or provider ID changed: got=%+v want=%+v", got, want)
	}
	saved, err := f.service.Store.ChannelMessage(t.Context(), m.ID)
	if err != nil || saved.DisableReactions || saved.Status != "completed" || saved.ReplyStatus != "accepted" || len(saved.Reactions) != 3 {
		t.Fatal("feedback changed task or delivery truth", saved, err)
	}
	for i, r := range saved.Reactions {
		if r.Status != "accepted" || r.ProviderReactionID != "provider-id-"+r.Label || r.Active != (i == 2) || i < 2 && r.RemovalStatus != "accepted" {
			t.Fatal("provider acceptance/removal evidence lost", r)
		}
	}
	meeting := processMeeting(t, f, meetingMessage("meeting", "700000000000000001", "ou_owner", "Meeting question."))
	command := processMeeting(t, f, feishuMessage("command", "1", input.ConversationID, input.SenderID, "/status"))
	drainReactions(t, f.service)
	if !meeting.DisableReactions || len(meeting.Reactions) != 0 || len(command.Reactions) != 0 || len(reactor.snapshot()) != 5 || len(reply.calls) != 3 || len(f.runner.snapshot()) != 2 {
		t.Fatal("meeting or command acquired feedback, or duplicate executed")
	}
	// Enabled IM receipts remain eligible for same-conversation Host history.
	followup := processMeeting(t, f, feishuMessage("history", "1", input.ConversationID, input.SenderID, "Explain the next step."))
	binding, err := f.service.Store.ConversationHistoryBinding(t.Context(), followup.ID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.service.Store.SearchConversationHistory(t.Context(), binding, state.ConversationHistorySearch{Limit: 10})
	if err != nil || len(history.Turns) != 1 || history.Turns[0].ReceiptID != m.ID {
		t.Fatal("feedback policy broke history identity", history, err)
	}
}

func TestFeishuAPIReactionUnknownAddStopsLaterFeedbackWithoutStoppingReply(t *testing.T) {
	for _, tc := range []struct {
		name, providerID, status string
		err                      error
	}{
		{"unknown", "", "unknown", errors.New("response lost")},
		{"missing ID", "", "accepted", nil},
		{"unbounded ID", strings.Repeat("x", 1025), "accepted", nil},
		{"conflicting evidence", "provider-id", "accepted", errors.New("contradictory result")},
		{"invalid status", "", "success", nil},
		{"missing failure", "", "failed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, reply, reactor := newAPIReactionFixture(t)
			reactor.send = func(context.Context, apiReactionCall) (string, string, error) {
				return tc.providerID, tc.status, tc.err
			}
			input := feishuMessage("unknown", "1", "oc_private", "ou_owner", "Explain the project.")
			if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			drainReactions(t, f.service)
			m := f.next(t)
			drainReactions(t, f.service)
			if err := f.service.process(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			drainReactions(t, f.service)
			if err := f.service.Store.RecoverChannels(t.Context()); err != nil {
				t.Fatal(err)
			}
			drainReactions(t, f.service)
			saved, err := f.service.Store.ChannelMessage(t.Context(), m.ID)
			if err != nil || saved.Status != "completed" || saved.ReplyStatus != "accepted" || len(saved.Reactions) != 3 || len(reactor.snapshot()) != 1 || len(reply.calls) != 1 || len(f.runner.snapshot()) != 1 {
				t.Fatal("unknown feedback replayed or blocked task/reply", saved, err)
			}
			for i, r := range saved.Reactions {
				want := "skipped"
				if i == 0 {
					want = "unknown"
				}
				if r.Status != want || r.Active || r.ProviderReactionID != "" || r.Error == "" {
					t.Fatal("uncertain reaction acquired acceptance or replacement", r)
				}
			}
		})
	}
}

func TestFeishuAPIReactionUnresolvedRemovalIsNeverRepeated(t *testing.T) {
	for _, outcome := range []string{"failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f, _, reactor := newAPIReactionFixture(t)
			reactor.send = func(_ context.Context, call apiReactionCall) (string, string, error) {
				if call.action == "remove" {
					return "", outcome, errors.New("removal not confirmed")
				}
				return "exact-remote-id", "accepted", nil
			}
			input := feishuMessage("cleanup", "2", "oc_group", "ou_alice", "Explain the project.")
			if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			drainReactions(t, f.service)
			m := f.next(t)
			drainReactions(t, f.service)
			if err := f.service.process(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			drainReactions(t, f.service)
			if err := f.service.Store.RecoverChannels(t.Context()); err != nil {
				t.Fatal(err)
			}
			drainReactions(t, f.service)
			saved, err := f.service.Store.ChannelMessage(t.Context(), m.ID)
			calls := reactor.snapshot()
			if err != nil || len(calls) != 2 || calls[1].value != "exact-remote-id" || saved.ReplyStatus != "accepted" || len(saved.Reactions) != 3 {
				t.Fatal("removal replayed or changed task delivery", calls, saved, err)
			}
			old := saved.Reactions[0]
			if !old.Active || old.ProviderReactionID != "exact-remote-id" || old.Status != "accepted" || old.RemovalStatus != outcome || saved.Reactions[1].Status != "failed" || saved.Reactions[2].Status != "skipped" {
				t.Fatal("unresolved cleanup lost durable evidence", saved.Reactions)
			}
		})
	}
}

func TestFeishuAPIReactionWorkerDoesNotBlockReplyAndJoinsCancellation(t *testing.T) {
	f, _, reactor := newAPIReactionFixture(t)
	entered, stopped := make(chan struct{}), make(chan struct{})
	reactor.send = func(ctx context.Context, _ apiReactionCall) (string, string, error) {
		close(entered)
		defer close(stopped)
		<-ctx.Done()
		return "", "unknown", ctx.Err()
	}
	input := feishuMessage("slow", "1", "oc_private", "ou_owner", "Explain the project.")
	if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Error("worker shutdown", err)
		}
		select {
		case <-stopped:
		default:
			t.Error("reaction network worker was not joined")
		}
		rows, err := f.service.Store.ChannelReactions(context.Background(), Key(state.ReplyTargetFeishu, f.service.ClientID, input.TenantID, input.ConversationID, input.ID))
		if err != nil || len(rows) == 0 || rows[0].Status != "unknown" {
			t.Error("canceled attempt lost", rows, err)
		}
	})
	waitForWorkerSignal(t, ctx, entered)
	// A blocked platform operation must not hold receipt, native execution, or reply.
	if err := f.service.ReceiveMessage(ctx, input); err != nil {
		t.Fatal(err)
	}
	for {
		saved, err := f.service.Store.ChannelMessage(ctx, Key(state.ReplyTargetFeishu, f.service.ClientID, input.TenantID, input.ConversationID, input.ID))
		if err != nil {
			t.Fatal(err)
		}
		if saved.ReplyStatus == "accepted" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("slow feedback blocked reply")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if len(f.runner.snapshot()) != 1 {
		t.Fatal("duplicate receipt replayed execution")
	}
}

func TestFeishuAPIReactionAcceptedAtCancellationIsStillSaved(t *testing.T) {
	f, _, reactor := newAPIReactionFixture(t)
	input := feishuMessage("cancel-ack", "1", "oc_private", "ou_owner", "Explain the project.")
	if err := f.service.ReceiveMessage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Store.ClaimNextChannelReaction(t.Context(), f.service.Profile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reactor.send = func(context.Context, apiReactionCall) (string, string, error) {
		cancel()
		return "confirmed-at-shutdown", "accepted", nil
	}
	if err := f.service.processReaction(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err := f.service.Store.ChannelReactions(t.Context(), r.ChannelMessageID)
	if err != nil || len(rows) != 1 || rows[0].Status != "accepted" || rows[0].ProviderReactionID != "confirmed-at-shutdown" || !rows[0].Active {
		t.Fatal("shutdown discarded platform acknowledgment", rows, err)
	}
}

func TestFeishuAPIReactionValidationRequiresAdapterOnlyWhenEnabled(t *testing.T) {
	f, _, _ := newAPIReactionFixture(t)
	if err := f.service.Validate(); err != nil {
		t.Fatal(err)
	}
	f.service.APIReactor = nil
	if err := f.service.Validate(); err == nil {
		t.Fatal("enabled IM accepted missing reaction adapter")
	}
	f.service.DisableReactions = true
	if err := f.service.Validate(); err != nil {
		t.Fatal(err)
	}
	f.service.Channel, f.service.DisableReactions, f.service.APIReactor = state.ReplyTargetFeishuMeeting, false, &testAPIReactor{}
	if err := f.service.Validate(); err == nil {
		t.Fatal("meeting service acquired IM feedback")
	}
}
