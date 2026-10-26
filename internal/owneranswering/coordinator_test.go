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

type fakeProvider func(context.Context, discovery.Source, discovery.Range) (discovery.Collection, error)

func (f fakeProvider) Fetch(ctx context.Context, s discovery.Source, r discovery.Range) (discovery.Collection, error) {
	return f(ctx, s, r)
}

type classifierRunner struct{ request harness.Request }

func (r *classifierRunner) Run(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	r.request = req
	return harness.Result{Status: "completed", Model: "fixture", StructuredOutput: json.RawMessage(`{"action":"handle","reason":"scope matches","clarification":""}`)}, nil
}
func TestOwnerAnsweringClassifierIsolatedToolFree(t *testing.T) {
	r := &classifierRunner{}
	classify := NativeClassifier(r, "fixture", "Owner scope")
	d, err := classify(t.Context(), Question{ID: "one"})
	first := r.request.NativeSessionID
	if err != nil || d.Action != "handle" || !r.request.ToolsDisabled || !r.request.Extraction || r.request.Resume || r.request.NativeProfile != "" || len(r.request.MCPServers) != 0 || r.request.JSONSchema == "" {
		t.Fatal("classifier capabilities leaked", r.request, err)
	}
	if _, err = classify(t.Context(), Question{ID: "two"}); err != nil || first == r.request.NativeSessionID {
		t.Fatal("classifier reused native state", err)
	}
}

type fixture struct {
	c                                   *Coordinator
	s                                   discovery.Source
	now                                 time.Time
	e                                   []state.DiscoveryEvidence
	handled, sent, notified, classified int
	next                                string
	partial                             bool
	notifyText                          string
	sendStatus                          string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := &fixture{now: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), next: "handle", sendStatus: "accepted"}
	f.s = discovery.Source{Type: "dm", ConversationID: "dm-one", PeerOpenDingTalkID: "peer-open", ProjectID: "project-a"}
	cfg := discovery.Config{Profile: "corp:owner", Sources: []discovery.Source{f.s}, OwnerAnswering: discovery.OwnerAnsweringConfig{Enabled: true, AgentID: "default", Instructions: "Handle test requests with the authorized fixture tool.", Bindings: []discovery.AnsweringBinding{{ConversationID: f.s.ConversationID, PeerOpenDingTalkID: f.s.PeerOpenDingTalkID, Owner: discovery.AnsweringAuthor{ID: "owner-observed", Type: "person"}, Peer: discovery.AnsweringAuthor{ID: "peer-observed", Type: "person"}}}}}
	f.c = &Coordinator{Store: store, Config: cfg, Now: func() time.Time { return f.now }}
	f.c.Provider = fakeProvider(func(_ context.Context, _ discovery.Source, r discovery.Range) (discovery.Collection, error) {
		var evidence []state.DiscoveryEvidence
		for _, e := range f.e {
			if !e.CreatedAt.Before(r.From) && e.CreatedAt.Before(r.To) {
				evidence = append(evidence, e)
			}
		}
		return discovery.Collection{Evidence: evidence, Coverage: state.DiscoveryCollection{From: r.From, To: r.To, Complete: !f.partial, Partial: f.partial}}, nil
	})
	f.c.Classify = func(context.Context, Question) (Decision, error) {
		f.classified++
		return Decision{Action: f.next, Reason: "Unsupported or missing details", Clarification: "Which environment?"}, nil
	}
	f.c.Handle = func(_ context.Context, q Question) (Outcome, error) {
		f.handled++
		source, readErr := store.OwnerAnsweringRecord(t.Context(), "source:"+f.s.Attribution(cfg).ID)
		var intake sourceState
		json.Unmarshal(source.Payload, &intake)
		if readErr != nil || intake.Active != q.ID || !intake.Through.Equal(f.now) {
			t.Fatal("tools started before atomic intake watermark/active link", readErr, intake)
		}
		r, err := store.OwnerAnsweringRecord(t.Context(), "question:"+q.ID)
		if err != nil || !strings.Contains(string(r.Payload), `"status":"executing"`) || !strings.Contains(string(r.Payload), `"attempted":true`) {
			t.Fatal("tools started before durable claim", err)
		}
		return Outcome{Outcome: "answer", Text: "Verified result", RunID: "fixture-run"}, nil
	}
	f.c.Send = func(_ context.Context, q Question, id string) (Receipt, error) {
		f.sent++
		if id != "oa-"+q.ID || !strings.Contains(q.Answer, "AI assistant replying") {
			t.Fatal("send binding/marker missing")
		}
		return Receipt{Status: f.sendStatus, MessageIDs: []string{"ai-message"}}, nil
	}
	f.c.Notify = func(_ context.Context, q Question, text string) (Receipt, error) {
		f.notified++
		f.notifyText = text
		return Receipt{Status: "accepted"}, nil
	}
	if err = f.c.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	f.e = []state.DiscoveryEvidence{f.message("m1", "Please check the configured service", "peer-observed", "person")}
	return f
}

func TestOwnerAnsweringChangedBurstCannotReplayAssignedMessages(t *testing.T) {
	f := setup(t)
	f.consume(t)
	key := "source:" + f.s.Attribution(f.c.Config).ID
	r, err := f.c.Store.OwnerAnsweringRecord(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a cursor-save interruption/older checkpoint without deleting the
	// durable completed question and its message membership.
	if err = f.c.save(t.Context(), &r, sourceState{Policy: f.c.policy(), Through: f.now.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	f.e = append(f.e, f.message("m2", "Check a separate complete request", "peer-observed", "person"))
	f.c.Handle = func(_ context.Context, q Question) (Outcome, error) {
		f.handled++
		if len(q.Evidence) != 1 || q.Evidence[0].MessageID != "m2" {
			t.Fatal("original request repackaged for replay", q.Evidence)
		}
		return Outcome{Outcome: "answer", Text: "Second distinct result"}, nil
	}
	f.consume(t)
	if f.handled != 2 || f.sent != 2 {
		t.Fatal("new request handling lost", f.handled, f.sent)
	}
}

func TestOwnerAnsweringOnlyAcceptedAIReceiptsExemptEchoes(t *testing.T) {
	for _, status := range []string{"accepted", "provider_pending", "unknown", "failed"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			f.sendStatus = status
			f.consume(t)
			known, err := f.c.knownAI(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := status == "accepted"
			if known[f.s.ConversationID+"\x00ai-message"] != want {
				t.Fatal("uncertain/failed receipt granted AI echo exemption")
			}
		})
	}
}
func (f *fixture) message(id, text, who, kind string) state.DiscoveryEvidence {
	a := f.s.Attribution(f.c.Config)
	return state.PrepareDiscoveryEvidence(state.DiscoveryEvidence{SourceID: a.ID, Profile: a.Profile, PrincipalID: a.PrincipalID, TenantID: a.TenantID, ConversationID: f.s.ConversationID, ProjectID: f.s.ProjectID, MessageID: id, AuthorID: who, Author: json.RawMessage(`"Fixture peer"`), SenderType: kind, MessageType: "text", Text: text, CreatedAt: f.now.Add(-time.Second), Availability: "available"})
}
func (f *fixture) consume(t *testing.T) {
	t.Helper()
	err := f.c.Consume(t.Context(), f.s, discovery.Collection{Evidence: f.e, Coverage: state.DiscoveryCollection{From: f.now.Add(-2 * time.Second), To: f.now, Complete: true}})
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) question(t *testing.T) (state.OwnerAnsweringRecord, Question) {
	t.Helper()
	records, err := f.c.Store.OwnerAnsweringRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if strings.HasPrefix(r.Key, "question:") {
			var q Question
			if err = json.Unmarshal(r.Payload, &q); err != nil {
				t.Fatal(err)
			}
			return r, q
		}
	}
	t.Fatal("no question")
	return state.OwnerAnsweringRecord{}, Question{}
}

func TestSelectedDMHandlingAndDuplicateIntake(t *testing.T) {
	f := setup(t)
	f.consume(t)
	f.consume(t)
	_, q := f.question(t)
	if f.handled != 1 || f.sent != 1 || f.notified != 0 || q.Status != "replied" || !q.Attempted || q.RunID != "fixture-run" {
		t.Fatalf("unexpected path: %+v handles=%d sends=%d alerts=%d", q, f.handled, f.sent, f.notified)
	}
}
func TestNonrequestAndUnsupported(t *testing.T) {
	for _, action := range []string{"ignore", "ask_owner"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			f.next = action
			f.consume(t)
			f.consume(t)
			_, q := f.question(t)
			if f.handled != 0 || f.sent != 0 {
				t.Fatal("blocked/nonrequest executed or replied")
			}
			if action == "ignore" && f.notified != 0 {
				t.Fatal("greeting escalated")
			}
			if action == "ask_owner" && (f.notified != 1 || !strings.Contains(f.notifyText, "Which environment?") || !strings.Contains(f.notifyText, "m1") || !strings.Contains(f.notifyText, q.Reference())) {
				t.Fatal("missing or repeated escalation", f.notifyText)
			}
		})
	}
}
func TestHistoricalActivationAndIdentityBindings(t *testing.T) {
	for _, kind := range []string{"historical", "owner", "unknown", "missing-type", "wrong-type"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			switch kind {
			case "historical":
				f.e[0].CreatedAt = f.now.Add(-time.Hour)
			case "owner":
				f.e[0].AuthorID = "owner-observed"
			case "unknown":
				f.e[0].AuthorID = "automation"
			case "missing-type":
				f.e[0].SenderType = ""
			case "wrong-type":
				f.e[0].SenderType = "robot"
			}
			f.e[0] = state.PrepareDiscoveryEvidence(f.e[0])
			if kind == "historical" || kind == "owner" {
				f.consume(t)
			} else {
				err := f.c.Consume(t.Context(), f.s, discovery.Collection{Evidence: f.e, Coverage: state.DiscoveryCollection{Complete: true, To: f.now}})
				if err == nil || !strings.Contains(err.Error(), "blocked_identity") {
					t.Fatal("identity failure not surfaced", err)
				}
			}
			if f.handled != 0 || f.sent != 0 || f.notified != 0 {
				t.Fatal("ineligible evidence produced effects")
			}
			if kind != "historical" && kind != "owner" {
				r, err := f.c.Store.OwnerAnsweringRecord(t.Context(), "source:"+f.s.Attribution(f.c.Config).ID)
				if err != nil || !strings.Contains(string(r.Payload), "blocked_identity") {
					t.Fatal("identity blocker lost", err)
				}
			}
		})
	}
}
func TestFreshHistoryCancelsPeerSend(t *testing.T) {
	for _, change := range []string{"owner", "new-peer", "partial", "unknown", "missing", "policy"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			f.c.Handle = func(_ context.Context, q Question) (Outcome, error) {
				f.handled++
				switch change {
				case "owner":
					f.e = append(f.e, f.message("owner-reply", "I will handle it", "owner-observed", "person"))
				case "new-peer":
					f.e = append(f.e, f.message("new-detail", "Different environment", "peer-observed", "person"))
				case "unknown":
					f.e = append(f.e, f.message("robot", "status", "robot", "bot"))
				case "partial":
					f.partial = true
				case "missing":
					f.e = nil
				case "policy":
					f.c.Validate = func(context.Context) error { return errors.New("policy disabled") }
				}
				return Outcome{Outcome: "answer", Text: "stale result"}, nil
			}
			err := f.c.Consume(t.Context(), f.s, discovery.Collection{Evidence: f.e, Coverage: state.DiscoveryCollection{Complete: true, To: f.now}})
			if change != "policy" && err != nil {
				t.Fatal(err)
			}
			if f.sent != 0 {
				t.Fatal("stale answer sent")
			}
			_, q := f.question(t)
			if !q.Attempted {
				t.Fatal("tool attempt lost")
			}
			if change == "owner" && q.Status != "taken_over" {
				t.Fatal("takeover lost", q.Status)
			}
		})
	}
}
func TestClarificationCorrelatesRevisionWithoutBlindForward(t *testing.T) {
	f := setup(t)
	f.next = "ask_owner"
	f.consume(t)
	_, q := f.question(t)
	handled, _, err := f.c.Clarify(t.Context(), "ordinary bot message")
	if handled || err != nil {
		t.Fatal("ordinary message intercepted")
	}
	for _, text := range []string{"/answer missing.1 hello", "/answer " + q.ID + ".9 hello", "/answer " + q.ID + " hello"} {
		_, reply, err := f.c.Clarify(t.Context(), text)
		if err != nil || reply == "" || f.sent != 0 {
			t.Fatal("ambiguous reference accepted")
		}
	}
	f.next = "handle"
	f.c.Handle = func(_ context.Context, current Question) (Outcome, error) {
		f.handled++
		if current.Generation != 2 || current.Clarification != "test environment" {
			t.Fatal("clarification not revision bound", current)
		}
		return Outcome{Outcome: "answer", Text: "Reevaluated result"}, nil
	}
	_, _, err = f.c.Clarify(t.Context(), "/answer "+q.Reference()+" test environment")
	if err != nil || f.sent != 1 || f.handled != 1 {
		t.Fatal("clarification did not evaluate", err)
	}
	_, _, err = f.c.Clarify(t.Context(), "/answer "+q.Reference()+" old")
	if err != nil || f.sent != 1 {
		t.Fatal("stale owner response replayed", err)
	}
}
func TestInterruptedExecutionAndUnknownDeliveryNeverReplay(t *testing.T) {
	for _, stage := range []string{"executing", "ready", "peer-reserved", "escalation-reserved", "unknown-delivery", "failed-handler"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t)
			if stage == "unknown-delivery" {
				f.sendStatus = "unknown"
				f.consume(t)
			} else if stage == "failed-handler" {
				f.c.Handle = func(context.Context, Question) (Outcome, error) {
					f.handled++
					return Outcome{RunID: "partial-run", Reason: "partial effects"}, errors.New("interrupted")
				}
				f.consume(t)
			} else {
				f.next = "ask_owner"
				f.consume(t)
				r, q := f.question(t)
				switch stage {
				case "executing", "ready":
					q.Status = stage
					q.Attempted = true
					q.Escalation = Receipt{}
				case "peer-reserved":
					q.Status = "sending"
					q.Peer.Status = "reserved"
					q.Escalation = Receipt{}
				case "escalation-reserved":
					q.Escalation.Status = "reserved"
				}
				if err := f.c.save(t.Context(), &r, q); err != nil {
					t.Fatal(err)
				}
			}
			beforeHandles, beforeSends, beforeAlerts := f.handled, f.sent, f.notified
			if err := f.c.Activate(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.consume(t)
			f.consume(t)
			_, q := f.question(t)
			if f.handled != beforeHandles || f.sent != beforeSends {
				t.Fatal("reserved effect replayed")
			}
			if stage == "escalation-reserved" && f.notified != beforeAlerts {
				t.Fatal("uncertain alert replayed")
			}
			if q.Attempted || q.Peer.Status != "" {
				_, _, err := f.c.Clarify(t.Context(), "/answer "+q.Reference()+" retry")
				if err != nil || f.handled != beforeHandles || f.sent != beforeSends {
					t.Fatal("owner clarification replayed effects")
				}
			}
		})
	}
}
func TestPartialIntakeRejectedAndNotificationFailureRetained(t *testing.T) {
	f := setup(t)
	if err := f.c.Consume(t.Context(), f.s, discovery.Collection{Coverage: state.DiscoveryCollection{Partial: true}}); err == nil {
		t.Fatal("partial intake accepted")
	}
	f.next = "ask_owner"
	f.c.Notify = func(context.Context, Question, string) (Receipt, error) {
		f.notified++
		return Receipt{Status: "unknown"}, errors.New("transport interrupted")
	}
	f.consume(t)
	f.consume(t)
	_, q := f.question(t)
	if f.notified != 1 || f.sent != 0 || q.Escalation.Status != "unknown" {
		t.Fatal("notification failure lost or retried")
	}
}
