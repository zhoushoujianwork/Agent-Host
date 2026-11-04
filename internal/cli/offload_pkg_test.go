//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/channel"
	"agent-host/internal/harness"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
	"agent-host/internal/state"
)

type compatibilityExecutor struct{}

func (compatibilityExecutor) Run(context.Context, offloadexec.Request) (offloadexec.Result, error) {
	return offloadexec.Result{}, nil
}

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
while [ $# -gt 0 ]; do case "$1" in --resume|--session-id) session="$2"; shift;; esac; shift; done
printf '{"type":"system","subtype":"init","session_id":"%s","model":"native-fixture"}\n' "$session"
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"package answer","usage":{"input_tokens":3,"output_tokens":2}}\n' "$session"
`
	if err := os.WriteFile(native, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_CLAUDE", native)
	if err := os.WriteFile(filepath.Join(dir, "offload"), []byte("#!/bin/sh\necho forbidden > '"+dir+"/cli-started'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	nativeConfig := filepath.Join(dir, "native.json")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	writeBotJSON(t, nativeConfig, map[string]any{"profiles": map[string]any{"canonical": map[string]any{"backend": "claude", "aliases": []string{"alias"}, "model": "native-fixture", "base_url": server.URL, "token": "synthetic-fixture-token"}}})
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
	weakFingerprint := offloadchannel.FingerprintPrefix + channel.Key(o.executionFingerprint, o.workspace, o.defaultScope, o.agentID, o.hostInstructions)
	if err := resolveOffloadChannelOptions(&o); err != nil {
		t.Fatal(err)
	}
	expectedProfile := offloadCleanProfilePath(o.home, o.agentID, o.audienceID)
	if o.nativeProfile != expectedProfile || o.runtimeFingerprint == weakFingerprint {
		t.Fatal("clean native path or strict runtime identity missing", o.nativeProfile, o.runtimeFingerprint)
	}
	if body, err := os.ReadFile(filepath.Join(expectedProfile, "settings.json")); err != nil || string(body) != "{}\n" {
		t.Fatal("clean profile settings changed", string(body), err)
	}
	if entries, err := os.ReadDir(filepath.Join(expectedProfile, "skills")); err != nil || len(entries) != 0 {
		t.Fatal("clean profile contains Skills", entries, err)
	}
	store, err := state.Open(o.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := composeOffloadChannelHost(store, o, nil).(*offloadchannel.Host)
	legacy := state.Session{ID: "legacy", AgentID: o.agentID, AudienceID: o.audienceID, Scope: o.scope, Workspace: o.workspace, RuntimeFingerprint: weakFingerprint, Model: o.model, NativeSessionID: state.OffloadPendingPrefix + "legacy"}
	if err := store.WithPartition(o.agentID, o.audienceID).CreateSession(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Session(t.Context(), legacy.ID); err == nil {
		t.Fatal("weaker pre-isolation session was resumed")
	}
	session, err := h.Session(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.Run(t.Context(), session, "channel-composed package task")
	if err != nil || run.ExecutionStatus != "completed" || run.Output != "package answer" {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestPackageCleanNativeProfileRejectsResidueAndCompatibilityClient(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *options)
	}{
		{"skill residue", func(t *testing.T, o *options) {
			if err := os.WriteFile(filepath.Join(o.nativeProfile, "skills", "ambient"), []byte("ambient"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"skill symlink", func(t *testing.T, o *options) {
			if err := os.Remove(filepath.Join(o.nativeProfile, "skills")); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if err := os.Symlink(target, filepath.Join(o.nativeProfile, "skills")); err != nil {
				t.Fatal(err)
			}
		}},
		{"nonprivate profile", func(t *testing.T, o *options) {
			if err := os.Chmod(o.nativeProfile, 0755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, _, _ := pkgChatFixture(t)
			if err := resolveOffloadChannelOptions(&o); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, &o)
			if err := resolveOffloadChannelOptions(&o); err == nil {
				t.Fatal("unsafe clean native profile was accepted")
			}
		})
	}
	o, _, dir := pkgChatFixture(t)
	o.offloadClient = compatibilityExecutor{}
	if err := resolveOffloadChannelOptions(&o); err == nil || !strings.Contains(err.Error(), "CLI compatibility selectors are unsupported") {
		t.Fatal("compatibility executor was not rejected", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "count")); !os.IsNotExist(err) {
		t.Fatal("unsupported executor dispatched native work")
	}
}

func TestSelectEmptyNativeChatProfileIgnoresAmbientAndPreservesFullHostOptions(t *testing.T) {
	ambient := canonicalTemp(t)
	if err := os.WriteFile(filepath.Join(ambient, "settings.json"), []byte(`{"ambient":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "history.json"), []byte("ambient history"), 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(ambient, "skills", "ambient", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("ambient skill"), 0600); err != nil {
		t.Fatal(err)
	}
	home, workspace := canonicalTemp(t), canonicalTemp(t)
	o := options{home: home, nativeProfile: ambient, nativeSkillsFingerprint: "ambient-fingerprint", runtimeFingerprint: "keep-runtime",
		workspace: workspace, defaultWorkspace: workspace, scope: "keep-scope", defaultScope: "keep-default-scope",
		projectID: "project", projectIDs: []string{"project", "other"}, primaryProject: "project",
		hostInstructions: "keep instructions", model: "keep-model", permissionMode: "dontAsk", noTools: true,
		allowedTools: []string{"Read"}, mcpServers: []harness.MCPServer{{Name: "keep", Command: "/keep"}},
		knowledgeConfig: "/keep/knowledge.json", knowledgeConfigs: []string{"/keep/one.json"}, knowledgeNames: []string{"keep-source"},
		memoryOffloadConfig: "/keep/memory.json", skillsRoot: "/keep/managed-skills", managedFingerprint: "keep-managed",
		pluginNames: []string{"knowledge"}, contextPolicy: "keep-context", agentID: "default", audienceID: "owner"}
	before := o
	if err := selectNoCatalogChatProfile("chat", &o); err != nil {
		t.Fatal(err)
	}
	expectedFingerprint, err := nativeSkillSelectionFingerprint("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.home != home || o.nativeProfile != offloadCleanProfilePath(home, "default", "owner") || o.nativeSkillsFingerprint != expectedFingerprint {
		t.Fatal("full Host zero-Skill identity was not selected", o.home, o.nativeProfile, o.nativeSkillsFingerprint)
	}
	if o.runtimeFingerprint != before.runtimeFingerprint || o.workspace != before.workspace || o.defaultWorkspace != before.defaultWorkspace || o.scope != before.scope || o.defaultScope != before.defaultScope ||
		o.projectID != before.projectID || !reflect.DeepEqual(o.projectIDs, before.projectIDs) || o.primaryProject != before.primaryProject || o.hostInstructions != before.hostInstructions ||
		o.model != before.model || o.permissionMode != before.permissionMode || o.noTools != before.noTools || !reflect.DeepEqual(o.allowedTools, before.allowedTools) || !reflect.DeepEqual(o.mcpServers, before.mcpServers) ||
		o.knowledgeConfig != before.knowledgeConfig || !reflect.DeepEqual(o.knowledgeConfigs, before.knowledgeConfigs) || !reflect.DeepEqual(o.knowledgeNames, before.knowledgeNames) ||
		o.memoryOffloadConfig != before.memoryOffloadConfig || o.skillsRoot != before.skillsRoot || o.managedFingerprint != before.managedFingerprint || !reflect.DeepEqual(o.pluginNames, before.pluginNames) || o.contextPolicy != before.contextPolicy {
		t.Fatal("zero-Skill profile selection changed unrelated full Host options")
	}
	if body, err := os.ReadFile(filepath.Join(o.nativeProfile, "settings.json")); err != nil || string(body) != "{}\n" {
		t.Fatal("ambient settings entered the controlled profile", string(body), err)
	}
	if entries, err := os.ReadDir(filepath.Join(o.nativeProfile, "skills")); err != nil || len(entries) != 0 {
		t.Fatal("ambient Skills entered the controlled profile", entries, err)
	}
	if _, err := os.Stat(filepath.Join(o.nativeProfile, "history.json")); !os.IsNotExist(err) {
		t.Fatal("ambient history entered the controlled profile", err)
	}
	for _, skipped := range []struct {
		command string
		catalog string
	}{{"memory candidates", ""}, {"chat", "/agents.json"}} {
		candidate := before
		candidate.agentsConfig = skipped.catalog
		if err := selectNoCatalogChatProfile(skipped.command, &candidate); err != nil {
			t.Fatal(err)
		}
		if candidate.nativeProfile != before.nativeProfile || candidate.nativeSkillsFingerprint != before.nativeSkillsFingerprint {
			t.Fatal("profile selector escaped the no-catalog Chat branch", skipped)
		}
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
