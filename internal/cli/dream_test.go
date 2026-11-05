package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDreamScanRoutesReadOnlyJSON(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "handoff-cli")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"launch.json": `{"exit_code":0,"finalized_at_utc":"2026-11-05T01:02:03Z","stream_format":"text"}`,
		"request.txt": "read only",
		"stdout.txt":  "finished",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errout bytes.Buffer
	err = Run(t.Context(), []string{"dream", "scan", "--source", root, "--person", "person", "--agent", "agent", "--audience", "private", "--memory-scope", "scope", "--format", "json"}, bytes.NewReader(nil), &out, &errout)
	if err != nil {
		t.Fatalf("dream scan: %v (%s)", err, errout.String())
	}
	var report struct {
		Status          string                    `json:"status"`
		WritesPerformed bool                      `json:"writes_performed"`
		ModelInvoked    bool                      `json:"model_invoked"`
		Entries         []struct{ Eligible bool } `json:"entries"`
	}
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Status != "scan_only" || report.WritesPerformed || report.ModelInvoked || len(report.Entries) != 1 || !report.Entries[0].Eligible {
		t.Fatalf("unexpected CLI output: %s", out.String())
	}
}

func TestDreamScanRequiresExplicitJSON(t *testing.T) {
	err := runDream([]string{"scan", "--source", "/tmp", "--person", "p", "--agent", "a", "--audience", "private", "--memory-scope", "s"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("dream scan accepted implicit output format")
	}
}
