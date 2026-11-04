package cli

import (
	"agent-host/internal/offloadexec"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentResolvesCurrentOffloadDefaultForEachRequest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	directory := filepath.Join(root, "offload")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "config.json")
	write := func(selected string) {
		t.Helper()
		body := `{"default":"` + selected + `","profiles":{"first":{"backend":"codex","model":"first-model"},"second":{"backend":"codex","model":"second-model"}}}`
		if err := os.WriteFile(config, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	library, err := offloadexec.DefaultLibrary("", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base := options{offloadClient: library, model: "stale-model", timeout: time.Minute, nativeProfile: root}
	agent := agentDefinition{ID: "default", Name: "Default"}
	first, err := agent.options(base, "owner", false, false)
	if err != nil {
		t.Fatal(err)
	}
	write("second")
	second, err := agent.options(base, "owner", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.model != "first-model" || second.model != "second-model" || first.executionFingerprint == second.executionFingerprint {
		t.Fatalf("first=%s second=%s fingerprints=%s/%s", first.model, second.model, first.executionFingerprint, second.executionFingerprint)
	}
	agent.Model = "explicit-model"
	overridden, err := agent.options(base, "owner", false, false)
	if err != nil || overridden.model != "explicit-model" {
		t.Fatalf("%s %v", overridden.model, err)
	}
	agent.Model = ""
	base.modelExplicit, base.model = true, "command-model"
	command, err := agent.options(base, "owner", false, false)
	if err != nil || command.model != "command-model" {
		t.Fatalf("%s %v", command.model, err)
	}
}
