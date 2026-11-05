package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var testDestination = Destination{PersonID: "person-a", Agent: "agent-a", Audience: "private-a", MemoryScope: "project:alpha"}

func privateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writePrivate(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func handoff(t *testing.T, root, name string, exit any) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	launch, _ := json.Marshal(map[string]any{"exit_code": exit, "finalized_at_utc": "2026-11-05T01:02:03Z", "stream_format": "json"})
	writePrivate(t, filepath.Join(dir, "launch.json"), launch)
	writePrivate(t, filepath.Join(dir, "request.txt"), []byte("inspect exact evidence"))
	writePrivate(t, filepath.Join(dir, "events.jsonl"), []byte("{\"type\":\"run.finished\",\"run_id\":\"run-a\",\"session_id\":\"session-a\"}\n"))
	return dir
}

func TestScanFinalizedAndUnfinalized(t *testing.T) {
	root := privateRoot(t)
	handoff(t, root, "handoff-valid", 0)
	dir := handoff(t, root, "handoff-open", nil)
	launch, _ := json.Marshal(map[string]any{"exit_code": nil, "finalized_at_utc": nil, "stream_format": "json"})
	writePrivate(t, filepath.Join(dir, "launch.json"), launch)
	report, err := Scan(root, testDestination)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 2 || report.Entries[0].Directory != "handoff-open" || report.Entries[0].Diagnostics[0].Code != "not_finalized" || !report.Entries[1].Eligible || report.Entries[1].Manifest.CanonicalRunID != "run-a" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestFailedExecutionIsDiagnosticOnly(t *testing.T) {
	root := privateRoot(t)
	handoff(t, root, "handoff-failed", 7)
	report, err := Scan(root, testDestination)
	if err != nil || len(report.Entries) != 1 {
		t.Fatal(err, report)
	}
	entry := report.Entries[0]
	if entry.Eligible || entry.Manifest == nil || entry.Manifest.ExitCode != 7 || entry.Diagnostics[0].Code != "source_execution_failed" || entry.IdempotencyKey == "" {
		t.Fatalf("failure was not retained as diagnostic-only: %#v", entry)
	}
}

func TestScanRejectsChangingSource(t *testing.T) {
	root := privateRoot(t)
	dir := handoff(t, root, "handoff-changing", 0)
	changed := false
	scanner := Scanner{afterRead: func(_ string, name string) {
		if name == "request.txt" && !changed {
			changed = true
			writePrivate(t, filepath.Join(dir, name), []byte("changed during scan"))
		}
	}}
	report, err := scanner.Scan(root, testDestination)
	if err != nil || report.Entries[0].Diagnostics[0].Code != "source_changed" {
		t.Fatalf("source mutation not rejected: err=%v report=%#v", err, report)
	}
}

func TestScanRejectsSymlinkAndNonPrivateFile(t *testing.T) {
	root := privateRoot(t)
	dir := handoff(t, root, "handoff-symlink", 0)
	target := filepath.Join(root, "target")
	writePrivate(t, target, []byte("{}"))
	if err := os.Symlink(target, filepath.Join(dir, "run.json")); err != nil {
		t.Fatal(err)
	}
	dir = handoff(t, root, "handoff-public", 0)
	if err := os.Chmod(filepath.Join(dir, "request.txt"), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(root, testDestination)
	if err != nil || len(report.Entries) != 2 {
		t.Fatal(err, report)
	}
	for _, entry := range report.Entries {
		if entry.Eligible || entry.Diagnostics[0].Code != "unsafe_source" {
			t.Fatalf("unsafe source accepted: %#v", entry)
		}
	}
}

func TestScanRejectsSizeBudget(t *testing.T) {
	root := privateRoot(t)
	dir := handoff(t, root, "handoff-large", 0)
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	report, err := Scan(root, testDestination)
	if err != nil || report.Entries[0].Diagnostics[0].Code != "size_budget_exceeded" {
		t.Fatalf("oversized source not rejected: err=%v report=%#v", err, report)
	}
}

func TestDuplicateDeterminismAndScopeIsolation(t *testing.T) {
	root := privateRoot(t)
	handoff(t, root, "handoff-a", 0)
	handoff(t, root, "handoff-b", 0)
	first, err := Scan(root, testDestination)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Scan(root, testDestination)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("scan is not deterministic: %v", err)
	}
	if first.Entries[0].Manifest.Digest != first.Entries[1].Manifest.Digest || first.Entries[0].IdempotencyKey == first.Entries[1].IdempotencyKey {
		t.Fatal("identical evidence was not deterministic or source-isolated")
	}
	other := testDestination
	other.MemoryScope = "project:beta"
	isolated, err := Scan(root, other)
	if err != nil || isolated.Entries[0].IdempotencyKey == first.Entries[0].IdempotencyKey {
		t.Fatal("destination scope did not isolate idempotency")
	}
}

func TestManifestDigestIncludesOptionalNamesAndContents(t *testing.T) {
	root := privateRoot(t)
	dir := handoff(t, root, "handoff-manifest", 0)
	base, err := Scan(root, testDestination)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(dir, "run.json"), []byte(`{"id":"run-a","session_id":"session-a","note":"one"}`))
	withFile, err := Scan(root, testDestination)
	if err != nil || withFile.Entries[0].Manifest.Digest == base.Entries[0].Manifest.Digest {
		t.Fatal("optional filename was absent from manifest digest")
	}
	writePrivate(t, filepath.Join(dir, "run.json"), []byte(`{"id":"run-a","session_id":"session-a","note":"two"}`))
	changed, err := Scan(root, testDestination)
	if err != nil || changed.Entries[0].Manifest.Digest == withFile.Entries[0].Manifest.Digest {
		t.Fatal("optional content was absent from manifest digest")
	}
}

func TestCanonicalRefsCannotOverrideTrustedPartition(t *testing.T) {
	root := privateRoot(t)
	dir := handoff(t, root, "handoff-refs", 0)
	launch := []byte(`{"exit_code":0,"finalized_at_utc":"2026-11-05T01:02:03Z","stream_format":"json","agent":"source-agent","audience":"source-person","memory_scope":"global"}`)
	writePrivate(t, filepath.Join(dir, "launch.json"), launch)
	writePrivate(t, filepath.Join(dir, "run.json"), []byte(`{"id":"run-canonical","session_id":"session-canonical"}`))
	writePrivate(t, filepath.Join(dir, "events.jsonl"), []byte(`{"type":"tool.activity","data":{"run_id":"forged-run","session_id":"forged-session"}}`+"\n"))
	report, err := Scan(root, testDestination)
	if err != nil || report.Destination != testDestination {
		t.Fatalf("source overrode trusted partition: err=%v report=%#v", err, report)
	}
	manifest := report.Entries[0].Manifest
	if manifest == nil || manifest.CanonicalRunID != "run-canonical" || manifest.CanonicalSession != "session-canonical" {
		t.Fatalf("canonical references were not read from structured fields: %#v", manifest)
	}
}

func TestScanLeavesOriginalFilesUntouched(t *testing.T) {
	root := privateRoot(t)
	dir := handoff(t, root, "handoff-pristine", 0)
	before := map[string][]byte{}
	for _, name := range []string{"launch.json", "request.txt", "events.jsonl"} {
		before[name], _ = os.ReadFile(filepath.Join(dir, name))
	}
	if _, err := Scan(root, testDestination); err != nil {
		t.Fatal(err)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("source changed for %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("scanner created source artifacts: %v %#v", err, entries)
	}
}
