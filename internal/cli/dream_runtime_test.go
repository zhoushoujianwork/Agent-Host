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

func TestDreamRuntimeIdleDefaultsAndActivityRoots(t *testing.T) {
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	profileDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(profileDir, "extractor.json")
	if err = os.WriteFile(profile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	activityHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	activityStore, err := state.Open(activityHome, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = activityStore.Close(); err != nil {
		t.Fatal(err)
	}
	path := writeDreamConfig(t, `{"enabled":true,"source_root":"`+source+`","memory_offload_config":"`+profile+`","person_id":"person","agent_id":"agent","audience_id":"owner","scope":"scope","interval":"2m","timeout":"1m","batch":10,"activity_state_homes":["`+activityHome+`"]}`)
	c, err := loadDreamRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.IdleFor != "60s" || c.ActivityPollInterval != "1s" || len(c.ActivityStateHomes) != 1 {
		t.Fatalf("idle defaults/config lost: %+v", c)
	}
	bad := writeDreamConfig(t, `{"enabled":true,"source_root":"`+source+`","memory_offload_config":"`+profile+`","person_id":"person","agent_id":"agent","audience_id":"owner","scope":"scope","interval":"2m","timeout":"1m","batch":10,"activity_state_homes":["relative"]}`)
	if _, err = loadDreamRuntime(bad); err == nil {
		t.Fatal("relative activity state home was accepted")
	}
}

func TestDreamSkillDraftOptInRequiresValidatedOwnerToken(t *testing.T) {
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	profileRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(profileRoot, "extractor.json")
	if err = os.WriteFile(profile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	skills, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(skills, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeDreamConfig(t, `{"enabled":true,"draft_skills":true,"skills_root":"`+skills+`","source_root":"`+source+`","memory_offload_config":"`+profile+`","person_id":"owner","agent_id":"agent","audience_id":"owner","scope":"scope","interval":"2m","timeout":"1m","batch":10}`)
	c, err := loadDreamRuntime(path)
	if err != nil || !c.DraftSkills || c.SkillsRoot != skills {
		t.Fatalf("draft opt-in did not load: %+v %v", c, err)
	}
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := options{dreamConfig: path, agentID: "agent", audienceID: state.OwnerAudienceID, scope: "scope"}
	if _, _, err = startDreamBackground(context.Background(), store, base); err == nil || !strings.Contains(err.Error(), "authenticated channel Dream partition") {
		t.Fatal("legacy runtime bypassed automatic draft trust token", err)
	}
	wrong := base
	wrongPartition := state.DreamPartition{PersonID: "owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "other"}
	wrong.dreamValidatedPartition = &wrongPartition
	if _, _, err = startDreamBackground(context.Background(), store, wrong); err == nil || !strings.Contains(err.Error(), "matching authenticated") {
		t.Fatal("mismatched trust token started automatic drafts", err)
	}
	agents := agentCatalog{DefaultAgent: "agent", Agents: []agentDefinition{{ID: "agent", Name: "Agent"}}, ownerID: "owner"}
	validated := base
	if err = validateFullHostChannelDream(context.Background(), store, &validated, agents, "owner", "dingtalk", "app", "tenant"); err != nil {
		t.Fatal(err)
	}
	want := state.DreamPartition{PersonID: "owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	if validated.dreamValidatedPartition == nil || *validated.dreamValidatedPartition != want {
		t.Fatalf("validated token mismatch: %+v", validated.dreamValidatedPartition)
	}
	if err = validateFullHostChannelDream(context.Background(), store, &validated, agents, "other", "dingtalk", "app", "tenant"); err == nil || validated.dreamValidatedPartition != nil {
		t.Fatal("failed validation retained prior trust token", err)
	}
}

func TestDreamSkillDraftOptInRejectsNonOwnerAudience(t *testing.T) {
	source, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	profileRoot, _ := filepath.EvalSymlinks(t.TempDir())
	profile := filepath.Join(profileRoot, "extractor.json")
	if err := os.WriteFile(profile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	skills, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chmod(skills, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeDreamConfig(t, `{"enabled":true,"draft_skills":true,"skills_root":"`+skills+`","source_root":"`+source+`","memory_offload_config":"`+profile+`","person_id":"member","agent_id":"agent","audience_id":"group","scope":"scope","interval":"2m","timeout":"1m","batch":10}`)
	if _, err := loadDreamRuntime(path); err == nil || !strings.Contains(err.Error(), "owner audience") {
		t.Fatal("group/nonowner automatic draft config was accepted", err)
	}
}

func TestDreamActivityProbeAggregatesRootsAndFailsClosed(t *testing.T) {
	local, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close() })
	otherHome := t.TempDir()
	other, err := state.Open(otherHome, false)
	if err != nil {
		t.Fatal(err)
	}
	partition := other.WithPartition("other", "other")
	session := state.Session{ID: "activity-session", AgentID: "other", AudienceID: "other", Scope: "scope", NativeSessionID: "native"}
	if err = partition.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)
	run := state.Run{ID: "activity-run", SessionID: session.ID, AgentID: session.AgentID, AudienceID: session.AudienceID, Scope: session.Scope, Input: "foreground", NativeSessionID: session.NativeSessionID, ExecutionStatus: "running", MemoryStatus: "blocked", CreatedAt: created}
	if err = partition.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	activity, err := dreamActivityProbe(local, []string{otherHome})(context.Background())
	if err != nil || !activity.Busy || !activity.LastForeground.Equal(created) {
		t.Fatalf("aggregated activity=%+v err=%v", activity, err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err = dreamActivityProbe(local, []string{missing})(context.Background()); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatal("missing explicit activity root did not fail closed", err)
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
