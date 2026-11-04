//go:build unix

package offloadexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func libraryFixture(t *testing.T) (*Library, string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-home"))
	t.Setenv("OFFLOAD_MAX_WORKERS", "-1")
	script := `#!/bin/sh
d="$(dirname "$0")"
printf '%s\n' "$@" > "$d/native.args"
cat > "$d/native.stdin"
echo started > "$d/started"
printf '%s\n' '{"type":"thread.started","thread_id":"native-session"}'
if [ -f "$d/hang" ]; then exec sleep 60; fi
if [ -f "$d/fail" ]; then exit 7; fi
printf '%s\n' '{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"SDK answer"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":2}}'
`
	native := filepath.Join(dir, "native")
	if err := os.WriteFile(native, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_CODEX", native)
	// Any accidental compatibility fallback would execute this sentinel.
	if err := os.WriteFile(filepath.Join(dir, "offload"), []byte("#!/bin/sh\necho forbidden > '"+dir+"/cli-started'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(dir, "native.json")
	if err := os.WriteFile(path, []byte(`{"default":"unused","profiles":{"canonical":{"backend":"codex","aliases":["alias"],"model":"fixture"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewLibrary(path, "alias", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client, dir, path
}

func TestLibraryExecutesAndResumesWithoutOffloadCLI(t *testing.T) {
	c, dir, _ := libraryFixture(t)
	req := Request{CallerRunID: "agh:first", Prompt: "visible task", Instructions: "internal context", Workspace: dir, Write: true}
	first, err := c.Run(t.Context(), req)
	if err != nil || first.Status != StatusOK || first.Text != "SDK answer" || first.Profile != "canonical" || first.RunID == "" || first.Session != "native-session" {
		t.Fatalf("%+v %v", first, err)
	}
	var record map[string]any
	if json.Unmarshal(first.Raw, &record) != nil || record["caller"] != "agh" || record["caller_run_id"] != req.CallerRunID || record["task"] != req.Prompt {
		t.Fatalf("lost execution provenance: %s", first.Raw)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "native.stdin"))
	argv, _ := os.ReadFile(filepath.Join(dir, "native.args"))
	if !strings.Contains(string(stdin), req.Instructions) || !strings.Contains(string(stdin), "Task:\n"+req.Prompt) || strings.Contains(string(argv), req.Instructions) || strings.Contains(string(argv), req.Prompt) {
		t.Fatal("task/context transport lost")
	}
	req.Session, req.ResumeProfile, req.CallerRunID = first.Session, first.Profile, "agh:second"
	second, err := c.Run(t.Context(), req)
	argv, _ = os.ReadFile(filepath.Join(dir, "native.args"))
	if err != nil || second.Session != first.Session || second.RunID == first.RunID || !strings.Contains(string(argv), "resume\n") || !strings.Contains(string(argv), first.Session+"\n") {
		t.Fatalf("resume %+v %v argv=%s", second, err, argv)
	}
	if _, err := os.Stat(filepath.Join(dir, "cli-started")); !os.IsNotExist(err) {
		t.Fatal("Offload CLI was launched")
	}
	if c.Transport() != "go-package" {
		t.Fatal("incorrect transport identity")
	}
	if first.HistoryStatus != "saved" || second.HistoryStatus != "saved" || first.HistoryError != "" {
		t.Fatalf("history sync missing: %+v %+v", first, second)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state", "offload", "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("history count %d", len(lines))
	}
	for i, want := range []Result{first, second} {
		var got map[string]any
		if json.Unmarshal([]byte(lines[i]), &got) != nil || got["run_id"] != want.RunID || got["caller_run_id"] != want.CallerRunID {
			t.Fatalf("history correlation lost: %s", lines[i])
		}
	}
}

func TestLibraryHistoryFailureDoesNotChangeNativeSuccess(t *testing.T) {
	c, dir, _ := libraryFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, "state", "offload", "runs.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := c.Run(t.Context(), Request{Prompt: "inspect", Workspace: dir})
	if err != nil || r.Status != StatusOK || r.Text != "SDK answer" || r.HistoryStatus != "failed" || r.HistoryError == "" || len(r.Raw) == 0 {
		t.Fatalf("history failure obscured native execution: %+v %v", r, err)
	}
}

func TestLibraryCancellationFailureAndNoReplay(t *testing.T) {
	c, dir, _ := libraryFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "hang"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, err := c.Run(ctx, Request{Prompt: "wait", Workspace: dir})
		if r.Status != StatusCanceled || r.ErrorKind != KindCanceled || len(r.Raw) == 0 {
			done <- errors.New("cancellation outcome/evidence lost")
			return
		}
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native fixture did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("owned native cancellation did not complete")
	}
	os.Remove(filepath.Join(dir, "hang"))
	os.WriteFile(filepath.Join(dir, "fail"), nil, 0600)
	r, err := c.Run(t.Context(), Request{Prompt: "fail", Workspace: dir})
	if err == nil || r.Status != StatusError || len(r.Raw) == 0 {
		t.Fatalf("native failure hidden: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cli-started")); !os.IsNotExist(err) {
		t.Fatal("failed SDK call fell back to CLI")
	}
}

func TestLibraryRejectsUnsafeConfigAndChangedResumeProfile(t *testing.T) {
	c, dir, path := libraryFixture(t)
	ctx, stop := context.WithCancelCause(t.Context())
	cause := errors.New("host caller exited")
	stop(cause)
	if _, err := c.Run(ctx, Request{Prompt: "inspect", Workspace: dir}); !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation cause lost: %v", err)
	}
	for _, input := range []string{"relative", filepath.Join(dir, "missing")} {
		if _, err := NewLibrary(input, "alias", "", time.Minute); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLibrary(path, "alias", "", time.Minute); err == nil {
		t.Fatal("shared-readable credential config accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(dir, "link.json")
	os.Symlink(path, link)
	if _, err := NewLibrary(link, "alias", "", time.Minute); err == nil {
		t.Fatal("symlink config accepted")
	}
	if _, err := c.Run(t.Context(), Request{Prompt: "resume", Workspace: dir, Session: "native-session", ResumeProfile: "other"}); err == nil {
		t.Fatal("changed resume profile accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
		t.Fatal("rejected request started native execution")
	}
	before := c.Fingerprint()
	body, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(body), `"fixture"`, `"changed-model"`, 1)), 0600)
	changed, err := NewLibrary(path, "alias", "", c.timeout)
	if err != nil || changed.Fingerprint() == before {
		t.Fatalf("native profile changes not fingerprinted: %v", err)
	}
}
