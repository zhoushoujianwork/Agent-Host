package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/taskfeedback"
)

func TestNotificationTriageReadOnlyAndSanitized(t *testing.T) {
	q, o, _ := feedbackWorkerFixture(t, true)
	items, err := q.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Finish(t.Context(), items[0], "needs_recipient", "", "private error"); err != nil {
		t.Fatal(err)
	}
	before, _ := q.List(t.Context())
	var out bytes.Buffer
	if err = runNotifications(t.Context(), []string{"triage", "--config-dir", o.feedbackConfigDir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{items[0].Home, "ou_alice", "cli_app", "private error", "Service healthy", `"alice"`} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("triage exposed %q", secret)
		}
	}
	var report struct {
		Counts map[string]int
		Items  []feedbackTriageRow
	}
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Counts["unresolved"] != 1 || report.Counts["actionable"] != 1 || report.Items[0].AgeBasis != "run_created_at" {
		t.Fatal(out.String())
	}
	after, _ := q.List(t.Context())
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		t.Fatal("triage mutated queue")
	}
}

func TestNotificationTriageRequiresEvidenceAndDoesNotInferHistory(t *testing.T) {
	now := time.Now().UTC()
	items := []taskfeedback.Item{{ID: "old", Status: "needs_recipient", UpdatedAt: now.Add(-40 * 24 * time.Hour)}, {ID: "waiting", Status: "awaiting_result"}}
	var out bytes.Buffer
	if err := writeFeedbackTriage(t.Context(), items, "", &out, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"needs_review":2`) || !strings.Contains(out.String(), `"30d_plus"`) {
		t.Fatal(out.String())
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(`{"old":{"class":"test","evidence":"private fixture provenance"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := writeFeedbackTriage(t.Context(), items, path, &out, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"historical_test":1`) || strings.Contains(out.String(), "private fixture provenance") {
		t.Fatal(out.String())
	}
	if err := os.WriteFile(path, []byte(`{"old":{"class":"test"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeFeedbackTriage(t.Context(), items, path, io.Discard, now); err == nil {
		t.Fatal("accepted unsupported review")
	}
}

func TestNotificationTriageAwaitingResult(t *testing.T) {
	_, o, _ := feedbackWorkerFixture(t, false)
	var out bytes.Buffer
	if err := runNotifications(t.Context(), []string{"triage", "--config-dir", o.feedbackConfigDir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"awaiting_result":1`) || !strings.Contains(out.String(), `"actionable":0`) {
		t.Fatal(out.String())
	}
}

func TestNotificationTriageFailedAttributionDoesNotAssertOwner(t *testing.T) {
	var out bytes.Buffer
	item := taskfeedback.Item{ID: "unresolved", Status: "needs_recipient", Plan: taskfeedback.Plan{RequestFor: "owner", Reason: "ownership_classification_failed"}}
	if err := writeFeedbackTriage(t.Context(), []taskfeedback.Item{item}, "", &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"owner":"unresolved"`) || !strings.Contains(out.String(), `"attribution":"ownership_classification_failed"`) {
		t.Fatal(out.String())
	}
}
