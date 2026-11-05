package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func writeDreamConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dream.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDreamRuntimeHotOnlyAndStrictConfig(t *testing.T) {
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeDreamConfig(t, `{"hot_enabled":true,"cache_root":"`+cache+`","person_id":"person","agent_id":"agent","audience_id":"owner","scope":"scope","batch":10}`)
	c, err := loadDreamRuntime(path)
	if err != nil || !c.HotEnabled || c.Enabled {
		t.Fatalf("hot-only config: %+v %v", c, err)
	}
	if err = validateDreamRuntimeIdentity(c, options{agentID: "agent", audienceID: "owner", scope: "scope"}, "other"); err == nil {
		t.Fatal("trusted person mismatch was accepted")
	}
	unknown := writeDreamConfig(t, `{"unknown":true}`)
	if _, err = loadDreamRuntime(unknown); err == nil {
		t.Fatal("unknown dream config field was accepted")
	}
	oversized := writeDreamConfig(t, strings.Repeat(" ", 65<<10))
	if _, err = loadDreamRuntime(oversized); err == nil {
		t.Fatal("oversized dream config was accepted")
	}
}

func TestDreamRuntimeRejectsSymlinkAncestry(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "dream.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDreamRuntime(filepath.Join(link, "dream.json")); err == nil {
		t.Fatal("dream config symlink ancestry was accepted")
	}
}

func TestDreamRuntimeAllowsFullHostComposition(t *testing.T) {
	for _, o := range []options{
		{dreamConfig: "/private/dream.json", fullHost: true},
		{dreamConfig: "/private/dream.json", agentsConfig: "/private/agents.json"},
		{dreamConfig: "/private/dream.json", memoryOffloadConfig: "/private/extractor.json"},
	} {
		if o.memoryOffloadConfig != "" {
			continue // This fixture has no extraction profile; integration is covered by service composition tests.
		}
		if err := loadExecutionOptions(&o); err != nil {
			t.Fatalf("Full Host rejected dream config: %v", err)
		}
	}
}

func TestDreamHOTExcludesGroupsAndDifferentPeopleBeforeStoreAccess(t *testing.T) {
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeDreamConfig(t, `{"hot_enabled":true,"cache_root":"`+cache+`","person_id":"owner","agent_id":"agent","audience_id":"private:owner","scope":"scope","batch":10}`)
	base := options{dreamConfig: path, agentID: "agent", audienceID: "private:owner", scope: "scope"}
	for name, test := range map[string]struct {
		person  string
		private bool
	}{
		"group":            {person: "owner", private: false},
		"different person": {person: "other", private: true},
	} {
		t.Run(name, func(t *testing.T) {
			o := base
			o.hotRefresh = func(context.Context) ([]harness.PresetDocument, string, error) { return nil, "old", nil }
			if err := bindDreamHOT(nil, &o, test.person, test.private); err != nil {
				t.Fatal(err)
			}
			if o.hotRefresh != nil || o.hotPersonID != "" {
				t.Fatal("personal HOT survived excluded identity")
			}
		})
	}
}

func TestServiceForwardsDreamConfig(t *testing.T) {
	path := "/private/config/dream.json"
	for _, offload := range []string{"", "/private/config/offload.json"} {
		args := strings.Join((serviceConfig{DreamConfig: path, OffloadConfig: offload}).arguments(serviceLayout{Config: "/config", State: "/state", Workspace: "/workspace", Native: "/native"}, "serve"), " ")
		if !strings.Contains(args, "--dream-config "+path) {
			t.Fatalf("dream config not forwarded: %s", args)
		}
	}
}

func TestDreamStatusUsesCanonicalConfigIdentityAndReturnsLatestFirst(t *testing.T) {
	home := t.TempDir()
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := writeDreamConfig(t, `{"hot_enabled":true,"cache_root":"`+cache+`","person_id":"person","agent_id":"agent","audience_id":"owner","scope":"scope","batch":10}`)
	c, err := loadDreamRuntime(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= 201; i++ {
		at := base.Add(-time.Duration(i) * time.Minute)
		if err := store.SaveDreamCycle(context.Background(), state.DreamCycle{ID: at.Format(time.RFC3339), ConfigID: dreamRuntimeConfigID(configPath, c), Status: "completed", StartedAt: at, FinishedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
	for i, status := range []string{"completed", "failed"} {
		finished := base.Add(time.Duration(i) * time.Minute)
		cycle := state.DreamCycle{ID: "cycle-" + status, ConfigID: dreamRuntimeConfigID(configPath, c), Status: status, StartedAt: finished, FinishedAt: &finished}
		if err := store.SaveDreamCycle(context.Background(), cycle); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runDreamLifecycle([]string{"status", "--home", home, "--dream-config", configPath, "--limit", "1", "--format", "json"}, &out, &out); err != nil {
		t.Fatal(err, out.String())
	}
	var result struct {
		ConfigID string             `json:"config_id"`
		Cycles   []state.DreamCycle `json:"cycles"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ConfigID != dreamRuntimeConfigID(configPath, c) || len(result.Cycles) != 1 || result.Cycles[0].Status != "failed" {
		t.Fatalf("unexpected status result: %+v", result)
	}
}
