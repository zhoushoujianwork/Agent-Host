package owneranswering

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/discovery"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func TestVerifiedUntypedContactReachesClassification(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "untyped-verified", true: "conflicting-type"}[conflict], func(t *testing.T) {
			f := setup(t)
			b := &f.c.Config.OwnerAnswering.Bindings[0]
			b.Peer = discovery.AnsweringAuthor{ID: f.s.PeerOpenDingTalkID, VerifiedContact: true}
			b.Owner = discovery.AnsweringAuthor{ID: "owner-open", VerifiedContact: true}
			if err := f.c.Activate(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(2 * time.Second)
			e := f.message("untyped-message", "Please check the service", f.s.PeerOpenDingTalkID, "")
			e.MessageType = ""
			if conflict {
				e.SenderType = "bot"
			}
			f.e = []state.DiscoveryEvidence{state.PrepareDiscoveryEvidence(e)}
			if conflict {
				err := f.c.Consume(t.Context(), f.s, discovery.Collection{Evidence: f.e, Coverage: state.DiscoveryCollection{Complete: true, To: f.now}})
				if err == nil || f.handled != 0 || f.sent != 0 {
					t.Fatal("conflicting type accepted", err)
				}
			} else {
				f.consume(t)
				if f.classified != 1 || f.handled != 1 || f.sent != 1 {
					t.Fatal("verified untyped text ignored")
				}
			}
		})
	}
}

func TestActiveQuestionMergesExpandedPollAfterInterruptedTools(t *testing.T) {
	f := setup(t)
	f.c.Handle = func(context.Context, Question) (Outcome, error) {
		f.handled++
		return Outcome{RunID: "partial-run", Reason: "partial investigation"}, errors.New("interrupted")
	}
	f.consume(t)
	r, q := f.question(t)
	original := q.ID
	originalGeneration := q.Generation
	q.Status = "executing"
	if err := f.c.save(t.Context(), &r, q); err != nil {
		t.Fatal(err)
	}
	sr, err := f.c.Store.OwnerAnsweringRecord(t.Context(), "source:"+f.s.Attribution(f.c.Config).ID)
	if err != nil {
		t.Fatal(err)
	}
	var st sourceState
	json.Unmarshal(sr.Payload, &st)
	st.Through = f.now.Add(-2 * time.Second)
	if err = f.c.save(t.Context(), &sr, st); err != nil {
		t.Fatal(err)
	}
	if err = f.c.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	f.e = append(f.e, f.message("details", "The environment is staging", "peer-observed", "person"))
	f.consume(t)
	f.consume(t)
	_, q = f.question(t)
	if q.ID != original || q.Generation <= originalGeneration || len(q.Evidence) != 2 || !q.Attempted || f.handled != 1 || f.sent != 0 || f.notified != 1 {
		t.Fatal("follow-up bypassed active interrupted work", q, f.handled, f.sent, f.notified)
	}
	if q.ID != hash([]string{f.s.Attribution(f.c.Config).ID, "m1"}) {
		t.Fatal("question not anchored to first provider message")
	}
	saved, _ := f.c.Store.OwnerAnsweringRecord(t.Context(), sr.Key)
	json.Unmarshal(saved.Payload, &st)
	if st.Active != original || !st.Through.Equal(f.now) {
		t.Fatal("intake lost active link/watermark", st)
	}
}

func TestPeerDetailsCompleteUnattemptedQuestionWithoutRenotifying(t *testing.T) {
	f := setup(t)
	f.next = "ask_owner"
	f.consume(t)
	_, first := f.question(t)
	f.now = f.now.Add(2 * time.Second)
	f.e = append(f.e, f.message("details", "Environment is staging", "peer-observed", "person"))
	f.consume(t)
	f.consume(t)
	_, updated := f.question(t)
	if updated.ID != first.ID || updated.Generation != 2 || len(updated.Evidence) != 2 || f.notified != 1 || f.handled != 0 {
		t.Fatal("details became independent task or duplicate escalation", updated)
	}
	f.next = "handle"
	_, _, err := f.c.Clarify(t.Context(), "/answer "+updated.Reference()+" Handle the staging service.")
	if err != nil || f.handled != 1 || f.sent != 1 {
		t.Fatal("complete clarified request did not run once", err)
	}
}

func TestProviderPendingReconcilesWithoutFreshSend(t *testing.T) {
	for _, status := range []string{"accepted", "failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			f.c.Send = func(context.Context, Question, string) (Receipt, error) {
				f.sent++
				return Receipt{Status: "provider_pending", TaskID: "task-one", Raw: json.RawMessage(`{"operation":"original"}`)}, nil
			}
			f.consume(t)
			_, before := f.question(t)
			if before.Status != "delivery_pending" || len(before.Peer.MessageIDs) != 0 {
				t.Fatal("pending marked replied", before)
			}
			f.c.GetStatus = func(context.Context, Question) (Receipt, error) {
				return Receipt{Status: "provider_pending", TaskID: "task-one", Raw: json.RawMessage(`{"status":"PENDING"}`)}, nil
			}
			if err := f.c.Activate(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(2 * time.Second)
			f.e = append(f.e, f.message("followup", "Additional detail", "peer-observed", "person"))
			f.consume(t)
			_, pending := f.question(t)
			if pending.ID != before.ID || f.sent != 1 || f.handled != 1 || len(pending.Evidence) != 2 || pending.Status != "delivery_pending" {
				t.Fatal("pending reply slot bypassed", pending)
			}
			f.c.GetStatus = func(_ context.Context, q Question) (Receipt, error) {
				if q.Peer.TaskID != "task-one" {
					t.Fatal("task binding lost")
				}
				return Receipt{Status: status, TaskID: "task-one", MessageIDs: []string{"sent-message"}, Raw: json.RawMessage(`{"status":"observed"}`)}, nil
			}
			f.consume(t)
			_, after := f.question(t)
			if f.sent != 1 || f.handled != 1 || after.Peer.TaskID != "task-one" || string(after.Peer.Raw) != `{"operation":"original"}` || len(after.Peer.StatusRaw) == 0 {
				t.Fatal("reconciliation replayed or lost receipts", after)
			}
			known, err := f.c.knownAI(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if known[f.s.ConversationID+"\x00sent-message"] != (status == "accepted") {
				t.Fatal("echo exemption before success")
			}
			if status == "accepted" && after.Status != "replied" || status == "failed" && after.Status != "delivery_failed" || status == "unknown" && after.Status != "waiting_owner" {
				t.Fatal("delivery status collapsed", after)
			}
			if status == "unknown" {
				_, _, err = f.c.Clarify(t.Context(), "/answer "+after.Reference()+" retry")
				if err != nil || f.sent != 1 {
					t.Fatal("unknown send replayed")
				}
			}
		})
	}
}

func TestClarificationAfterToolsUsesOnlyToolFreeDraft(t *testing.T) {
	f := setup(t)
	f.c.Handle = func(context.Context, Question) (Outcome, error) {
		f.handled++
		return Outcome{Outcome: "blocked", Text: "Observed a read-only service state.", Reason: "Need owner answer", Clarification: "Which public URL should be shared?", RunID: "investigation"}, nil
	}
	f.consume(t)
	_, q := f.question(t)
	drafts := 0
	f.c.Draft = func(_ context.Context, current Question) (Outcome, error) {
		drafts++
		if current.HandlerOutcome.Text != "Observed a read-only service state." || current.Clarification != "Share the approved staging URL." || !current.Attempted {
			t.Fatal("draft evidence/clarification missing", current)
		}
		return Outcome{Outcome: "answer", Text: "Use the approved staging URL."}, nil
	}
	_, _, err := f.c.Clarify(t.Context(), "/answer "+q.Reference()+" Share the approved staging URL.")
	if err != nil || drafts != 1 || f.handled != 1 || f.sent != 1 {
		t.Fatal("clarification replayed tools or failed drafting", err, drafts, f.handled, f.sent)
	}
}

func TestEvaluationCancellationFinishesPrivatelyWithoutReplay(t *testing.T) {
	for _, kind := range []string{"deadline", "canceled-success"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.c.Timeout = 50 * time.Millisecond
			f.c.Handle = func(ctx context.Context, q Question) (Outcome, error) {
				f.handled++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("handling has no deadline")
				}
				if kind == "deadline" {
					<-ctx.Done()
					return Outcome{RunID: "canceled", Text: "Retained partial investigation"}, ctx.Err()
				}
				cancel()
				return Outcome{Outcome: "answer", RunID: "canceled-success", Text: "Completed as cancellation arrived"}, nil
			}
			f.c.Notify = func(ctx context.Context, q Question, text string) (Receipt, error) {
				f.notified++
				if ctx.Err() != nil {
					t.Fatal("notification inherited canceled evaluation")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 21*time.Second {
					t.Fatal("notification is unbounded")
				}
				return Receipt{Status: "accepted"}, nil
			}
			err := f.c.Consume(ctx, f.s, discovery.Collection{Evidence: f.e, Coverage: state.DiscoveryCollection{Complete: true, To: f.now}})
			if err != nil {
				t.Fatal(err)
			}
			_, q := f.question(t)
			if q.Status != "waiting_owner" || !q.Attempted || !strings.Contains(strings.ToLower(q.Reason), "cancel") || q.Escalation.Status != "accepted" || q.HandlerOutcome.Text == "" || f.sent != 0 {
				t.Fatal("cancellation left executing or sent peer reply", q)
			}
			f.consume(t)
			if f.handled != 1 || f.sent != 0 || f.notified != 1 {
				t.Fatal("canceled work replayed")
			}
		})
	}
}

func TestClarificationQueueWaitHonorsTimeout(t *testing.T) {
	f := setup(t)
	f.c.Timeout = 10 * time.Millisecond
	if err := f.c.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer f.c.release()
	start := time.Now()
	handled, _, err := f.c.Clarify(t.Context(), "/answer unknown.1 details")
	if !handled || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || f.handled != 0 {
		t.Fatal("clarification wait ignored its deadline", err)
	}
}

func TestPendingQuestionCannotBeBypassedByProjectRemapping(t *testing.T) {
	f := setup(t)
	f.c.Send = func(context.Context, Question, string) (Receipt, error) {
		f.sent++
		return Receipt{Status: "provider_pending", TaskID: "pending-task"}, nil
	}
	f.consume(t)
	f.c.Config.Sources[0].ProjectID = "project-b"
	if err := f.c.Activate(t.Context()); err == nil || !strings.Contains(err.Error(), "target/project") {
		t.Fatal("project remapping bypassed unresolved delivery", err)
	}
	if f.sent != 1 || f.handled != 1 {
		t.Fatal("remapping replayed work")
	}
}

type draftRunner struct{ request harness.Request }

func (r *draftRunner) Run(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	r.request = req
	return harness.Result{Status: "completed", Model: "fixture", StructuredOutput: json.RawMessage(`{"outcome":"answer","text":"Owner supplied a grounded answer","reason":"","clarification":""}`)}, nil
}
func TestNativeDraftCannotUseToolsOrPriorNativeSession(t *testing.T) {
	r := &draftRunner{}
	draft := NativeDraft(r, "fixture", "authorized scope")
	o, err := draft(t.Context(), Question{Attempted: true, Clarification: "Owner answer"})
	if err != nil || o.Outcome != "answer" || !r.request.ToolsDisabled || !r.request.Extraction || r.request.Resume || r.request.NativeProfile != "" || len(r.request.MCPServers) != 0 || r.request.JSONSchema == "" {
		t.Fatal("draft capabilities leaked", r.request, err)
	}
}
