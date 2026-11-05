package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err = os.Chmod(cache, 0700); err != nil {
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

func TestDreamRuntimeRejectsUnsupportedFullHostInsteadOfIgnoringConfig(t *testing.T) {
	for _, o := range []options{
		{dreamConfig: "/private/dream.json", fullHost: true},
		{dreamConfig: "/private/dream.json", agentsConfig: "/private/agents.json"},
		{dreamConfig: "/private/dream.json", memoryOffloadConfig: "/private/extractor.json"},
	} {
		if err := loadExecutionOptions(&o); err == nil || !strings.Contains(err.Error(), "Full Host") {
			t.Fatalf("unsupported runtime silently ignored dream config: %v", err)
		}
	}
}
