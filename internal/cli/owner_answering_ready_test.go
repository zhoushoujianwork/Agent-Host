package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/owneranswering"
	"agent-host/internal/state"
)

func TestAnsweringExecutionReadyBridgesParsedOutcomeNotRawOutput(t *testing.T) {
	var got owneranswering.Outcome
	var calls int
	handler := answeringExecutionReady(func(o owneranswering.Outcome, err error) error {
		calls++
		got = o
		if err != nil {
			t.Fatal("a valid outcome carried an error", err)
		}
		return nil
	})
	raw := "Private progress and tool narration\n" + outcomeBlock(fixtureOutcome) + "\n"
	run := state.Run{ID: "run-1", ExecutionStatus: "completed", Output: raw}
	if err := handler(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Outcome != "answer" || got.Text != "Grounded answer" || got.RunID != "run-1" || strings.Contains(got.Text, "Private progress") {
		t.Fatalf("bridge must pass the parsed outcome only: calls=%d %+v", calls, got)
	}
}

func TestAnsweringExecutionReadyKeepsInvalidAndFailedRunsOnTheSafeguardPath(t *testing.T) {
	var outcome owneranswering.Outcome
	var outcomeErr error
	handler := answeringExecutionReady(func(o owneranswering.Outcome, err error) error {
		outcome, outcomeErr = o, err
		return nil
	})
	// Unparseable output is reported as an invalid outcome, never forwarded raw.
	if err := handler(context.Background(), state.Run{ID: "bad", ExecutionStatus: "completed", Output: "plain unmarked answer"}); err != nil {
		t.Fatal(err)
	}
	if outcomeErr == nil || !strings.Contains(outcomeErr.Error(), "invalid handler outcome") || outcome.Text != "" {
		t.Fatalf("invalid outcome was not rejected: %+v %v", outcome, outcomeErr)
	}
	// A blocked outcome keeps its reason for the private owner escalation.
	blocked := outcomeBlock(`{"outcome":"blocked","text":"","reason":"needs approval","clarification":"Approve?"}`)
	if err := handler(context.Background(), state.Run{ID: "blocked", ExecutionStatus: "completed", Output: blocked}); err != nil {
		t.Fatal(err)
	}
	if outcomeErr != nil || outcome.Outcome != "blocked" || outcome.Reason != "needs approval" {
		t.Fatalf("blocked outcome changed: %+v %v", outcome, outcomeErr)
	}
	// Non-completed runs never reach the callback.
	called := false
	skip := answeringExecutionReady(func(owneranswering.Outcome, error) error { called = true; return nil })
	if err := skip(context.Background(), state.Run{ID: "failed", ExecutionStatus: "failed"}); err != nil || called {
		t.Fatalf("failed run reached the delivery callback: %v", err)
	}
}

func TestAnsweringExecutionReadyWithoutCoordinatorCallbackKeepsLegacyPath(t *testing.T) {
	if answeringExecutionReady(nil) != nil {
		t.Fatal("a nil coordinator callback must leave the legacy post-closeout path")
	}
	failing := answeringExecutionReady(func(owneranswering.Outcome, error) error { return errors.New("already handled") })
	if err := failing(context.Background(), state.Run{ExecutionStatus: "completed", Output: outcomeBlock(fixtureOutcome)}); err == nil {
		t.Fatal("callback error must surface to Host for journaling")
	}
}
