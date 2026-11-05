package nativepolicy

import (
	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexHostConsumesNativeEventsAndResume(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", root)
	account := filepath.Join(root, "account")
	if err := os.Mkdir(account, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account, "auth.json"), []byte(`{"token":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", account)
	t.Setenv("OFFLOAD_MAX_WORKERS", "-1")
	binary := filepath.Join(root, "codex")
	log := filepath.Join(root, "codex-requests")
	const threadID = "11111111-1111-4111-8111-111111111111"
	script := fmt.Sprintf(`#!/bin/sh
read_frame() { IFS= read -r frame || exit 0; printf '%%s\n' "$frame" >> %s; }
read_frame
printf '%%s\n' '{"id":1,"result":{"userAgent":"fixture"}}'
read_frame
read_frame
printf '%%s\n' '{"id":2,"result":{}}'
read_frame
printf '%%s\n' '{"id":3,"result":{"data":[{"cwd":"/workspace","errors":[],"skills":[]}]}}'
read_frame
printf '%%s\n' '{"id":4,"result":{"data":[{"cwd":"/workspace","errors":[],"skills":[]}]}}'
read_frame
printf '%%s\n' '{"id":5,"result":{"model":"gpt-native","thread":{"id":"%s","model":"gpt-native"}}}'
read_frame
printf '%%s\n' '{"id":6,"result":{"turn":{"id":"turn-native","status":"inProgress","items":[]}}}'
printf '%%s\n' '{"method":"turn/started","params":{"threadId":"%s","turn":{"id":"turn-native"}}}'
printf '%%s\n' '{"method":"item/agentMessage/delta","params":{"threadId":"%s","turnId":"turn-native","itemId":"message","delta":"Codex answer"}}'
printf '%%s\n' '{"method":"turn/completed","params":{"threadId":"%s","turn":{"id":"turn-native","status":"completed","items":[{"id":"message","type":"agentMessage","text":"Codex answer"}],"error":null}}}'
mkdir -p "$CODEX_HOME/sessions"
printf '%%s\n' '{"type":"session_meta","payload":{"id":"%s"}}' > "$CODEX_HOME/sessions/rollout-fixture-%s.jsonl"
`, shellQuote(log), threadID, threadID, threadID, threadID, threadID, threadID)
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
	request := harness.Request{Workspace: root, NativeProfile: root, HostSessionID: "host-session", Prompt: "task", NativeSessionID: "host-placeholder", PermissionMode: "bypassPermissions", PresetDocuments: []harness.PresetDocument{{Name: "USERS.md", Content: []byte("CODEX_HOT_USER_731")}, {Name: "MEMORY.md", Content: []byte("CODEX_HOT_MEMORY_732")}}}
	init := 0
	result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
		if o.Type == "init" {
			init++
		}
		return nil
	})
	if err != nil || result.Status != "completed" || result.Text != "Codex answer" || result.Model != "gpt-native" || result.NativeSessionID != threadID || init != 1 || result.Evidence.Backend != "codex" || result.Evidence.NativeProtocol != "codex-app-server" {
		t.Fatalf("%+v evidence=%+v %v", result, result.Evidence, err)
	}
	logged, readErr := os.ReadFile(log)
	if readErr != nil || !strings.Contains(string(logged), "CODEX_HOT_USER_731") || !strings.Contains(string(logged), "CODEX_HOT_MEMORY_732") {
		t.Fatalf("Codex SDK request omitted preset documents: %v %s", readErr, logged)
	}
	request.NativeSessionID = result.NativeSessionID
	request.Resume = true
	resumed, err := adapter.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("resume=%+v evidence=%+v err=%v", resumed, resumed.Evidence, err)
	}
	request.ToolsDisabled = true
	if _, err := adapter.Run(context.Background(), request, nil); err == nil {
		t.Fatal("unsupported tool-free policy accepted")
	}
	request.ToolsDisabled = false
	if err := os.MkdirAll(filepath.Join(root, "skills", "configured"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "configured", "SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := approvedSkillRoots(root, "")
	if err != nil || len(roots) != 1 || !strings.HasSuffix(roots[0], "/skills/configured") {
		t.Fatalf("configured native Skills were not explicitly resolved: %v %v", roots, err)
	}
}
