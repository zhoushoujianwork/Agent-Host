package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/state"
)

func TestHealthWindowStagesAndPrivacy(t *testing.T) {
	home, db := fixture(t)
	statusTables(t, db)
	o := options(home)
	secret := "PRIVATE_MESSAGE_TOKEN_778"
	for _, v := range []struct {
		id, status, reply, cause string
		at                       time.Time
	}{
		{"before", "completed", "accepted", "", o.Since.Add(-time.Nanosecond)},
		{"lower", "failed", "accepted", "unsupported message type " + secret, o.Since},
		{"cancel", "failed", "failed", "context canceled " + secret, o.Since.Add(time.Second)},
		{"ambiguous", "unknown", "unknown", "", o.Since.Add(time.Second)},
		{"upper", "failed", "failed", "routing HTTP 402", o.Until},
		{"provider", "failed", "pending", "routing HTTP 402 " + secret, o.Until.Add(-time.Nanosecond)},
	} {
		raw, _ := json.Marshal(map[string]string{"reply_status": v.reply, "error": v.cause, "text": secret, "sender_id": secret, "reply_text": secret})
		if _, e := db.Exec("INSERT INTO channel_messages VALUES(?,?,?)", v.status, v.at.Format(time.RFC3339Nano), raw); e != nil {
			t.Fatal(e)
		}
	}
	insertRun(t, db, state.Run{ID: "post", CreatedAt: o.Since, ExecutionStatus: "completed", MemoryStatus: "saved", ExperienceStatus: "failed", ExperienceError: "experience evidence exceeds 1024 bytes " + secret}, event.Event{Type: "phase.finished", Phase: "extract", Data: map[string]any{"duration_ms": 1250}}, event.Event{Type: "phase.finished", Phase: "extract"}, event.Event{Type: "phase.finished", Phase: secret, Data: map[string]any{"duration_ms": 2000}})
	insertRun(t, db, state.Run{ID: "canceled", CreatedAt: o.Since, ExecutionStatus: "canceled", MemoryStatus: "skipped", ExperienceStatus: "skipped", Error: "context canceled"})
	insertRun(t, db, state.Run{ID: "upper", CreatedAt: o.Until, ExecutionStatus: "completed", MemoryStatus: "failed", ExperienceStatus: "failed"})
	r, e := CollectStatus(context.Background(), o, true, 8765)
	if e != nil {
		t.Fatal(e)
	}
	runs := findCheck(t, r, "runs.window", "").Evidence.(map[string]any)
	if runs["denominator"] != 2 || runs["acceptance"].(map[string]int)["unverified"] != 2 {
		t.Fatal(runs)
	}
	intake := findCheck(t, r, "intake.window", "").Evidence.(map[string]any)
	if intake["denominator"] != 4 {
		t.Fatal(intake)
	}
	if intake["processing"].(map[string]int)["unknown"] != 1 || intake["reply_platform"].(map[string]int)["accepted"] != 1 {
		t.Fatal(intake)
	}
	classes := intake["error_observations"].(map[string]int)
	for _, key := range []string{"unsupported_input", "canceled_context", "dependency_http_402"} {
		if classes[key] != 1 {
			t.Fatal(classes)
		}
	}
	stages := findCheck(t, r, "runs.stages", "").Evidence.(map[string]any)
	if stages["denominator"] != 2 || stages["memory"].(map[string]int)["saved"] != 1 || stages["experience"].(map[string]int)["failed"] != 1 {
		t.Fatal(stages)
	}
	raw, _ := json.Marshal(r)
	var text bytes.Buffer
	WriteStatusText(&text, r)
	if strings.Contains(string(raw)+text.String(), secret) {
		t.Fatal("secret escaped")
	}
	latency, _ := json.Marshal(findCheck(t, r, "stages.latency", "").Evidence)
	for _, want := range []string{`"observations":2`, `"timed_observations":1`, `"total_seconds":1.25`} {
		if !bytes.Contains(latency, []byte(want)) {
			t.Fatal(string(latency))
		}
	}
	var output bytes.Buffer
	if e := RunStatus(context.Background(), []string{"--home", home, "--since", o.Since.Format(time.RFC3339Nano), "--until", o.Until.Format(time.RFC3339Nano), "--no-http", "--format", "json"}, &output, &bytes.Buffer{}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(output.String(), `"denominator": 4`) {
		t.Fatal(output.String())
	}
	o.Since = o.Until
	o.Until = o.Until.Add(time.Hour)
	o.Since = o.Since.Add(time.Nanosecond)
	r, e = CollectStatus(context.Background(), o, true, 8765)
	if e != nil {
		t.Fatal(e)
	}
	if findCheck(t, r, "intake.window", "").Evidence.(map[string]any)["denominator"] != 0 {
		t.Fatal("empty window")
	}
	if findCheck(t, r, "stages.latency", "").Status != "unknown" {
		t.Fatal("missing timing claimed known")
	}
}

func TestDependencyErrorClasses(t *testing.T) {
	for _, v := range []struct{ in, want string }{{"Jev provider routing returned HTTP 400 (Bad Request)", "dependency_http_400"}, {"routing HTTP 402", "dependency_http_402"}, {"HTTP 4000", "other"}} {
		if got := errorClass(v.in); got != v.want {
			t.Fatalf("%q: %s", v.in, got)
		}
	}
}

func TestHealthMissingEvidenceIsUnavailable(t *testing.T) {
	home, db := fixture(t)
	o := options(home)
	insertRun(t, db, state.Run{ID: "legacy", CreatedAt: o.Since, ExecutionStatus: "completed"})
	r, err := CollectStatus(context.Background(), o, true, 8765)
	if err != nil {
		t.Fatal(err)
	}
	c := findCheck(t, r, "intake.window", "")
	if c.Status != "unknown" || c.Evidence != nil {
		t.Fatal(c)
	}
	stages := findCheck(t, r, "runs.stages", "").Evidence.(map[string]any)
	for _, key := range []string{"memory", "experience"} {
		if stages[key].(map[string]int)["unknown"] != 1 {
			t.Fatal(stages)
		}
	}
	coverage := findCheck(t, r, "coverage", "").Evidence.(map[string]string)
	if coverage["central_usage_cost"] != "unavailable" || coverage["business_acceptance_recording"] != "not_implemented" || coverage["schedule_configuration"] != "unavailable" {
		t.Fatal(coverage)
	}
}
