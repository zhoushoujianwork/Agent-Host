package stats

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func findCheck(t *testing.T, r *StatusReport, id, scope string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id && (scope == "" || c.Scope == scope) {
			return c
		}
	}
	t.Fatalf("missing %s", id)
	return Check{}
}
func statusTables(t *testing.T, db *sql.DB) {
	t.Helper()
	_, e := db.Exec(`CREATE TABLE channel_messages(status TEXT,received_at TEXT,payload BLOB); CREATE TABLE candidates(status TEXT,payload BLOB);`)
	if e != nil {
		t.Fatal(e)
	}
}
func TestStatusPrivacyCurrentStateAndReadOnly(t *testing.T) {
	home, db := fixture(t)
	statusTables(t, db)
	secret := "PRIVATE_INPUT_OUTPUT_SENDER_PROFILE_PAYLOAD"
	for _, v := range []struct {
		id, status string
		at         time.Time
	}{
		{"stale-old", "running", fixtureNow.Add(-48 * time.Hour)},
		{"threshold", "running", fixtureNow.Add(-2 * time.Hour)},
		{"unrecorded", "unknown", fixtureNow.Add(-72 * time.Hour)},
		{"lower", "completed", fixtureNow.Add(-24 * time.Hour)},
		{"upper", "failed", fixtureNow},
		{"canceled", "canceled", fixtureNow.Add(-time.Hour)},
		{"postprocess", "completed", fixtureNow.Add(-time.Hour)},
		{"failure", "failed", fixtureNow.Add(-time.Hour)},
	} {
		insertRun(t, db, state.Run{ID: v.id, CreatedAt: v.at, ExecutionStatus: v.status, MemoryStatus: "failed", Input: secret, Output: secret, Error: "exit status 1 " + secret})
	}
	_, e := db.Exec(`INSERT INTO channel_messages VALUES('unknown',?,?),('failed',?,?)`, fixtureNow.Add(-time.Hour).Format(time.RFC3339Nano), secret, fixtureNow.Format(time.RFC3339Nano), `{"error":"unsupported message type"}`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO candidates VALUES('pending',?)`, `{"created_at":"2026-10-28T10:00:00Z"}`); e != nil {
		t.Fatal(e)
	}

	before := map[string][]byte{}
	for _, name := range []string{"state.db", "state.db-wal"} {
		before[name], e = os.ReadFile(filepath.Join(home, name))
		if e != nil {
			t.Fatal(e)
		}
	}
	o := options(home)
	o.Homes = append(o.Homes, filepath.Join(t.TempDir(), "missing"))
	r, e := CollectStatus(context.Background(), o, true, 8765)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"runs.stale_running", "runs.unrecorded", "intake.unresolved", "memory.candidates"} {
		c := findCheck(t, r, id, "store-1")
		if c.Status != "warning" || c.Evidence.(map[string]any)["count"] != 1 {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	if findCheck(t, r, "intake.unresolved", "").Evidence.(map[string]any)["ambiguous_intake_count"] != 1 {
		t.Fatal("ambiguous intake omitted")
	}
	counts := findCheck(t, r, "runs.window", "").Evidence.(map[string]any)["outcomes"].(map[string]int)
	if counts["completed"] != 2 || counts["failed"] != 1 || counts["canceled"] != 1 || counts["running"] != 1 {
		t.Fatal(counts)
	}
	errors := findCheck(t, r, "runs.failures", "store-1").Evidence.([]ErrorClass)
	if len(errors) != 1 || errors[0].Runs != 1 || errors[0].Class != "process_exit" {
		t.Fatal(errors)
	}
	for _, id := range []string{"service.process", "deployment.mode", "memory.verification", "workbench.http"} {
		if findCheck(t, r, id, "").Status != "unknown" {
			t.Fatal(id)
		}
	}
	raw, _ := json.Marshal(r)
	var text bytes.Buffer
	if e = WriteStatusText(&text, r); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw)+text.String(), secret) {
		t.Fatal("private content escaped")
	}
	for name, want := range before {
		got, e := os.ReadFile(filepath.Join(home, name))
		if e != nil || !bytes.Equal(want, got) {
			t.Fatalf("mutated %s", name)
		}
	}
	if _, e = os.Stat(filepath.Join(home, "host.lock")); !os.IsNotExist(e) {
		t.Fatal("lock created")
	}
}
func TestStatusRootAuxiliaryStores(t *testing.T) {
	root := t.TempDir()
	home, db := fixture(t)
	statusTables(t, db)
	if e := os.Symlink(home, filepath.Join(root, "cli-state")); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "channels", "dingtalk", "dws")
	if e := os.MkdirAll(path, 0700); e != nil {
		t.Fatal(e)
	}
	ops, e := sql.Open("sqlite", filepath.Join(path, "host-operations.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer ops.Close()
	_, e = ops.Exec(`CREATE TABLE operations(profile TEXT,id TEXT,status TEXT,updated TEXT);INSERT INTO operations VALUES('SECRET_PROFILE','SECRET_OPERATION','provider_pending','2026-10-13T00:00:00Z'),('hidden','hidden','partial_failure','2026-10-13T00:00:00Z')`)
	if e != nil {
		t.Fatal(e)
	}
	feedback := filepath.Join(root, "feedback")
	os.MkdirAll(feedback, 0700)
	f, e := sql.Open("sqlite", filepath.Join(feedback, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	_, e = f.Exec(`CREATE TABLE feedback(status TEXT,payload TEXT); INSERT INTO feedback VALUES('needs_recipient','{"updated_at":"2026-10-13T00:00:00Z","text":"SECRET_PROFILE"}')`)
	if e != nil {
		t.Fatal(e)
	}
	o := options(home)
	o.Homes = nil
	o.Root = root
	r, e := CollectStatus(context.Background(), o, true, 8765)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"operations.pending", "feedback.recipients"} {
		if c := findCheck(t, r, id, ""); c.Status != "warning" || c.Evidence.(map[string]any)["count"] != 1 {
			t.Fatal(c)
		}
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "SECRET_") {
		t.Fatal("private auxiliary data")
	}
}
func TestStatusUsageWrongHomeAndHTTP(t *testing.T) {
	for _, args := range [][]string{{}, {"--root", "relative"}, {"--home", "x", "--window", "0s"}, {"--home", "x", "--format", "xml"}, {"--home", "x", "--viewer-port", "0"}, {"--root", "/x", "--home", "y"}} {
		if RunStatus(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}) == nil {
			t.Fatal(args)
		}
	}
	root := t.TempDir()
	candidate := filepath.Join(root, "runtime", "channels", "dingtalk", "state")
	os.MkdirAll(candidate, 0700)
	var out bytes.Buffer
	e := RunStatus(context.Background(), []string{"--home", root, "--no-http", "--format", "json"}, &out, &bytes.Buffer{})
	if e == nil || !strings.Contains(e.Error(), candidate) || !strings.Contains(e.Error(), "state.db inside --home") {
		t.Fatal(e)
	}
	home, db := fixture(t)
	statusTables(t, db)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); w.Write([]byte("SECRET_BODY")) }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	report, e := CollectStatus(context.Background(), options(home), false, p)
	if e != nil {
		t.Fatal(e)
	}
	c := findCheck(t, report, "workbench.http", "")
	if c.Status != "warning" || c.Evidence.(map[string]int)["http_status"] != 503 {
		t.Fatal(c)
	}
	if counts := findCheck(t, report, "runs.window", "").Evidence.(map[string]any)["outcomes"].(map[string]int); counts["completed"] != 0 {
		t.Fatal(counts)
	}
}

func TestStatusUnsupportedAndExistingLock(t *testing.T) {
	home, db := fixture(t)
	statusTables(t, db)
	bad, badDB := fixture(t)
	if _, e := badDB.Exec("PRAGMA user_version=999"); e != nil {
		t.Fatal(e)
	}
	lock := filepath.Join(home, "host.lock")
	if e := os.WriteFile(lock, []byte("existing owner"), 0600); e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(lock)
	if e != nil {
		t.Fatal(e)
	}
	o := options(home)
	o.Homes = append(o.Homes, bad)
	r, e := CollectStatus(context.Background(), o, true, 8765)
	if e != nil {
		t.Fatal(e)
	}
	if c := findCheck(t, r, "store.store-2", ""); c.Status != "failed" || !strings.Contains(c.Message, "999") {
		t.Fatal(c)
	}
	after, e := os.Stat(lock)
	if e != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("lock modified", e)
	}
	content, e := os.ReadFile(lock)
	if e != nil || string(content) != "existing owner" {
		t.Fatal("lock content changed")
	}
	o.Homes = []string{bad}
	r, e = CollectStatus(context.Background(), o, true, 8765)
	if e == nil {
		t.Fatal("all unsupported succeeded")
	}
	if findCheck(t, r, "runs.window", "").Evidence.(map[string]any)["outcomes"] != nil {
		t.Fatal("unavailable window claimed zero")
	}
}
