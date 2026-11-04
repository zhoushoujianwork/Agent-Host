package nativepolicy

import (
	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexHostConsumesNativeEventsAndResume(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, "account"))
	t.Setenv("OFFLOAD_MAX_WORKERS", "-1")
	binary := filepath.Join(root, "codex")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"native-codex-session"}' '{"type":"turn.started"}' '{"type":"item.completed","item":{"id":"message","type":"agent_message","text":"Codex answer"}}' '{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":1}}'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_CODEX", binary)
	config := filepath.Join(root, "offload.json")
	if err := os.WriteFile(config, []byte(`{"profiles":{"fixture":{"backend":"codex","model":"fixture"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	library, err := offloadexec.NewLibrary(config, "fixture", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWithLibrary("/unused/claude", library)
	if !adapter.GeneratesSessionID() {
		t.Fatal("Codex session ownership missing")
	}
	request := harness.Request{Workspace: root, Prompt: "task", NativeSessionID: "host-placeholder", PermissionMode: "bypassPermissions"}
	init := 0
	result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
		if o.Type == "init" {
			init++
		}
		return nil
	})
	if err != nil || result.Status != "completed" || result.Text != "Codex answer" || result.NativeSessionID != "native-codex-session" || init != 1 || result.Evidence.Backend != "codex" {
		t.Fatalf("%+v %v", result, err)
	}
	request.NativeSessionID = result.NativeSessionID
	request.Resume = true
	if _, err := adapter.Run(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	request.ToolsDisabled = true
	if _, err := adapter.Run(context.Background(), request, nil); err == nil {
		t.Fatal("unsupported tool-free policy accepted")
	}
	request.ToolsDisabled = false
	request.NativeProfile = root
	if err := os.MkdirAll(filepath.Join(root, "skills", "configured"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Run(context.Background(), request, nil); err == nil {
		t.Fatal("configured native Skills silently ignored")
	}
}
