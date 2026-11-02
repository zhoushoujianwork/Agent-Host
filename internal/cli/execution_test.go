package cli

import (
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/state"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
