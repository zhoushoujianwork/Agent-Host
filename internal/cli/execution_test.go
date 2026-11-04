package cli

import (
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/state"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func memorySelectorFixture(t *testing.T, backend, model string) string {
	t.Helper()
	dir := t.TempDir()
	native := filepath.Join(dir, "native.json")
	profile := map[string]any{"backend": backend, "model": model}
	if backend == "claude" {
		profile["base_url"] = "https://memory.example.test"
		profile["token"] = "synthetic-memory-token"
	}
	writeBotJSON(t, native, map[string]any{"profiles": map[string]any{"memory": profile}})
	selector := filepath.Join(dir, "memory-selector.json")
	writeBotJSON(t, selector, map[string]any{"version": 2, "config_path": native, "profile": "memory", "timeout": "1m"})
	return selector
}

func executionFixture(t *testing.T) (serviceLayout, serviceConfig, string) {
	t.Helper()
	layout, config := newServiceFixture(t)
	path := filepath.Join(layout.Config, "execution.offload.json")
	writeBotJSON(t, path, map[string]any{"version": 2, "config_path": filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "offload", "config.json"), "profile": "fixture", "timeout": "1m"})
	return layout, config, path
}

func TestDefaultExecutionUsesOffloadPackageWithoutSelector(t *testing.T) {
	for _, entry := range []string{"chat", "dingtalk serve", "feishu-meeting serve"} {
		o, _, err := parse(entry, []string{"--workspace", canonicalTemp(t)}, io.Discard)
		if err != nil || o.offloadClient == nil || !strings.HasPrefix(o.executionFingerprint, "offload:") {
			t.Fatalf("%s: %+v %v", entry, o, err)
		}
	}
}

func TestRemovedACPConfigurationIsRejected(t *testing.T) {
	_, _, path := executionFixture(t)
	if _, _, err := parse("chat", []string{"--execution-config", path}, io.Discard); err == nil {
		t.Fatalf("legacy engine selection accepted: %v", err)
	}
}

func TestCommonHostUsesOnlyPackageRunner(t *testing.T) {
	layout, config, _ := executionFixture(t)
	o, _, err := parse("chat", []string{"--claude", config.Claude, "--no-tools", "--workspace", layout.Workspace}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(layout.State, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	h := composeHost(store, o, client, nil)
	identity := harness.RunnerIdentity(h.Runner)
	if identity.Engine != "offload" || identity.Transport != "go-package" {
		t.Fatalf("non-package runner %+v", identity)
	}
}

func TestMemorySelectorKeepsBusinessProfileAndModelSeparate(t *testing.T) {
	selector := memorySelectorFixture(t, "claude", "memory-model")
	o, _, err := parse("chat", []string{"--workspace", canonicalTemp(t), "--model", "business-model", "--memory-offload-config", selector}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.model != "business-model" || o.memoryModel != "memory-model" || o.memoryOffloadClient == nil || o.memoryOffloadClient == o.offloadClient {
		t.Fatalf("business and memory execution were not separated: business=%q memory=%q", o.model, o.memoryModel)
	}
	if o.executionFingerprint == "" || o.memoryExecutionFingerprint == "" || o.executionFingerprint == o.memoryExecutionFingerprint {
		t.Fatal("business and memory selectors lack distinct fingerprints")
	}
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	h := composeHost(store, o, client, nil)
	if h.Config.Model != "business-model" || harness.RunnerIdentity(h.Runner).Transport != "go-package" {
		t.Fatal("internal selector changed displayed business execution", h.Config.Model, harness.RunnerIdentity(h.Runner))
	}
	for name, extractor := range map[string]any{"experience": h.Extractor, "objects": h.Objects} {
		value := reflect.ValueOf(extractor)
		if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().FieldByName("model").String() != "memory-model" {
			t.Fatalf("%s extractor did not receive the internal model", name)
		}
	}
}

func TestMemorySelectorDefaultCompatibilityAndExplicitErrors(t *testing.T) {
	o, _, err := parse("chat", []string{"--workspace", canonicalTemp(t)}, io.Discard)
	if err != nil || o.memoryOffloadClient != nil || o.memoryModel != "" || o.memoryExecutionFingerprint != "" {
		t.Fatal("omitted selector changed legacy runner semantics", err)
	}
	for _, tc := range []struct {
		name, path, want string
	}{
		{"unsupported backend", memorySelectorFixture(t, "codex", "codex-model"), "unsupported extraction backend"},
		{"missing selector", filepath.Join(t.TempDir(), "missing.json"), "existing regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parse("chat", []string{"--workspace", canonicalTemp(t), "--memory-offload-config", tc.path}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected selector error: %v (want %q)", err, tc.want)
			}
		})
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "version-one.json")
	writeBotJSON(t, bad, map[string]any{"version": 1, "profile": "legacy", "timeout": "1m"})
	if _, _, err := parse("chat", []string{"--workspace", canonicalTemp(t), "--memory-offload-config", bad}, io.Discard); err == nil || !strings.Contains(err.Error(), "only version 2") {
		t.Fatalf("unsupported selector version did not fail explicitly: %v", err)
	}
	native := filepath.Join(dir, "native.json")
	writeBotJSON(t, native, map[string]any{"profiles": map[string]any{"known": map[string]any{"backend": "claude", "model": "known-model"}}})
	unknown := filepath.Join(dir, "unknown-profile.json")
	writeBotJSON(t, unknown, map[string]any{"version": 2, "config_path": native, "profile": "missing", "timeout": "1m"})
	if _, _, err := parse("chat", []string{"--workspace", canonicalTemp(t), "--memory-offload-config", unknown}, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot load the explicitly selected Offload configuration/profile") {
		t.Fatalf("unknown selector profile did not fail explicitly: %v", err)
	}
}

func TestAgentMemorySelectorOverrideIsIndependentlyAdmitted(t *testing.T) {
	baseSelector := memorySelectorFixture(t, "claude", "base-memory")
	agentSelector := memorySelectorFixture(t, "claude", "agent-memory")
	base, _, err := parse("chat", []string{"--workspace", canonicalTemp(t), "--model", "business-model", "--memory-offload-config", baseSelector}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := (agentDefinition{ID: "special", Name: "Special", MemoryOffloadConfig: agentSelector}).options(base, state.OwnerAudienceID, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if selected.model != "business-model" || selected.memoryModel != "agent-memory" || selected.memoryExecutionFingerprint == base.memoryExecutionFingerprint {
		t.Fatalf("agent memory override crossed business execution: business=%q memory=%q", selected.model, selected.memoryModel)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	_, err = (agentDefinition{ID: "broken", Name: "Broken", MemoryOffloadConfig: missing}).options(base, state.OwnerAudienceID, false, false)
	if err == nil || !strings.Contains(err.Error(), "agent broken: memory offload configuration") {
		t.Fatalf("agent selector admission did not identify the source: %v", err)
	}
	unsupported := memorySelectorFixture(t, "codex", "changed-memory")
	raw, err := os.ReadFile(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseSelector, raw, 0600); err != nil {
		t.Fatal(err)
	}
	base.offloadConfig = "already-resolved-business-selector"
	if _, err := (agentDefinition{ID: "inherited", Name: "Inherited"}).options(base, state.OwnerAudienceID, false, false); err == nil || !strings.Contains(err.Error(), "agent inherited: memory offload configuration") {
		t.Fatalf("inherited selector was not revalidated at Agent admission: %v", err)
	}
}

func TestAgentConfigResolvesRelativeMemorySelector(t *testing.T) {
	selector := memorySelectorFixture(t, "claude", "agent-file-memory")
	dir := t.TempDir()
	localSelector := filepath.Join(dir, "memory.json")
	raw, err := os.ReadFile(selector)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localSelector, raw, 0600); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(dir, "agents.json")
	writeBotJSON(t, agentsPath, map[string]any{"default_agent": "default", "agents": []map[string]any{{"id": "default", "name": "Default", "memory_offload_config": "memory.json"}}})
	catalog, err := loadAgents(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Agents[0].MemoryOffloadConfig != localSelector {
		t.Fatal("relative Agent memory selector was not resolved", catalog.Agents[0].MemoryOffloadConfig)
	}
	selected, err := catalog.Agents[0].options(options{model: "business-model"}, state.OwnerAudienceID, false, false)
	if err != nil || selected.memoryModel != "agent-file-memory" || selected.model != "business-model" {
		t.Fatal("resolved Agent selector was not independently admitted", selected.memoryModel, selected.model, err)
	}
}
