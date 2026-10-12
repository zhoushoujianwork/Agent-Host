package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/state"
)

const feedbackSecret = "PRIVATE-FEEDBACK-FIXTURE"

func feedbackFixture(t *testing.T, agent, audience, model string) (string, state.Run) {
	t.Helper()
	home := t.TempDir()
	release, err := state.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	owner, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	store := owner.WithPartition(agent, audience)
	session := state.Session{ID: feedbackSecret + "-session", Scope: "/private/" + feedbackSecret, NativeSessionID: feedbackSecret + "-native"}
	if err := store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: feedbackSecret + "-run", SessionID: session.ID, Scope: session.Scope, NativeSessionID: session.NativeSessionID, Input: feedbackSecret + " input"}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(t.Context(), run.ID, "completed", feedbackSecret+" output", model, feedbackSecret+" raw error"); err != nil {
		t.Fatal(err)
	}
	run, err = store.Run(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.MemoryStatus, run.ExperienceStatus = "saved", "review_pending"
	run.MemoryError, run.ExperienceError = feedbackSecret, feedbackSecret
	run.RequestedModel, run.RuntimeFingerprint = feedbackSecret, feedbackSecret
	run.Context = []state.Memory{
		{ID: feedbackSecret + "-memory", Kind: "preference", Status: "active", SourceType: "user_statement", Text: feedbackSecret, SenderID: feedbackSecret},
		{ID: feedbackSecret + "-procedure", Kind: "procedure", Status: "active", SourceType: "reviewed_experience", Text: feedbackSecret, Conditions: feedbackSecret, Steps: []string{feedbackSecret}},
		{ID: feedbackSecret + "-future", Kind: feedbackSecret, Status: feedbackSecret, SourceType: feedbackSecret, Text: feedbackSecret},
	}
	run.Decision = json.RawMessage(`{"endpoint":"https://private.example/PRIVATE-FEEDBACK-FIXTURE","role":"PRIVATE-FEEDBACK-FIXTURE"}`)
	message := state.ChannelMessage{ID: feedbackSecret + "-receipt", AgentID: run.AgentID, AudienceID: run.AudienceID, Channel: "dingtalk", AccountID: feedbackSecret + "-account", MessageID: feedbackSecret + "-platform", SenderID: feedbackSecret + "-member", Profile: "fixture", Scope: run.Scope, Text: run.Input, Status: "completed", ReplyStatus: "accepted", ReplyText: feedbackSecret, RunID: run.ID, SessionID: run.SessionID}
	run.Source = &state.RunSource{ChannelMessageID: message.ID, AccountID: message.AccountID, MessageID: message.MessageID, SenderID: message.SenderID, Channel: message.Channel}
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	messagePayload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channel_messages(id,channel,account_id,message_id,profile,status,received_at,reply_url,payload) VALUES(?,?,?,?,?,?,?,?,?)", message.ID, message.Channel, message.AccountID, message.MessageID, message.Profile, message.Status, time.Now().UTC().Format(time.RFC3339Nano), feedbackSecret, messagePayload); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var forged map[string]any
	if err := json.Unmarshal(payload, &forged); err != nil {
		t.Fatal(err)
	}
	forged["future_private_field"] = feedbackSecret
	payload, err = json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE runs SET payload=? WHERE id=?", payload, run.ID); err != nil {
		t.Fatal(err)
	}
	for _, observation := range []event.Event{
		{Type: "tool.activity", Phase: "execute", Data: map[string]any{"tool": map[string]any{"id": feedbackSecret, "name": feedbackSecret, "stage": "call", "input": feedbackSecret}}},
		{Type: "tool.activity", Phase: "execute", Data: map[string]any{"tool": map[string]any{"id": feedbackSecret, "stage": "result", "is_error": true, "result": feedbackSecret}, "raw": feedbackSecret}},
		{Type: "phase.finished", Phase: "execute", Data: map[string]any{"duration_ms": 1234, "progress_error": feedbackSecret, "future_private_field": feedbackSecret}},
		{Type: "phase.finished", Phase: "/private/" + feedbackSecret, Data: map[string]any{"duration_ms": 999}},
	} {
		observation.RunID = run.ID
		if err := store.AppendEvent(t.Context(), &observation); err != nil {
			t.Fatal(err)
		}
	}
	return home, run
}

func feedbackFile(t *testing.T, path string) (feedbackReport, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report feedbackReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report, raw
}

func feedbackDatabaseBytes(t *testing.T, home string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{"state.db", "state.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(home, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		files[name] = raw
	}
	return files
}

func TestFeedbackReportReadsLiveStateWithoutSensitiveFields(t *testing.T) {
	home, run := feedbackFixture(t, state.DefaultAgentID, state.OwnerAudienceID, "claude-sonnet-4-5-20250929")
	before := feedbackDatabaseBytes(t, home)
	output := filepath.Join(t.TempDir(), "feedback.json")
	var stdout bytes.Buffer
	if err := runFeedback(t.Context(), []string{"report", "--home", home, "--run", run.ID, "--output", output}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	report, raw := feedbackFile(t, output)
	for _, private := range []string{feedbackSecret, home, "https://private.example", "future_private_field", "source_run_id", "native_session_id"} {
		if bytes.Contains(raw, []byte(private)) || strings.Contains(stdout.String(), private) {
			t.Fatalf("private metadata escaped the projection: %q", private)
		}
	}
	if report.SchemaVersion != 1 || len(report.ReportID) != 32 || report.Database.Status != "available" || report.Database.SchemaVersion == nil || *report.Database.SchemaVersion != state.SchemaVersion || report.AffectedExecutionBuild != "unknown" {
		t.Fatalf("missing independent report identity or schema metadata: %+v", report)
	}
	summary := report.Run
	if summary == nil || summary.ExecutionStatus != "completed" || summary.MemoryStatus != "saved" || summary.ExperienceStatus != "review_pending" || summary.AcceptanceStatus != "unverified" || summary.ReplyStatus != "accepted" || summary.Model != run.Model {
		t.Fatalf("recorded outcomes were changed: %+v", summary)
	}
	if summary.Tools != (feedbackTools{Calls: 1, Results: 1, Errors: 1}) || summary.Memories.InjectedCount != 3 || summary.Memories.ByKind["unknown"] != 1 || summary.Memories.ByStatus["active"] != 2 || summary.Memories.BySourceType["reviewed_experience"] != 1 || len(summary.PhaseDurationMS) != 1 || summary.PhaseDurationMS["execute"] != 1234 || !summary.ProgressWarning {
		t.Fatalf("allowed structural observations were lost: %+v", summary)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions: %v %v", info, err)
	}
	for name, after := range feedbackDatabaseBytes(t, home) {
		if !bytes.Equal(before[name], after) {
			t.Fatalf("read-only feedback changed %s", name)
		}
	}
}

func TestFeedbackReportMissingStateRunAndExistingOutput(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent-home")
	output := filepath.Join(t.TempDir(), "installation.json")
	args := []string{"report", "--home", home, "--output", output}
	if err := runFeedback(t.Context(), args, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	report, original := feedbackFile(t, output)
	if report.Database.Status != "unavailable" || report.Database.SchemaVersion != nil || report.Run != nil {
		t.Fatalf("missing state was invented: %+v", report)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report created a missing state home: %v", err)
	}
	if err := runFeedback(t.Context(), args, io.Discard, io.Discard); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing report was not protected: %v", err)
	}
	_, unchanged := feedbackFile(t, output)
	if !bytes.Equal(original, unchanged) {
		t.Fatal("existing report was overwritten")
	}
	second := filepath.Join(t.TempDir(), "installation.json")
	if err := runFeedback(t.Context(), []string{"report", "--home", home, "--output", second}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	next, _ := feedbackFile(t, second)
	if next.ReportID == report.ReportID {
		t.Fatal("report identity was reused")
	}
	liveHome, _ := feedbackFixture(t, state.DefaultAgentID, state.OwnerAudienceID, "claude-sonnet-4-5-20250929")
	for _, stateHome := range []string{home, liveHome} {
		missing := filepath.Join(t.TempDir(), "missing.json")
		if err := runFeedback(t.Context(), []string{"report", "--home", stateHome, "--run", "missing-run", "--output", missing}, io.Discard, io.Discard); err == nil {
			t.Fatal("missing run was reported as a real observation")
		}
		if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing run created a report: %v", err)
		}
	}
}

func TestFeedbackReportPreservesPartitionAndUnknownModel(t *testing.T) {
	home, run := feedbackFixture(t, "fixture-agent", "fixture-group", "/private/"+feedbackSecret)
	output := filepath.Join(t.TempDir(), "group.json")
	args := []string{"report", "--home", home, "--run", run.ID, "--output", output}
	if err := runFeedback(t.Context(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("default partition read another audience's run")
	}
	args = append(args, "--agent", "fixture-agent", "--audience", "fixture-group")
	if err := runFeedback(t.Context(), args, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	report, raw := feedbackFile(t, output)
	if report.Run == nil || report.Run.Model != "unknown" || report.Run.ReplyStatus != "accepted" || bytes.Contains(raw, []byte(feedbackSecret)) || bytes.Contains(raw, []byte("fixture-group")) || bytes.Contains(raw, []byte("fixture-agent")) {
		t.Fatalf("partition identity or unapproved model escaped: %+v", report.Run)
	}
}
