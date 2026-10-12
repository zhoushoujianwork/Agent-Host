package channel

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

// These synthetic conversations exercise the channel boundary with a scripted
// semantic decision. Router tests cover interpretation; these tests verify the
// source context and runtime that a decision actually receives and executes.
func TestQueuedNaturalFollowupUsesLatestExecutedTaskAndLeavesPendingWork(t *testing.T) {
	f := newRoutingFixture(t)
	pending := legacyPending(t, f, f.message(t, "pending", "ambiguous deployment task", "text", "dm"))
	firstInput := f.message(t, "first", "Inspect project alpha's cache policy.", "text", "dm")
	followupInput := f.message(t, "followup", "Search the handbook for that setting.", "text", "dm")
	var latest routing.TaskContext
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		if req.Text == firstInput.Text {
			if len(req.Recent) != 0 {
				t.Fatal("unexecuted pending task entered routing context", req.Recent)
			}
			return routing.Decision{Kind: "project", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha", Reason: "The request names alpha."}, nil
		}
		if req.Text != followupInput.Text || len(req.Recent) != 1 {
			t.Fatal("follow-up lost its latest source context", req)
		}
		latest = req.Recent[0]
		first := f.receipt(t, firstInput.ID)
		if latest.MessageID != firstInput.ID || latest.RunID != first.RunID || latest.Text != firstInput.Text || !reflect.DeepEqual(latest.ProjectIDs, []string{"alpha"}) || latest.PrimaryID != "alpha" {
			t.Fatal("follow-up received a guessed or incomplete task source", latest, first)
		}
		return continueLatestTask(latest), nil
	})
	// Both receipts exist before either task executes. Routing must happen only
	// after the previous queued task has saved its actual execution context.
	f.receive(t, firstInput)
	f.receive(t, followupInput)
	f.receive(t, followupInput)
	for range 2 {
		if err := f.service.process(context.Background(), f.next(t)); err != nil {
			t.Fatal(err)
		}
	}
	first, followup := f.receipt(t, firstInput.ID), f.receipt(t, followupInput.ID)
	requests := f.runner.snapshot()
	choice, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(pending))
	if err != nil || choice.Status != "pending" || pending.RunID != "" || len(requests) != 2 || followup.Status != "completed" || followup.ProjectID != "alpha" || followup.Workspace != f.projects["alpha"].Workspace || followup.SessionID != first.SessionID || !requests[1].Resume || requests[1].Workspace != followup.Workspace {
		t.Fatal("natural follow-up consumed pending work or lost native continuity", first, followup, choice, requests, err)
	}
	run := f.run(t, followup)
	if run.Input != followupInput.Text || run.Source == nil || run.Source.MessageID != followupInput.ID || !strings.Contains(requests[1].Prompt, followupInput.Text) {
		t.Fatal("continuation replaced the new request with the previous task", run)
	}
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate follow-up executed again", err)
	}
}

func TestEverydayMissingDetailsUseDefaultAndBecomeLatestContext(t *testing.T) {
	f := newRoutingFixture(t)
	project := f.process(t, f.message(t, "project", "Inspect project alpha.", "text", "dm"))
	pending := legacyPending(t, f, f.message(t, "pending", "ambiguous project repair", "text", "dm"))
	const everyday = "Explain the quoted message; ask me for it if it is missing."
	const followup = "Give a shorter explanation."
	var ordinary state.ChannelMessage
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		if req.Text == everyday {
			if len(req.Recent) != 1 || req.Recent[0].RunID != project.RunID {
				t.Fatal("pending project task replaced latest execution", req.Recent)
			}
			return routing.Decision{Kind: "default", Reason: "Missing quoted content is a conversational detail."}, nil
		}
		if req.Text != followup || len(req.Recent) != 2 || req.Recent[0].MessageID != ordinary.MessageID || req.Recent[0].RunID != ordinary.RunID || len(req.Recent[0].ProjectIDs) != 0 || req.Recent[1].RunID != project.RunID {
			t.Fatal("ordinary turn was skipped in favor of an older project", req.Recent)
		}
		return continueLatestTask(req.Recent[0]), nil
	})
	ordinary = f.process(t, f.message(t, "ordinary", everyday, "text", "dm"))
	continued := f.process(t, f.message(t, "continued", followup, "text", "dm"))
	choice, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(pending))
	requests := f.runner.snapshot()
	if err != nil || choice.Status != "pending" || len(requests) != 3 || ordinary.Status != "completed" || ordinary.ProjectID != "" || ordinary.Workspace != f.host.Config.Scope || ordinary.SessionID == project.SessionID || continued.Status != "completed" || continued.ProjectID != "" || continued.SessionID != ordinary.SessionID || !requests[2].Resume {
		t.Fatal("ordinary request entered project picker or inherited stale project context", ordinary, continued, choice, err)
	}
	if f.run(t, ordinary).Input != everyday || f.run(t, continued).Input != followup {
		t.Fatal("native assistant did not receive the missing-detail question intact")
	}
}

func TestNaturalFollowupContextRespectsVerifiedIdentityAndExplicitReset(t *testing.T) {
	for _, boundary := range []string{"sender", "tenant", "conversation", "new"} {
		t.Run(boundary, func(t *testing.T) {
			f := newRoutingFixture(t)
			first := f.process(t, f.message(t, "first", "Inspect project alpha.", "text", "dm"))
			conversation := "dm"
			switch boundary {
			case "sender":
				// The fixture parser verifies the replacement owner before Receive.
				f.config.OwnerUserID = "another-verified-owner"
			case "tenant":
				f.config.CorpID = "another-verified-tenant"
			case "conversation":
				conversation = "another-dm"
			case "new":
				f.process(t, f.message(t, "reset", "/new", "text", "dm"))
			}
			f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
				if len(req.Recent) != 0 {
					t.Fatal("old context crossed identity or explicit reset boundary", req.Recent)
				}
				return routing.Decision{Kind: "default", Reason: "Ask for the missing conversation detail in the assistant."}, nil
			})
			followup := f.process(t, f.message(t, "followup", "Which setting did you mean?", "text", conversation))
			requests := f.runner.snapshot()
			if followup.Status != "completed" || followup.ProjectID != "" || followup.SessionID == first.SessionID || len(requests) != 2 || requests[1].Resume || requests[1].Workspace != f.host.Config.Scope {
				t.Fatal("unbound follow-up borrowed the previous native session", followup, requests)
			}
		})
	}
}

func TestNaturalFollowupAfterFailureKeepsSourceButDoesNotReplayTask(t *testing.T) {
	f := newRoutingFixture(t)
	f.runner.run = func(context.Context, harness.Request) (harness.Result, error) {
		return harness.Result{Status: "failed"}, errors.New("synthetic native connection failure")
	}
	failed := f.process(t, f.message(t, "failed", "Inspect project alpha.", "text", "dm"))
	if failed.Status != "failed" || failed.RunID == "" {
		t.Fatal("synthetic native failure did not execute", failed)
	}
	f.runner.run = nil
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		if len(req.Recent) != 1 || req.Recent[0].MessageID != failed.MessageID || req.Recent[0].RunID != failed.RunID || req.Recent[0].Text != failed.Text {
			t.Fatal("failed execution lost its source-linked topic", req.Recent)
		}
		return continueLatestTask(req.Recent[0]), nil
	})
	const followup = "Only list the relevant documentation titles now."
	recovered := f.process(t, f.message(t, "new-request", followup, "text", "dm"))
	requests := f.runner.snapshot()
	run := f.run(t, recovered)
	session, err := f.service.Store.Session(context.Background(), recovered.SessionID)
	if err != nil || failed.Status != "failed" || recovered.Status != "completed" || recovered.ProjectID != "alpha" || recovered.SessionID == failed.SessionID || session.PreviousSessionID != failed.SessionID || session.RecoveryRunID != failed.RunID || len(requests) != 2 || requests[1].Resume || run.Input != followup || run.Source == nil || run.Source.MessageID != "new-request" || !strings.Contains(recovered.ReplyText, "旧任务没有自动重做") {
		t.Fatal("fresh follow-up replayed failed work or lost recovery evidence", failed, recovered, session, requests, err)
	}
	if f.run(t, failed).ExecutionStatus != "failed" {
		t.Fatal("follow-up rewrote the failed run")
	}
}

func continueLatestTask(task routing.TaskContext) routing.Decision {
	kind := "project"
	if len(task.ProjectIDs) == 0 {
		kind = "default"
	}
	return routing.Decision{Kind: kind, ProjectIDs: task.ProjectIDs, PrimaryID: task.PrimaryID, ContinuationMessageID: task.MessageID, ContinuationRunID: task.RunID, Reason: "Continue the source-linked latest task."}
}
