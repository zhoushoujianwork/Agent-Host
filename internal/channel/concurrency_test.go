package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/routing"
)

func newConcurrentFixture(t *testing.T) *projectFixture {
	f := newRoutingFixture(t)
	f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
		return routing.Decision{Kind: "default", Reason: "Independent fixture task."}, nil
	})
	instanceDispatch(t, f, map[string]string{})
	return f
}

func waitForWorkerSignal[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal("timed out waiting for conversation worker", ctx.Err())
		var zero T
		return zero
	}
}

func TestConversationWorkersKeepGroupFIFOAcrossMembersWithoutBlockingOtherAudiences(t *testing.T) {
	f := newConcurrentFixture(t)
	f.service.MaxConcurrentConversations = 2
	first := groupInput(t, f.serviceFixture, "a-first", "alice", "group-a", "Inspect alpha blocked-first")
	second := groupInput(t, f.serviceFixture, "a-second", "bob", "group-a", "Inspect alpha same-followup")
	other := groupInput(t, f.serviceFixture, "b-first", "bob", "group-b", "Inspect alpha independent-group")
	private := f.message(t, "private", "Inspect alpha independent-private", "text", "owner-dm")
	f.receive(t, first)
	f.receive(t, second)
	f.receive(t, other)
	f.receive(t, private)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		if strings.Contains(req.Prompt, "blocked-first") {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return harness.Result{Status: "canceled", Error: ctx.Err().Error()}, ctx.Err()
			}
		}
		return harness.Result{Status: "completed", Text: "answer", Model: req.Model}, nil
	}
	delivered := make(chan string, 4)
	f.replier.reply = func(_ context.Context, call replyCall) (string, error) {
		delivered <- call.url
		return "accepted", nil
	}
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first group task did not enter native execution", f.receipt(t, first.ID), f.runner.snapshot())
	case err := <-done:
		t.Fatal("workers terminated early", err)
	}
	for range 2 {
		url := waitForWorkerSignal(t, ctx, delivered)
		if !strings.Contains(url, "cap-b-first") && !strings.Contains(url, "cap-private") {
			t.Fatal("same-group work overtook the first member", url)
		}
	}
	f.receive(t, first)
	if got := f.receipt(t, second.ID); got.Status != "queued" || got.Binding != f.receipt(t, first.ID).Binding {
		t.Fatal("same group lost sender-independent serialization", got)
	}
	if len(f.runner.snapshot()) != 3 {
		t.Fatal("duplicate or same-group message executed early", f.runner.snapshot())
	}
	close(release)
	for range 2 {
		waitForWorkerSignal(t, ctx, delivered)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a, b := f.receipt(t, first.ID), f.receipt(t, second.ID)
	if a.SessionID != b.SessionID || a.SessionID == f.receipt(t, other.ID).SessionID || a.SessionID == f.receipt(t, private.ID).SessionID || b.ReplyStatus != "accepted" {
		t.Fatal("conversation continuation or audience isolation failed", a, b)
	}
	requests := f.runner.snapshot()
	if len(requests) != 4 || !requests[3].Resume || requests[3].NativeSessionID != aNativeSession(requests, "blocked-first") {
		t.Fatal("same-group followup did not resume exactly once", requests)
	}
}

func aNativeSession(requests []harness.Request, text string) string {
	for _, req := range requests {
		if strings.Contains(req.Prompt, text) {
			return req.NativeSessionID
		}
	}
	return ""
}

func TestConversationWorkersHoldBindingThroughReplyDelivery(t *testing.T) {
	f := newConcurrentFixture(t)
	f.service.MaxConcurrentConversations = 2
	f.receive(t, groupInput(t, f.serviceFixture, "a1", "alice", "a", "First"))
	f.receive(t, groupInput(t, f.serviceFixture, "a2", "bob", "a", "Second"))
	f.receive(t, groupInput(t, f.serviceFixture, "b1", "alice", "b", "Other"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstReply := make(chan struct{}, 1)
	release := make(chan struct{})
	delivered := make(chan string, 3)
	f.replier.reply = func(ctx context.Context, call replyCall) (string, error) {
		if strings.Contains(call.url, "cap-a1") {
			firstReply <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return "unknown", ctx.Err()
			}
		}
		delivered <- call.url
		return "accepted", nil
	}
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	waitForWorkerSignal(t, ctx, firstReply)
	if url := waitForWorkerSignal(t, ctx, delivered); !strings.Contains(url, "cap-b1") {
		t.Fatal("same conversation passed a pending reply", url)
	}
	if len(f.runner.snapshot()) != 2 || f.receipt(t, "a2").Status != "queued" {
		t.Fatal("terminal execution released binding before delivery")
	}
	close(release)
	for range 2 {
		waitForWorkerSignal(t, ctx, delivered)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls := f.replier.snapshot(); len(calls) != 3 || !strings.Contains(calls[2].url, "cap-a2") {
		t.Fatal("reply order changed", calls)
	}
}

func TestConversationWorkersBoundConcurrencyAndJoinCancellation(t *testing.T) {
	for _, limit := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			f := newConcurrentFixture(t)
			f.service.MaxConcurrentConversations = limit
			want := limit
			if want == 0 {
				want = 4
			}
			for i := range want + 2 {
				id := fmt.Sprintf("message-%d", i)
				f.receive(t, groupInput(t, f.serviceFixture, id, "alice", id, "Independent task"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered := make(chan struct{}, want+2)
			var exited atomic.Int64
			f.runner.run = func(ctx context.Context, _ harness.Request) (harness.Result, error) {
				entered <- struct{}{}
				<-ctx.Done()
				exited.Add(1)
				return harness.Result{Status: "canceled", Error: ctx.Err().Error()}, ctx.Err()
			}
			done := make(chan error, 1)
			go func() { done <- f.service.Work(ctx) }()
			for range want {
				waitForWorkerSignal(t, ctx, entered)
			}
			if len(f.runner.snapshot()) != want || f.receipt(t, fmt.Sprintf("message-%d", want)).Status != "queued" {
				t.Fatal("conversation worker limit exceeded")
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) || exited.Load() != int64(want) {
				t.Fatal("service returned before its native workers ended", err, exited.Load())
			}
			if err := f.service.Store.RecoverChannels(context.Background()); err != nil {
				t.Fatal(err)
			}
			for i := range want {
				m := f.receipt(t, fmt.Sprintf("message-%d", i))
				if m.Status != "failed" || m.RunID == "" || m.ReplyStatus != "accepted" {
					t.Fatal("cancellation lost the durable outcome", m)
				}
			}
			if next := f.next(t); next.MessageID != fmt.Sprintf("message-%d", want) {
				t.Fatal("restart replayed claimed work", next)
			}
		})
	}
}

func TestConversationWorkerConfigurationValidation(t *testing.T) {
	f := newServiceFixture(t)
	for _, value := range []int{-1, 33} {
		f.service.MaxConcurrentConversations = value
		if err := f.service.Validate(); err == nil {
			t.Fatal("invalid worker limit accepted", value)
		}
	}
	for _, value := range []int{0, 1, 4, 32} {
		f.service.MaxConcurrentConversations = value
		if err := f.service.Validate(); err != nil {
			t.Fatal(value, err)
		}
	}
}

func TestConversationWorkersSerializeDifferentOwnerBindingsThroughMemory(t *testing.T) {
	f := newConcurrentFixture(t)
	first := f.message(t, "owner-a", "First owner request", "text", "owner-conversation-a")
	second := f.message(t, "owner-b", "Second owner request", "text", "owner-conversation-b")
	other := groupInput(t, f.serviceFixture, "group", "alice", "group", "Independent group request")
	f.receive(t, first)
	f.receive(t, second)
	f.receive(t, other)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		if strings.Contains(req.Prompt, first.Text) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return harness.Result{Status: "canceled", Error: ctx.Err().Error()}, ctx.Err()
			}
		}
		return harness.Result{Status: "completed", Text: "answer", Model: req.Model}, nil
	}
	delivered := make(chan string, 3)
	f.replier.reply = func(_ context.Context, call replyCall) (string, error) {
		delivered <- call.url
		return "accepted", nil
	}
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	waitForWorkerSignal(t, ctx, entered)
	if url := waitForWorkerSignal(t, ctx, delivered); !strings.Contains(url, "cap-group") {
		t.Fatal("owner memory partition was executed concurrently", url)
	}
	a, b := f.receipt(t, first.ID), f.receipt(t, second.ID)
	if a.Binding == b.Binding || a.AudienceID != b.AudienceID || b.Status != "queued" {
		t.Fatal("shared partition did not serialize distinct bindings", a, b)
	}
	close(release)
	for range 2 {
		waitForWorkerSignal(t, ctx, delivered)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || len(f.runner.snapshot()) != 3 {
		t.Fatal("owner conversation did not continue after release", err)
	}
}

func TestConversationWorkerFatalFailureCancelsAndJoinsOtherConversations(t *testing.T) {
	f := newConcurrentFixture(t)
	f.service.MaxConcurrentConversations = 2
	active := groupInput(t, f.serviceFixture, "active", "alice", "a", "Active task")
	failure := groupInput(t, f.serviceFixture, "failure", "alice", "b", "Unstarted task")
	queued := groupInput(t, f.serviceFixture, "queued", "bob", "a", "Later task")
	f.receive(t, active)
	f.receive(t, failure)
	f.receive(t, queued)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	var exited atomic.Bool
	f.runner.run = func(ctx context.Context, _ harness.Request) (harness.Result, error) {
		close(entered)
		<-ctx.Done()
		exited.Store(true)
		return harness.Result{Status: "canceled", Error: ctx.Err().Error()}, ctx.Err()
	}
	fatal := errors.New("fixture channel journal failed")
	failureID := Key("dingtalk", f.service.ClientID, failure.ID)
	f.service.Observe = func(kind, message string) error {
		if kind == "channel.processing" && strings.Contains(message, failureID) {
			select {
			case <-entered:
				return fatal
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	if err := f.service.Work(ctx); !errors.Is(err, fatal) || !exited.Load() {
		t.Fatal("fatal worker error did not cancel and join peers", err)
	}
	if len(f.runner.snapshot()) != 1 || f.receipt(t, queued.ID).Status != "queued" {
		t.Fatal("unstarted work was executed during shutdown")
	}
	if err := f.service.Store.RecoverChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m := f.receipt(t, failure.ID); m.Status != "unknown" || m.RunID != "" {
		t.Fatal("claimed failure was automatically replayed", m)
	}
	if m := f.next(t); m.MessageID != queued.ID {
		t.Fatal("restart did not preserve only unclaimed work", m)
	}
}
