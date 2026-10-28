package stats

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/state"
)

var fixtureNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE runs(id TEXT PRIMARY KEY,created_at TEXT,execution_status TEXT,memory_status TEXT,payload BLOB); CREATE TABLE events(run_id TEXT,seq INTEGER,payload BLOB,PRIMARY KEY(run_id,seq)); PRAGMA user_version=17;`)
	if err != nil {
		t.Fatal(err)
	}
	return home, db
}
func insertRun(t *testing.T, db *sql.DB, r state.Run, events ...event.Event) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO runs VALUES(?,?,?,?,?)", r.ID, r.CreatedAt.Format(time.RFC3339Nano), r.ExecutionStatus, r.MemoryStatus, raw); err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		e.RunID = r.ID
		raw, _ = json.Marshal(e)
		if _, err = db.Exec("INSERT INTO events VALUES(?,?,?)", r.ID, i, raw); err != nil {
			t.Fatal(err)
		}
	}
}
func options(home string) Options {
	return Options{Homes: []string{home}, Since: fixtureNow.Add(-24 * time.Hour), Until: fixtureNow, Now: fixtureNow, StaleAfter: 2 * time.Hour, Top: 10}
}
func activity(name string) event.Event {
	return event.Event{Type: "tool.activity", Data: map[string]any{"raw": map[string]any{"_meta": map[string]any{"claudeCode": map[string]any{"toolName": name}}}}}
}
func TestFixtureGoldenPrivacyAndReadOnly(t *testing.T) {
	home, db := fixture(t)
	secrets := []string{"PRIVATE_INPUT_398", "PRIVATE_OUTPUT_992", "PRIVATE_SENDER_113", "PRIVATE_ROOM_228", "PRIVATE_CONVERSATION_331", "PRIVATE_PATH_122"}
	statuses := []string{"completed", "canceled", "unknown", "failed", "running", "running"}
	for i, status := range statuses {
		created := fixtureNow.Add(-time.Duration(6-i) * time.Hour)
		if i == 4 {
			created = created.Add(-time.Second)
		}
		r := state.Run{ID: string(rune('a' + i)), CreatedAt: created, ExecutionStatus: status, MemoryStatus: []string{"saved", "unchanged", "skipped", "failed", "pending", "pending"}[i], AcceptanceStatus: "unverified", AgentID: "ops", Model: "model-one", Input: secrets[0], Output: secrets[1], Workspace: secrets[5], Source: &state.RunSource{Channel: "cli", SenderName: secrets[2], SenderID: secrets[3], ConversationID: secrets[4]}}
		if i < 3 {
			end := created.Add(time.Duration(i+1) * 10 * time.Second)
			r.CompletedAt = &end
		}
		var events []event.Event
		if i == 0 {
			events = []event.Event{activity("Read"), activity("Read"), activity(""), {Type: "agent.result", Component: "harness.claude", Data: map[string]any{"raw": map[string]any{"type": "result", "uuid": "result-a", "total_cost_usd": 0.25, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}}}}
		}
		if i == 1 {
			r.Error = "caller exited or became unverifiable: context canceled " + secrets[2]
			events = []event.Event{{Type: "agent.result", Component: "harness.acp"}}
		}
		if i == 3 {
			r.Error = "malformed event " + secrets[0]
		}
		insertRun(t, db, r, events...)
	}
	insertRun(t, db, state.Run{ID: "previous", CreatedAt: fixtureNow.Add(-30 * time.Hour), ExecutionStatus: "completed", MemoryStatus: "skipped", AcceptanceStatus: "accepted"})
	insertRun(t, db, state.Run{ID: "until-excluded", CreatedAt: fixtureNow, ExecutionStatus: "failed"})
	if _, err := db.Exec("INSERT INTO events VALUES(?,?,?)", "until-excluded", 0, []byte("invalid private event")); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{"state.db", "state.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = raw
	}
	o := options(home)
	o.Compare = true
	r, err := Collect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(r, "", "  ")
	raw = append(raw, '\n')
	golden := filepath.Join("testdata", "report.json")
	if os.Getenv("UPDATE_STATS_GOLDEN") == "1" {
		if err := os.WriteFile(golden, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("golden mismatch:\n%s", raw)
	}
	var text bytes.Buffer
	if err = WriteText(&text, r); err != nil {
		t.Fatal(err)
	}
	for _, secret := range append(secrets, home) {
		if bytes.Contains(raw, []byte(secret)) || strings.Contains(text.String(), secret) {
			t.Fatalf("privacy leak: %s", secret)
		}
	}
	for name, b := range before {
		after, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || !bytes.Equal(b, after) {
			t.Fatalf("modified %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "host.lock")); !os.IsNotExist(err) {
		t.Fatal("unexpected host lock")
	}
	// Absolute homes produce identical data from an unrelated working directory.
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	again, err := Collect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(again, "", "  ")
	if !bytes.Equal(raw, append(got, '\n')) {
		t.Fatal("cwd changed results")
	}
}
func TestSourcesFiltersAndBounds(t *testing.T) {
	home, db := fixture(t)
	o := options(home)
	insertRun(t, db, state.Run{ID: "lower", CreatedAt: o.Since, AgentID: "ops", ExecutionStatus: "completed", Source: &state.RunSource{Channel: "feishu"}})
	insertRun(t, db, state.Run{ID: "outside", CreatedAt: o.Since.Add(-time.Nanosecond), ExecutionStatus: "failed"})
	bad, badDB := fixture(t)
	if _, err := badDB.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	o.Homes = append(o.Homes, bad, filepath.Join(t.TempDir(), "missing"))
	o.Agent = "ops"
	o.Channel = "feishu"
	r, err := Collect(context.Background(), o)
	if err != nil || r.Totals["runs"] != 1 || r.Sources[1].Status != "unreadable" || r.Sources[2].Status != "unreadable" || r.Sources[1].Runs != nil {
		t.Fatalf("%+v %v", r, err)
	}
	o.Homes = o.Homes[1:]
	r, err = Collect(context.Background(), o)
	if err == nil || len(r.Sources) != 2 {
		t.Fatal("expected all unreadable error")
	}
	o = options(home)
	o.Agent = "none"
	r, err = Collect(context.Background(), o)
	if err != nil || r.Totals["runs"] != 0 || r.Duration.Median != nil || r.Usage.Cost != nil {
		t.Fatal("empty window not preserved")
	}
}
func TestErrorPrecedence(t *testing.T) {
	cases := map[string]string{"caller exited or became unverifiable: context canceled": "caller_lost", "context canceled": "canceled_context", "context deadline exceeded": "canceled_deadline", "malformed event: exit status 1": "protocol_malformed_event", "WaitDelay expired": "process_wait_delay", "exit status 1": "process_exit", "persist event": "persist_event", "secret error": "other"}
	for s, want := range cases {
		if got := errorClass(s); got != want {
			t.Errorf("%q: %s", s, got)
		}
	}
}
func TestDiscoveryAndCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime[1]")
	for _, dir := range []string{"cli-state", "channels/feishu/state", "channels/dingtalk/state"} {
		p := filepath.Join(root, dir)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "state.db"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := discover(Options{Root: root})
	if err != nil || len(refs) != 3 || refs[1].name != "dingtalk" || refs[2].name != "feishu" {
		t.Fatalf("%+v %v", refs, err)
	}
	for _, args := range [][]string{{}, {"--root", "relative"}, {"--root", root, "--home", root}, {"--home", root, "--since", "bad"}, {"--home", root, "--top", "-1"}, {"--home", root, "--format", "xml"}, {"--home", root, "--stale-after", "0s"}, {"--home", root, "extra"}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	home, _ := fixture(t)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"--home", home, "--since", "2026-10-27", "--until", "2026-10-28", "--format", "json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var r Report
	if err = json.Unmarshal(out.Bytes(), &r); err != nil || r.Window.Since.Day() != 27 {
		t.Fatal("invalid command output")
	}
}

// Opt-in synthetic scale check: never reads a production store.
func TestScale(t *testing.T) {
	if os.Getenv("STATS_SCALE") != "1" {
		t.Skip("set STATS_SCALE=1 for 250 MB synthetic fixture")
	}
	home, db := fixture(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", 250000)
	for i := 0; i < 1000; i++ {
		r := state.Run{ID: strings.Repeat("r", 1) + time.Unix(int64(i), 0).Format("150405"), CreatedAt: fixtureNow.Add(-time.Hour), ExecutionStatus: "completed", Input: payload}
		raw, _ := json.Marshal(r)
		if _, err = tx.Exec("INSERT INTO runs VALUES(?,?,?,?,?)", r.ID, r.CreatedAt.Format(time.RFC3339Nano), r.ExecutionStatus, "skipped", raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	r, err := Collect(ctx, options(home))
	elapsed := time.Since(start)
	if err != nil || r.Totals["runs"] != 1000 {
		t.Fatalf("scale failed: %v", err)
	}
	t.Logf("synthetic store bytes=%d runs=%d elapsed=%s", info.Size(), r.Totals["runs"], elapsed)
}

func TestOptionalMeasurementsAndExactUpperBound(t *testing.T) {
	home, db := fixture(t)
	o := options(home)
	start := o.Until.Add(-time.Nanosecond)
	end := start.Add(time.Second)
	r := state.Run{ID: "edge", CreatedAt: start, CompletedAt: &end, ExecutionStatus: "completed"}
	zero := event.Event{Type: "agent.result", Component: "harness.claude", Data: map[string]any{"raw": map[string]any{"type": "result", "uuid": "zero", "total_cost_usd": 0, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}}}
	insertRun(t, db, r, activity("Read"), activity("Read"), zero, zero)
	r.ID = "negative"
	r.CreatedAt = o.Since.Add(time.Hour)
	before := r.CreatedAt.Add(-time.Second)
	r.CompletedAt = &before
	insertRun(t, db, r, event.Event{Type: "agent.result", Component: "harness.acp"})
	result, err := Collect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Totals["runs"] != 2 || result.Usage.Runs != 1 || result.Coverage["usage"] != [2]int{1, 2} || result.Duration.Count != 1 || result.Tools.Named[0].Runs != 1 || result.Coverage["tools_named"] != [2]int{1, 1} || result.Usage.Cost == nil || *result.Usage.Cost != 0 || result.Coverage["cost_usd"] != [2]int{1, 2} {
		t.Fatalf("bad optional evidence: %+v", result)
	}
	o.Agent = "missing"
	result, err = Collect(context.Background(), o)
	if err != nil || result.Usage.Cost != nil {
		t.Fatal("empty usage must be null")
	}
}

func TestBusyStoreExcluded(t *testing.T) {
	home, db := fixture(t)
	good, _ := fixture(t)
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE); PRAGMA journal_mode=DELETE; BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ROLLBACK")
	o := options(home)
	o.Homes = append(o.Homes, good)
	start := time.Now()
	r, err := Collect(context.Background(), o)
	if err != nil || r.Sources[0].Status != "unreadable" || r.Sources[1].Status != "ok" || time.Since(start) > 3*time.Second {
		t.Fatalf("busy isolation failed: %+v %v", r, err)
	}
}

func TestEvenDurationPercentiles(t *testing.T) {
	a := newAggregate(fixtureNow.Add(-time.Hour), fixtureNow, fixtureNow)
	a.durations = []float64{40, 10, 20, 30}
	a.r.Totals["runs"] = 5
	a.finish(10)
	if *a.r.Duration.Median != 25 || *a.r.Duration.P90 != 40 || *a.r.Duration.Max != 40 || a.r.Coverage["duration"] != [2]int{4, 5} {
		t.Fatalf("incorrect percentiles: %+v", a.r.Duration)
	}
}
