package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/state"
	"agent-host/internal/taskfeedback"
)

func TestFeedbackIsBoundBeforeExecutionAndContinuationSeesPriorPlan(t *testing.T) {
	h, runner := testHost(t)
	s := newSession(t, h)
	bound := 0
	h.Config.PrepareFeedback = func(ctx context.Context, run state.Run, previous *taskfeedback.Plan) (*taskfeedback.Plan, error) {
		if bound == 1 && (previous == nil || previous.NotifyTo != "alice") {
			t.Fatal("continuation lost prior attribution", previous)
		}
		return &taskfeedback.Plan{Requester: "owner", RequestFor: "alice", NotifyTo: "alice", Mode: "notify"}, nil
	}
	h.Config.BindFeedback = func(ctx context.Context, run state.Run) error {
		saved, err := h.Store.Run(ctx, run.ID)
		if err != nil || saved.Feedback == nil || len(runner.requests) != bound {
			t.Fatal("execution preceded durable feedback plan", err)
		}
		bound++
		return nil
	}
	for range 2 {
		run, err := h.Run(t.Context(), s, "Continue this task")
		if err != nil || run.Feedback == nil || run.Feedback.NotifyTo != "alice" {
			t.Fatal(run.Feedback, err)
		}
	}
	if len(runner.requests) != 2 || !strings.Contains(runner.requests[0].HostInstructions, "Host-bound task feedback") || !strings.Contains(runner.requests[0].HostInstructions, "Do not send a duplicate") {
		t.Fatal("native task lost feedback responsibility", runner.requests)
	}
}

func TestFeedbackQueueFailurePreventsUnreportableExecution(t *testing.T) {
	h, runner := testHost(t)
	s := newSession(t, h)
	h.Config.PrepareFeedback = func(context.Context, state.Run, *taskfeedback.Plan) (*taskfeedback.Plan, error) {
		return &taskfeedback.Plan{Mode: "notify", NotifyTo: "alice"}, nil
	}
	h.Config.BindFeedback = func(context.Context, state.Run) error { return errors.New("feedback storage unavailable") }
	_, err := h.Run(t.Context(), s, "Do work")
	if err == nil || len(runner.requests) != 0 {
		t.Fatal("unreportable work executed", err, len(runner.requests))
	}
}
