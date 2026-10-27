package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func TestEarlyReplyPrecedesDeciderAndCloseoutFailureNeverResends(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f := newServiceFixture(t)
			var mu sync.Mutex
			var observed []string
			f.service.Observe = func(kind, message string) error {
				mu.Lock()
				defer mu.Unlock()
				observed = append(observed, kind+" "+message)
				return nil
			}
			repliesAtDecision := -1
			f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
				repliesAtDecision = len(f.replier.snapshot())
				return memory.Decision{}, errors.New("fixture closeout failure")
			})
			f.replier.reply = func(context.Context, replyCall) (string, error) {
				if outcome == "accepted" {
					return "accepted", nil
				}
				return outcome, errors.New("fixture delivery " + outcome)
			}
			m := f.process(t, f.message(t, "early", "Explain this project.", "text", "early-dm"))
			run := f.run(t, m)
			if repliesAtDecision != 1 {
				t.Fatalf("answer must be attempted before the decider starts, got %d", repliesAtDecision)
			}
			if len(f.replier.snapshot()) != 1 || len(f.runner.snapshot()) != 1 {
				t.Fatalf("closeout failure caused a second answer or native replay: replies=%d native=%d", len(f.replier.snapshot()), len(f.runner.snapshot()))
			}
			if m.Status != "completed" || m.ReplyStatus != outcome || run.ExecutionStatus != "completed" || run.MemoryStatus != "failed" {
				t.Fatalf("closeout failure rewrote the delivered outcome: message=%+v run=%+v", m, run)
			}
			if strings.Contains(m.ReplyText, "后续保存步骤失败") {
				t.Fatalf("early answer carried a closeout warning: %q", m.ReplyText)
			}
			mu.Lock()
			defer mu.Unlock()
			var closeout bool
			for _, o := range observed {
				if strings.HasPrefix(o, "channel.closeout ") && strings.Contains(o, "status=failed") && strings.Contains(o, run.ID) {
					closeout = true
				}
			}
			if !closeout {
				t.Fatalf("closeout failure was not made visible: %v", observed)
			}
		})
	}
}

func TestEarlyReplyHandlerIgnoresForeignOrIncompleteRuns(t *testing.T) {
	f := newServiceFixture(t)
	m := state.ChannelMessage{ID: "message-1"}
	e := &earlyReply{}
	handler := f.service.earlyReplyHandler(m, e)
	if err := handler(context.Background(), state.Run{ID: "r", ExecutionStatus: "failed"}); err != nil || e.started() {
		t.Fatalf("a non-completed run must not start the early reply: %v", err)
	}
	if err := handler(context.Background(), state.Run{ID: "r", ExecutionStatus: "completed", Source: &state.RunSource{ChannelMessageID: "other"}}); err == nil || e.started() {
		t.Fatalf("a run for another message must be rejected without delivery: %v", err)
	}
	if len(f.replier.snapshot()) != 0 {
		t.Fatal("rejected runs delivered an answer")
	}
}

func TestCancelCloseoutAfterEarlyReplyPreservesDelivery(t *testing.T) {
	f := newServiceFixture(t)
	entered := make(chan struct{})
	f.host.Decider = serviceDecider(func(ctx context.Context, _ string, _ []state.Memory) (memory.Decision, error) {
		close(entered)
		<-ctx.Done()
		return memory.Decision{}, ctx.Err()
	})
	input := f.message(t, "cancel-closeout", "Explain this project.", "text", "cancel-dm")
	f.receive(t, input)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	claimed := f.next(t)
	done := make(chan error, 1)
	go func() { done <- f.service.process(ctx, claimed) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("closeout did not start")
	}
	before := f.receipt(t, input.ID)
	if before.ReplyStatus != "accepted" || len(f.replier.snapshot()) != 1 {
		t.Fatal("answer was not delivered before closeout", before)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closeout did not stop after cancellation")
	}
	after := f.receipt(t, input.ID)
	run := f.run(t, after)
	if after.Status != "completed" || after.ReplyStatus != "accepted" || after.ReplyText != before.ReplyText || run.ExecutionStatus != "completed" || run.MemoryStatus != "failed" || len(f.replier.snapshot()) != 1 || len(f.runner.snapshot()) != 1 {
		t.Fatalf("canceled closeout changed native execution or delivery: message=%+v run=%+v", after, run)
	}
}

func TestExperienceSourceOverflowPreservesChannelDelivery(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f := newServiceFixture(t)
			f.host.Runner = phaseToolRunner{paired: true}
			f.host.Config.ToolsDisabled = false
			f.host.Judge = &phaseJudge{}
			f.host.Extractor = phaseExtractor(func(ctx context.Context, run state.Run, evidence []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
				run.Output = strings.Repeat("x", experience.MaxSourceBytes)
				return experience.NewExtractor(f.host.Runner, "fixture").Extract(ctx, run, evidence, emit)
			})
			f.replier.reply = func(context.Context, replyCall) (string, error) {
				if outcome == "accepted" {
					return outcome, nil
				}
				return outcome, errors.New("fixture delivery " + outcome)
			}
			m := f.process(t, f.message(t, "overflow", "Explain this project.", "text", "overflow-dm"))
			run := f.run(t, m)
			if m.Status != "completed" || m.ReplyStatus != outcome || run.ExecutionStatus != "completed" || run.ExperienceStatus != "failed" || run.MemoryStatus != "unchanged" || !strings.Contains(run.ExperienceError, "entire payload unprocessed") {
				t.Fatalf("overflow confused stage outcomes: message=%+v run=%+v", m, run)
			}
			if len(f.replier.snapshot()) != 1 {
				t.Fatal("overflow resent answer")
			}
		})
	}
}
