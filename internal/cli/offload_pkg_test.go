//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
)

func pkgChatFixture(t *testing.T) (options, []string, string) {
	t.Helper()
	dir := canonicalTemp(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "native-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "native-state"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "native-home"))
	t.Setenv("OFFLOAD_MAX_WORKERS", "-1")
	native := filepath.Join(dir, "native")
	script := `#!/bin/sh
d="$(dirname "$0")"
n=$(cat "$d/count" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$d/count"
printf '%s\n' "$@" > "$d/argv.$n"
cat > "$d/stdin.$n"
session="session-sdk-$n"
for arg in "$@"; do case "$arg" in session-sdk-*) session="$arg";; esac; done
printf '{"type":"thread.started","thread_id":"%s"}\n' "$session"
printf '%s\n' '{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"package answer"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}'
`
	if err := os.WriteFile(native, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_CODEX", native)
	if err := os.WriteFile(filepath.Join(dir, "offload"), []byte("#!/bin/sh\necho forbidden > '"+dir+"/cli-started'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	nativeConfig := filepath.Join(dir, "native.json")
	writeBotJSON(t, nativeConfig, map[string]any{"profiles": map[string]any{"canonical": map[string]any{"backend": "codex", "aliases": []string{"alias"}, "model": "native-fixture"}}})
	profile := filepath.Join(dir, "offload-pkg.json")
	writeBotJSON(t, profile, map[string]any{"version": 2, "config_path": nativeConfig, "profile": "alias", "timeout": "30s"})
	home, workspace := canonicalTemp(t), canonicalTemp(t)
	args := []string{"--offload-config", profile, "--workspace", workspace, "--home", home, "--format", "json"}
	o, _, err := parse("chat", args, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	return o, append([]string{"chat"}, args...), dir
}

func TestPackageChatFreshResumeAndRoomRetry(t *testing.T) {
	o, args, dir := pkgChatFixture(t)
	var previous state.Run
	for i, text := range []string{"first package task", "continued package task", "room task", "room task"} {
		turn := append(append([]string{}, args...), "-m", text)
		if i == 1 {
			turn = append(turn, "--resume", previous.SessionID)
		}
		if i >= 2 {
			turn = append(turn, "--room", "pkg-room", "--request-id", "pkg-request")
		}
		events, err := chatOffloadEvents(t, turn)
		if err != nil {
			t.Fatal(err)
		}
		id, transport := "", ""
		for _, e := range events {
			if e.Type == "run.started" {
				id = e.RunID
			}
			if e.Type == "agent.dispatched" {
				value, _ := e.Data["execution"].(map[string]any)
				transport, _ = value["transport"].(string)
			}
			if e.Type == "offload.result" {
				t.Fatal("private native record leaked to Chat")
			}
		}
		if transport != "go-package" || id == "" {
			t.Fatalf("missing package execution identity: %s %s", id, transport)
		}
		store, err := state.Open(o.home, false)
		if err != nil {
			t.Fatal(err)
		}
		run, err := store.Run(t.Context(), id)
		store.Close()
		if err != nil || run.ExecutionStatus != "completed" || run.Output != "package answer" || !run.MemoryPluginDisabled {
			t.Fatalf("%+v %v", run, err)
		}
		if i == 1 && (run.NativeSessionID != previous.NativeSessionID || run.SessionID != previous.SessionID) {
			t.Fatal("package continuation lost its Host/native binding")
		}
		if i == 3 && run.ID != previous.ID {
			t.Fatal("accepted room retry started another run")
		}
		found := false
		for _, e := range run.Events {
			if e.Type == "offload.result" {
				found = true
				raw, _ := json.Marshal(e.Data["raw"])
				var record map[string]any
				if json.Unmarshal(raw, &record) != nil || record["caller"] != "agh" || record["caller_run_id"] != "agh:"+id || record["task"] != text {
					t.Fatal("package result lost attributed private evidence")
				}
			}
		}
		if !found {
			t.Fatal("private package result was not retained")
		}
		previous = run
	}
	count, _ := os.ReadFile(filepath.Join(dir, "count"))
	if strings.TrimSpace(string(count)) != "3" {
		t.Fatal("room retry repeated native execution")
	}
	if _, err := os.Stat(filepath.Join(dir, "cli-started")); !os.IsNotExist(err) {
		t.Fatal("package Chat started the Offload CLI")
	}
}

func TestPackageConfigurationSupportsChannelComposition(t *testing.T) {
	o, _, _ := pkgChatFixture(t)
	if err := resolveOffloadChannelOptions(&o); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(o.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := composeOffloadChannelHost(store, o, nil).(*offloadchannel.Host)
	session, err := h.Session(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.Run(t.Context(), session, "channel-composed package task")
	if err != nil || run.ExecutionStatus != "completed" || run.Output != "package answer" {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestPackageWebBridgeActualCLIAndNative(t *testing.T) {
	o, _, dir := pkgChatFixture(t)
	native := filepath.Join(dir, "native")
	raw, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), `session="session-sdk-$n"`, `case "$(cat "$d/stdin.$n")" in *slow-room*) exec sleep 60;; esac
session="session-sdk-$n"`, 1)
	if err := os.WriteFile(native, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runOffloadWebBridge(t, o, "package answer", "package answer")
	if _, err := os.Stat(filepath.Join(dir, "cli-started")); !os.IsNotExist(err) {
		t.Fatal("Web bridge started the Offload CLI")
	}
}
