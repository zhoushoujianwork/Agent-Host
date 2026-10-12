package channel

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type reactionCall struct{ action, conversation, message, label string }

type serviceReactor struct {
	mu    sync.Mutex
	calls []reactionCall
	send  func(context.Context, reactionCall) error
}

func (r *serviceReactor) AddReaction(ctx context.Context, conversation, message, label string) error {
	return r.call(ctx, reactionCall{"add", conversation, message, label})
}

func (r *serviceReactor) RemoveReaction(ctx context.Context, conversation, message, label string) error {
	return r.call(ctx, reactionCall{"remove", conversation, message, label})
}

func (r *serviceReactor) call(ctx context.Context, call reactionCall) error {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.send != nil {
		return r.send(ctx, call)
	}
	return nil
}

func (r *serviceReactor) snapshot() []reactionCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]reactionCall(nil), r.calls...)
}

func drainReactions(t *testing.T, s *Service) {
	t.Helper()
	for {
		r, err := s.Store.ClaimNextChannelReaction(context.Background(), s.Profile)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := s.processReaction(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReactionStagesReplaceOriginalMessageAndDeduplicate(t *testing.T) {
	f := newServiceFixture(t)
	r := f.service.Reactor.(*serviceReactor)
	var observed []state.ChannelReaction
	f.service.ObserveReaction = func(kind string, reaction state.ChannelReaction, duration time.Duration) error {
		if !strings.HasPrefix(kind, "channel.reaction.") || duration < 0 {
			t.Errorf("invalid reaction event: %s %s", kind, duration)
		}
		observed = append(observed, reaction)
		return nil
	}
	input := f.message(t, "original-msg", "Explain this project.", "text", "original-dm")
	f.receive(t, input)
	f.receive(t, input)
	drainReactions(t, f.service)
	m := f.next(t)
	drainReactions(t, f.service)
	if err := f.service.process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	f.receive(t, input)
	drainReactions(t, f.service)
	want := []reactionCall{
		{"add", "original-dm", "original-msg", "已收到"},
		{"remove", "original-dm", "original-msg", "已收到"},
		{"add", "original-dm", "original-msg", "处理中"},
		{"remove", "original-dm", "original-msg", "处理中"},
		{"add", "original-dm", "original-msg", "已完成"},
	}
	if got := r.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("stage replacement: got=%+v want=%+v", got, want)
	}
	got := f.receipt(t, input.ID)
	last := got.Reactions[len(got.Reactions)-1]
	if got.Reactions[0].Active || got.Reactions[1].Active || !last.Active || last.Status != "accepted" || last.Stage != "completed" {
		t.Fatalf("stage audit: %+v", got.Reactions)
	}
	if len(f.runner.snapshot()) != 1 || len(f.replier.snapshot()) != 1 {
		t.Fatal("reaction or duplicate changed execution/reply count")
	}
	if len(observed) != 6 || observed[0].Status != "sending" || observed[5].Status != "accepted" || observed[5].Stage != "completed" || observed[5].ChannelMessageID != got.ID {
		t.Fatalf("structured reaction observations: %+v", observed)
	}
}

func TestReactionCompletionRequiresTaskAndReplySuccess(t *testing.T) {
	for _, failure := range []string{"native", "memory", "delivery failed", "delivery unknown"} {
		t.Run(failure, func(t *testing.T) {
			f := newServiceFixture(t)
			switch failure {
			case "native":
				f.runner.run = func(context.Context, harness.Request) (harness.Result, error) {
					return harness.Result{Status: "failed"}, errors.New("fixture native failure")
				}
			case "memory":
				f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
					return memory.Decision{}, errors.New("fixture memory failure")
				})
			default:
				f.replier.reply = func(context.Context, replyCall) (string, error) {
					return strings.TrimPrefix(failure, "delivery "), errors.New("fixture reply failure")
				}
			}
			input := f.message(t, "failed", "Explain this project.", "text", "original-dm")
			m := f.process(t, input)
			drainReactions(t, f.service)
			calls := f.service.Reactor.(*serviceReactor).snapshot()
			if calls[len(calls)-1].label != "打叉" {
				t.Fatalf("false completion: %+v", calls)
			}
			for _, call := range calls {
				if call.label == "已完成" {
					t.Fatal("failure briefly marked complete")
				}
			}
			if strings.HasPrefix(failure, "delivery") && m.Status != "completed" {
				t.Fatal("delivery failure rewrote execution truth")
			}
		})
	}
}

func TestSlowReactionDoesNotBlockACKNativeOrReply(t *testing.T) {
	f := newServiceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.service.Reactor.(*serviceReactor).send = func(ctx context.Context, _ reactionCall) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	input := f.message(t, "slow", "Explain this project.", "text", "dm")
	f.receive(t, input)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reaction did not start")
	}
	// While the platform request remains blocked, receipt and final delivery finish.
	f.receive(t, input)
	for f.receipt(t, input.ID).ReplyStatus != "accepted" {
		select {
		case <-ctx.Done():
			t.Fatal("slow feedback blocked native/reply")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if len(f.runner.snapshot()) != 1 {
		t.Fatal("duplicate execution")
	}
	close(release)
	for {
		stages, err := f.service.Store.ChannelReactions(ctx, Key("dingtalk", f.service.ClientID, input.ID))
		if err != nil {
			t.Fatal(err)
		}
		if len(stages) > 0 && stages[len(stages)-1].Stage == "completed" && stages[len(stages)-1].Status == "accepted" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("terminal reaction not drained")
		case <-time.After(5 * time.Millisecond):
		}
	}
	for _, call := range f.service.Reactor.(*serviceReactor).snapshot() {
		if call.action == "add" && call.label != "已收到" && call.label != "已完成" {
			t.Fatalf("slow feedback replayed an obsolete intermediate stage: %+v", call)
		}
	}
}

func TestReactionErrorsAreRecordedWithoutTextFallbackOrReplay(t *testing.T) {
	for _, operation := range []string{"add", "remove"} {
		t.Run(operation, func(t *testing.T) {
			f := newServiceFixture(t)
			var events []string
			f.service.Observe = func(kind, message string) error { events = append(events, kind+" "+message); return nil }
			r := f.service.Reactor.(*serviceReactor)
			r.send = func(_ context.Context, call reactionCall) error {
				if call.action == operation {
					return errors.New("fixture uncertain response")
				}
				return nil
			}
			input := f.message(t, "feedback-fail", "Explain this project.", "text", "dm")
			m := f.process(t, input)
			drainReactions(t, f.service)
			before := len(r.snapshot())
			drainReactions(t, f.service)
			if len(r.snapshot()) != before || len(f.replier.snapshot()) != 1 || m.ReplyStatus != "accepted" || m.Status != "completed" {
				t.Fatal("feedback failure changed task/reply or retried")
			}
			stages, err := f.service.Store.ChannelReactions(context.Background(), m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stages[len(stages)-1].Status != "unknown" || !strings.Contains(stages[len(stages)-1].Error, operation) {
				t.Fatalf("failure not retained: %+v", stages)
			}
			if !strings.Contains(strings.Join(events, "\n"), "status=unknown") {
				t.Fatal("failure not observed")
			}
			if operation == "remove" {
				removals := 0
				for _, call := range r.snapshot() {
					if call.action == "remove" {
						removals++
					}
					if call.action == "add" && call.label != "已收到" {
						t.Fatal("added stage after failed cleanup")
					}
				}
				if removals != 1 {
					t.Fatalf("uncertain cleanup retried %d times", removals)
				}
			}
		})
	}
}

func TestSystemCommandAndUnsupportedInputDoNotCreateAgentReactions(t *testing.T) {
	f := newServiceFixture(t)
	f.process(t, f.message(t, "new", "/new", "text", "dm"))
	f.process(t, f.message(t, "image", "", "picture", "dm"))
	drainReactions(t, f.service)
	if len(f.service.Reactor.(*serviceReactor).snapshot()) != 0 {
		t.Fatal("system command/rejection received agent markers")
	}
}

func TestReactionWorkerJoinsCanceledNetworkRequest(t *testing.T) {
	f := newServiceFixture(t)
	entered, stopped := make(chan struct{}), make(chan struct{})
	f.service.Reactor.(*serviceReactor).send = func(ctx context.Context, _ reactionCall) error {
		close(entered)
		defer close(stopped)
		<-ctx.Done()
		return ctx.Err()
	}
	f.receive(t, f.message(t, "cancel", "Explain this project.", "text", "dm"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reaction not started")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("network worker not joined")
	}
	stages, err := f.service.Store.ChannelReactions(context.Background(), Key("dingtalk", f.service.ClientID, "cancel"))
	if err != nil || len(stages) == 0 || stages[0].Status != "unknown" {
		t.Fatalf("canceled attempt lost: %+v %v", stages, err)
	}
}

func TestReactionRemovalCrashDoesNotRepeatCleanupOrNativeWork(t *testing.T) {
	f := newServiceFixture(t)
	input := f.message(t, "crash", "Explain this project.", "text", "dm")
	f.receive(t, input)
	drainReactions(t, f.service)
	m := f.next(t)
	if _, err := f.service.Store.ClaimNextChannelReaction(context.Background(), f.service.Profile); err != nil {
		t.Fatal(err)
	}
	previous, err := f.service.Store.ActiveChannelReactions(context.Background(), m.ID)
	if err != nil || len(previous) != 1 {
		t.Fatalf("previous stage: %+v %v", previous, err)
	}
	if err := f.service.Store.ClaimChannelReactionRemoval(context.Background(), previous[0].ID); err != nil {
		t.Fatal(err)
	}
	// Simulate a successful remote remove followed by a crash before its commit.
	if err := f.service.Reactor.RemoveReaction(context.Background(), input.ConversationID, input.ID, previous[0].Label); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.RecoverChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainReactions(t, f.service)
	calls := f.service.Reactor.(*serviceReactor).snapshot()
	if len(calls) != 2 || calls[1].action != "remove" {
		t.Fatalf("crash repeated platform operation: %+v", calls)
	}
	got := f.receipt(t, input.ID)
	if got.Status != "unknown" || got.Reactions[0].RemovalStatus != "unknown" || got.Reactions[len(got.Reactions)-1].Status != "unknown" || len(f.runner.snapshot()) != 0 || len(f.replier.snapshot()) != 0 {
		t.Fatalf("crash state overclaimed or task replayed: %+v", got)
	}
}

func TestLegacyInternalReactionOutboxIsWithheldUnderPublicPolicy(t *testing.T) {
	f := newServiceFixture(t)
	m := f.message(t, "legacy-phases", "Explain the task.", "text", "dm")
	m.ConversationType = ""
	f.host.Sink = func(event event.Event) error {
		drainReactions(t, f.service)
		return nil
	}
	completed := f.process(t, m)
	drainReactions(t, f.service)
	if completed.Status != "completed" {
		t.Fatal(completed)
	}
	skipped := 0
	for _, reaction := range f.receipt(t, m.ID).Reactions {
		if reaction.Status == "skipped" && reaction.Error == "internal phase is not public progress" {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("legacy phase guard was not exercised")
	}
	for _, call := range f.service.Reactor.(*serviceReactor).snapshot() {
		if call.action == "add" && call.label != "已收到" && call.label != "处理中" && call.label != "已完成" && call.label != "打叉" {
			t.Fatal("legacy phase disclosed", call)
		}
	}
}
