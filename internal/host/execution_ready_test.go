package host

import (
	"context"
	"errors"
	"sync"
	"testing"

	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/runcompletion"
	"agent-host/internal/state"
)

type orderLog struct {
	mu    sync.Mutex
	steps []string
}

func (o *orderLog) add(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, step)
}

func (o *orderLog) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

func eventTypes(r state.Run) []string {
	var out []string
	for _, e := range r.Events {
		out = append(out, e.Type)
	}
	return out
}

func indexOf(types []string, want string) int {
	for i, t := range types {
		if t == want {
			return i
		}
	}
	return -1
}

func TestExecutionReadyRunsOnceBeforeDeciderAndExtractor(t *testing.T) {
	h, _, _ := experienceHost(t)
	order := &orderLog{}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		order.add("decide")
		return memory.Decision{Kind: "none", Model: "resolved-jev"}, nil
	})
	inner := h.Extractor
	h.Extractor = experienceExtractorFunc(func(ctx context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
		order.add("extract")
		return inner.Extract(ctx, r, e, emit)
	})
	session := newSession(t, h)
	var seen state.Run
	ctx := runcompletion.With(t.Context(), func(_ context.Context, run state.Run) error {
		order.add("ready")
		seen = run
		return nil
	})
	final, err := h.Run(ctx, session, "Fix the fixture decrement")
	if err != nil {
		t.Fatal(err)
	}
	steps := order.snapshot()
	if len(steps) < 3 || steps[0] != "ready" || steps[1] != "decide" || steps[2] != "extract" {
		t.Fatalf("handler must run exactly once before closeout: %v", steps)
	}
	for _, s := range steps[1:] {
		if s == "ready" {
			t.Fatalf("handler ran more than once: %v", steps)
		}
	}
	if seen.ExecutionStatus != "completed" || seen.MemoryStatus != "pending" || seen.ExperienceStatus != "pending" || seen.Output == "" || seen.AcceptanceStatus != "unverified" {
		t.Fatalf("handler must see persisted completed execution with pending closeout: %+v", seen)
	}
	// receipt snapshots the run before it appends run.finished, so reload.
	final, err = h.Store.Run(t.Context(), final.ID)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(final)
	ready, finished := indexOf(types, "execution.ready"), indexOf(types, "run.finished")
	if ready < 0 || finished < 0 || ready > finished || indexOf(types, "execution.ready") != lastIndex(types, "execution.ready") {
		t.Fatalf("execution.ready must be a single event before run.finished: %v", types)
	}
	data := final.Events[ready].Data
	if data["output"] != seen.Output || data["memory_status"] != "pending" || data["closeout"] != "pending" || final.Events[ready].Agent == "" {
		t.Fatalf("execution.ready lost attributed persisted output/status: %+v", final.Events[ready])
	}
	if final.MemoryStatus == "pending" || final.ExecutionStatus != "completed" {
		t.Fatalf("closeout did not finish after delivery: %+v", final)
	}
}

func lastIndex(types []string, want string) int {
	for i := len(types) - 1; i >= 0; i-- {
		if types[i] == want {
			return i
		}
	}
	return -1
}

func TestExecutionReadyHandlerErrorIsJournaledAndCloseoutContinues(t *testing.T) {
	h, runner := testHost(t)
	decided := 0
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		decided++
		return memory.Decision{Kind: "none", Model: "resolved-jev"}, nil
	})
	calls := 0
	ctx := runcompletion.With(t.Context(), func(context.Context, state.Run) error {
		calls++
		return errors.New("delivery refused")
	})
	run, err := h.Run(ctx, newSession(t, h), "hello")
	if err != nil {
		t.Fatalf("handler failure must be journaled, not returned as a closeout failure: %v", err)
	}
	if calls != 1 || decided != 1 || len(runner.requests) != 1 {
		t.Fatalf("handler=%d decide=%d native=%d: closeout must continue without replay or second attempt", calls, decided, len(runner.requests))
	}
	if run.MemoryStatus == "pending" || run.MemoryStatus == "failed" {
		t.Fatalf("closeout did not complete: %+v", run)
	}
	run, err = h.Store.Run(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var failure bool
	for _, e := range run.Events {
		if e.Type == "execution.ready.handler_failed" && e.Status == "failed" && e.Data["native_replayed"] == false && e.Message == "delivery refused" {
			failure = true
		}
	}
	if !failure || indexOf(eventTypes(run), "run.finished") < 0 {
		t.Fatalf("handler failure was not independently journaled: %v", eventTypes(run))
	}
}

func TestExecutionReadyIsNotOfferedForNativeFailureOrCancel(t *testing.T) {
	for _, status := range []string{"failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			h, runner := testHost(t)
			runner.status = status
			calls := 0
			ctx := runcompletion.With(t.Context(), func(context.Context, state.Run) error {
				calls++
				return nil
			})
			run, err := h.Run(ctx, newSession(t, h), "hello")
			if err == nil || calls != 0 {
				t.Fatalf("native %s must use the error path: err=%v handler calls=%d", status, err, calls)
			}
			if run.ExecutionStatus != status || indexOf(eventTypes(run), "execution.ready") >= 0 {
				t.Fatalf("native %s emitted a success signal: %+v", status, eventTypes(run))
			}
		})
	}
}

func TestExecutionReadyEventExposesOutputWithoutHandler(t *testing.T) {
	h, _ := testHost(t)
	run, err := h.Run(t.Context(), newSession(t, h), "hello")
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(eventTypes(run), "execution.ready")
	if i < 0 || run.Events[i].Data["output"] != "answer" || run.Events[i].Data["handler"] != false {
		t.Fatalf("observers need the final output even with no handler: %+v", run.Events)
	}
}
