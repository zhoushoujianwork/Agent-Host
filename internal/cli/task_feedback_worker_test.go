package cli

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/state"
	"agent-host/internal/taskfeedback"
)

func feedbackWorkerFixture(t *testing.T, completed bool) (*taskfeedback.Queue, options, taskfeedback.Account) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a := taskfeedback.Account{Channel: "feishu", AccountID: "cli_app", TenantID: "tenant", SenderID: "ou_alice"}
	writeBotJSON(t, filepath.Join(dir, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", Accounts: []userAccount{{Channel: a.Channel, AccountID: a.AccountID, TenantID: a.TenantID, SenderID: a.SenderID}}}}})
	home := filepath.Join(root, "cli-state")
	release, err := state.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateSession(t.Context(), state.Session{ID: "session", Scope: root, NativeSessionID: "native"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(t.Context(), state.Run{ID: "run", SessionID: "session", NativeSessionID: "native", Scope: root, ExecutionStatus: "running", MemoryStatus: "blocked", AcceptanceStatus: "unverified", Input: "Check the service", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if completed {
		if err = s.CompleteRun(t.Context(), "run", "completed", "Service healthy", "observed-model", ""); err != nil {
			t.Fatal(err)
		}
		e := event.Event{RunID: "run", Type: "run.finished"}
		if err = s.AppendEvent(t.Context(), &e); err != nil {
			t.Fatal(err)
		}
	}
	q, err := taskfeedback.Open(filepath.Join(root, "feedback"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err = q.Bind(t.Context(), taskfeedback.Item{ID: "notification", RunID: "run", Home: home, AgentID: state.DefaultAgentID, AudienceID: state.OwnerAudienceID, Plan: taskfeedback.Plan{Requester: "owner", RequestFor: "alice", NotifyTo: "alice", Mode: "notify", Target: &a}}); err != nil {
		t.Fatal(err)
	}
	return q, options{feedbackConfigDir: dir}, a
}

func TestFeedbackWorkerPreservesUnknownAndRetriesConfirmedFailure(t *testing.T) {
	for _, status := range []string{"unknown", "failed"} {
		t.Run(status, func(t *testing.T) {
			q, o, a := feedbackWorkerFixture(t, true)
			sender := &testFeedbackSender{status: status}
			if err := dispatchTaskFeedback(t.Context(), q, o, a, nil, sender); err != nil {
				t.Fatal(err)
			}
			firstText := sender.text
			sender.status = "accepted"
			if err := dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			items, _ := q.List(t.Context())
			wantCalls, wantStatus := 1, "unknown"
			if status == "failed" {
				wantCalls, wantStatus = 2, "accepted"
			}
			if sender.calls != wantCalls || items[0].Status != wantStatus || items[0].Text != firstText {
				t.Fatal(sender.calls, items)
			}
		})
	}
}

func TestFeedbackWorkerWaitsForLiveExperienceReview(t *testing.T) {
	q, o, a := feedbackWorkerFixture(t, false)
	items, _ := q.List(t.Context())
	release, err := state.Acquire(items[0].Home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s, err := state.Open(items[0].Home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CompleteRun(t.Context(), "run", "completed", "Service healthy", "observed-model", ""); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.MemoryStatus, run.ExperienceStatus = "unchanged", "review_pending"
	payload, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(items[0].Home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE runs SET memory_status=?, payload=? WHERE id=?", run.MemoryStatus, payload, run.ID); err != nil {
		t.Fatal(err)
	}
	sender := &testFeedbackSender{status: "accepted"}
	if err = dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 {
		t.Fatal("feedback was delivered while the live turn was still reviewing experience")
	}
	e := event.Event{RunID: "run", Type: "run.finished"}
	if err = s.AppendEvent(t.Context(), &e); err != nil {
		t.Fatal(err)
	}
	if err = dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatal("feedback was not delivered after the final receipt", sender.calls)
	}
}

func TestFeedbackWorkerRevalidatesRecipientAndReportsAbandonedHost(t *testing.T) {
	t.Run("removed account", func(t *testing.T) {
		q, o, a := feedbackWorkerFixture(t, true)
		writeBotJSON(t, filepath.Join(o.feedbackConfigDir, "users.json"), userCatalog{Version: 1, Users: []userDefinition{}})
		sender := &testFeedbackSender{status: "accepted"}
		if err := dispatchTaskFeedback(t.Context(), q, o, a, nil, sender); err != nil {
			t.Fatal(err)
		}
		items, _ := q.List(t.Context())
		if sender.calls != 0 || items[0].Status != "needs_recipient" {
			t.Fatal(sender.calls, items)
		}
	})
	t.Run("Host stopped", func(t *testing.T) {
		q, o, a := feedbackWorkerFixture(t, false)
		sender := &testFeedbackSender{status: "accepted"}
		if err := dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		items, _ := q.List(t.Context())
		if sender.calls != 1 || items[0].ExecutionStatus != "unknown" || items[0].SourceObservation != "host_exited_without_final_run_receipt" || !strings.Contains(sender.text, "未确认") {
			t.Fatal(sender.calls, items, sender.text)
		}
	})
	t.Run("postprocessing interrupted", func(t *testing.T) {
		q, o, a := feedbackWorkerFixture(t, false)
		items, _ := q.List(t.Context())
		release, err := state.Acquire(items[0].Home)
		if err != nil {
			t.Fatal(err)
		}
		s, err := state.Open(items[0].Home, false)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.CompleteRun(t.Context(), "run", "completed", "Service healthy", "observed-model", ""); err != nil {
			t.Fatal(err)
		}
		s.Close()
		release()
		sender := &testFeedbackSender{status: "accepted"}
		if err = dispatchTaskFeedbackAt(t.Context(), q, o, a, nil, sender, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		items, _ = q.List(t.Context())
		if sender.calls != 1 || items[0].ExecutionStatus != "completed" || items[0].SourceObservation != "host_exited_during_postprocessing" || !strings.Contains(sender.text, "Service healthy") {
			t.Fatal("postprocessing loss changed business outcome", items, sender.text)
		}
	})
}
