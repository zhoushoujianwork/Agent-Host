package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/state"
)

func outcomeBlock(body string) string {
	return answeringOutcomeStart + "\n" + body + "\n" + answeringOutcomeEnd
}

const fixtureOutcome = `{"outcome":"answer","text":"Grounded answer","reason":"","clarification":""}`

func TestOwnerAnsweringNarratedACPOutcome(t *testing.T) {
	full := "I will check the service.\nObserved read-only fixture result.\n" + outcomeBlock(fixtureOutcome) + "\n"
	o, err := parseAnsweringOutcome(full)
	if err != nil || o.Text != "Grounded answer" {
		t.Fatal("narrated ACP output rejected", o, err)
	}
	for _, bad := range []string{fixtureOutcome, full + "Afterword", full + outcomeBlock(fixtureOutcome), "prefix " + outcomeBlock(fixtureOutcome), outcomeBlock(`{"outcome":"answer","text":"a","text":"b","reason":"","clarification":""}`), outcomeBlock(`{"outcome":"answer","text":"answer"}`), outcomeBlock(`{"outcome":"answer","text":"answer","reason":"","clarification":"","extra":true}`), outcomeBlock(`{"outcome":"answer","text":null,"reason":"","clarification":""}`), outcomeBlock(`{"outcome":"invalid","text":"a","reason":"","clarification":""}`), answeringOutcomeStart + fixtureOutcome + answeringOutcomeEnd} {
		if _, err := parseAnsweringOutcome(bad); err == nil {
			t.Fatal("malformed/ambiguous outcome accepted", bad)
		}
	}
}
func TestOwnerAnsweringExecutionAndLearningOutcomesSeparate(t *testing.T) {
	run := state.Run{ID: "run", ExecutionStatus: "completed", Output: "Private progress\n" + outcomeBlock(fixtureOutcome), MemoryError: "learning unavailable"}
	o, err := answeringRunOutcome(run, errors.New("post-execution learning failed"))
	if err != nil || o.Outcome != "answer" || o.RunID != "run" || !strings.Contains(o.Warning, "learning") {
		t.Fatal("learning failure replaced actual success", o, err)
	}
	if !strings.Contains(run.Output, "Private progress") {
		t.Fatal("full evidence changed")
	}
	run.ExecutionStatus = "failed"
	run.Error = "native failed"
	if _, err = answeringRunOutcome(run, nil); err == nil {
		t.Fatal("execution failure accepted")
	}
	run.ExecutionStatus = "completed"
	run.Error = "native still failed"
	if _, err = answeringRunOutcome(run, nil); err == nil {
		t.Fatal("execution error accepted")
	}
	run.Error = ""
	run.Output = "No terminal result"
	if _, err = answeringRunOutcome(run, errors.New("learning error")); err == nil {
		t.Fatal("missing outcome hidden by learning error")
	}
}
func TestOwnerAnsweringRealWorkspaceReceiptAndStatusShapes(t *testing.T) {
	var value any
	json.Unmarshal([]byte(`{"operation_id":"oa-fixture","status":"completed","result":{"ok":true,"outcome":"success","data":{"result":{"openTaskId":"fixture-task"}}}}`), &value)
	r := answeringWorkspaceReceipt(value, nil, "fixture-dm")
	if r.Status != "provider_pending" || r.TaskID != "fixture-task" || len(r.MessageIDs) != 0 {
		t.Fatal("task acceptance mistaken for delivery", r)
	}
	for _, tc := range []struct{ status, want string }{{"SUCCESS", "accepted"}, {"PENDING", "provider_pending"}, {"FAILED", "failed"}, {"UNRECOGNIZED", "unknown"}} {
		body := json.RawMessage(`{"ok":true,"outcome":"success","data":{"result":{"openTaskId":"fixture-task","status":"` + tc.status + `","openMessageId":"fixture-message","openConversationId":"fixture-dm"}}}`)
		r = answeringWorkspaceStatus(body, "fixture-task", "fixture-dm", nil)
		if r.Status != tc.want || r.TaskID != "fixture-task" {
			t.Fatal("status result lost", r)
		}
		if tc.status == "SUCCESS" && len(r.MessageIDs) != 1 {
			t.Fatal("delivered message ID lost")
		}
		if tc.status != "SUCCESS" && len(r.MessageIDs) != 0 {
			t.Fatal("non-success granted echo exemption")
		}
		if wrong := answeringWorkspaceStatus(body, "other-task", "fixture-dm", nil); wrong.Status != "unknown" {
			t.Fatal("wrong task accepted")
		}
		if wrong := answeringWorkspaceStatus(body, "fixture-task", "other-dm", nil); wrong.Status != "unknown" {
			t.Fatal("wrong conversation accepted")
		}
	}
	bad := json.RawMessage(`{"ok":false,"outcome":"failure","data":{"result":{"status":"SUCCESS","openMessageId":"message"}}}`)
	if r := answeringWorkspaceStatus(bad, "fixture-task", "fixture-dm", nil); r.Status != "unknown" {
		t.Fatal("contradictory failure accepted", r)
	}
}
