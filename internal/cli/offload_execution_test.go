package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/offloadchannel"
)

func offloadProfileFixture(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "offload-profile.json")
	writeBotJSON(t, path, map[string]any{"version": 2, "config_path": filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "offload", "config.json"), "profile": "fixture", "timeout": "5m"})
	return path
}

func TestOffloadExecutionSelectorLoadsProfile(t *testing.T) {
	path := offloadProfileFixture(t)
	for _, entry := range []string{"dingtalk serve", "feishu-meeting serve"} {
		o, _, err := parse(entry, []string{"--offload-config", path, "--workspace", t.TempDir()}, io.Discard)
		if err != nil {
			t.Fatalf("%s: %v", entry, err)
		}
		if o.offloadProfile == nil || o.offloadClient == nil {
			t.Fatalf("%s: %+v", entry, o)
		}
		if !strings.HasPrefix(o.executionFingerprint, offloadchannel.FingerprintPrefix) || o.executionFingerprint != o.offloadProfile.Fingerprint() {
			t.Fatalf("%s fingerprint %q", entry, o.executionFingerprint)
		}
	}
}

func TestOffloadExecutionRejectsUnwiredEntriesAndLegacyOverrides(t *testing.T) {
	path := offloadProfileFixture(t)
	if _, _, err := parse("run retry-experience", []string{"--offload-config", path}, io.Discard); err == nil {
		t.Fatal("experience retry silently accepted an unwired execution engine")
	}
	for _, flag := range []string{"--model", "--claude", "--native-profile", "--jev-provider", "--jev-model", "--jev-endpoint"} {
		if _, _, err := parse("dingtalk check", []string{"--offload-config", path, flag, "ignored"}, io.Discard); err == nil {
			t.Fatalf("silently ignored %s", flag)
		}
	}
}

func TestOffloadDefaultSelectionIsNotACompatibilityTransport(t *testing.T) {
	o, _, err := parse("chat", []string{"--workspace", canonicalTemp(t)}, io.Discard)
	if err != nil || o.offloadClient == nil || o.offloadProfile == nil || o.offloadProfile.Version != 2 {
		t.Fatalf("default selection: %+v %v", o, err)
	}
}

func TestOffloadExecutionRejectsInvalidProfile(t *testing.T) {
	good := offloadProfileFixture(t)
	dir := filepath.Dir(good)
	raw, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing":          filepath.Join(dir, "absent.json"),
		"relative":         "offload-profile.json",
		"unknown field":    write(t, dir, "unknown.json", strings.Replace(string(raw), `"version":2`, `"version":2,"tools":["x"]`, 1), 0o600),
		"memory provider":  write(t, dir, "memory.json", strings.Replace(string(raw), `"version":2`, `"version":2,"memory":{"enabled":true,"provider":"unknown"}`, 1), 0o600),
		"group accessible": write(t, dir, "open.json", string(raw), 0o644),
	}
	for name, path := range cases {
		o := options{offloadConfig: path}
		if err := loadExecutionOptions(&o); err == nil || o.offloadProfile != nil || o.offloadClient != nil || o.executionFingerprint != "" {
			t.Errorf("%s: accepted (%+v, %v)", name, o, err)
		}
		if _, _, err := parse("dingtalk serve", []string{"--offload-config", path}, io.Discard); err == nil {
			t.Errorf("%s: parse accepted", name)
		}
	}
}

func write(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
