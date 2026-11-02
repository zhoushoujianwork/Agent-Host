//go:build unix

package offloadchannel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryProfileDefaultsAndDisabledFingerprintCompatibility(t *testing.T) {
	base := profileBase(t)
	_, disabled, err := Parse([]byte(`{` + base + `}`))
	if err != nil {
		t.Fatal(err)
	}
	active, fp, err := Parse([]byte(`{` + base + `,"memory":{"enabled":true}}`))
	if err != nil || !active.MemoryEnabled || !active.Memory.Processing || active.Memory.Provider != "typesafe" || fp == disabled {
		t.Fatalf("enabled defaults: %+v %v", active, err)
	}
	readOnly, roFP, err := Parse([]byte(`{` + base + `,"memory":{"enabled":true,"processing_enabled":false}}`))
	if err != nil || readOnly.Memory.Processing || roFP == fp {
		t.Fatalf("read-only mode not fingerprinted: %+v %v", readOnly, err)
	}
}

func writeProfile(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "offload.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func executable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "offload")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAcceptsExplicitProfileAndDefaultsMemoryOff(t *testing.T) {
	body := `{` + profileBase(t) + `,"model":"m","memory":{"enabled":false}}`
	p, fp, err := Load(writeProfile(t, 0o600, body))
	if err != nil || p.Profile != "work" || p.MemoryEnabled || !strings.HasPrefix(fp, FingerprintPrefix) {
		t.Fatalf("%+v %q %v", p, fp, err)
	}

	other := strings.Replace(body, `"work"`, `"other"`, 1)
	if _, fp2, err := Parse([]byte(other)); err != nil || fp2 == fp {
		t.Fatalf("fingerprint ignores profile: %v", err)
	}
	if _, _, err := Parse([]byte(strings.Replace(body, `,"memory":{"enabled":false}`, "", 1))); err != nil {
		t.Fatalf("memory is optional: %v", err)
	}
}

func TestLoadRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	cmd := executable(t)
	ok := profileBase(t)
	for name, body := range map[string]string{
		"memory provider": `{` + ok + `,"memory":{"enabled":true,"provider":"unknown"}}`,
		"memory implicit": `{` + ok + `,"memory":{}}`,
		"unknown field":   `{` + ok + `,"tools":["x"]}`,
		"trailing":        `{` + ok + `} {}`,
		"duplicate":       `{` + ok + `,"profile":"x"}`,
		"version":         `{"version":2,"command":"` + cmd + `","profile":"work","timeout":"1m"}`,
		"relative":        `{"version":1,"command":"offload","profile":"work","timeout":"1m"}`,
		"missing command": `{"version":1,"command":"/nonexistent/offload","profile":"work","timeout":"1m"}`,
		"empty profile":   `{"version":1,"command":"` + cmd + `","profile":"","timeout":"1m"}`,
		"no timeout":      `{"version":1,"command":"` + cmd + `","profile":"work"}`,
		"zero timeout":    `{"version":1,"command":"` + cmd + `","profile":"work","timeout":"0s"}`,
		"null":            `{` + ok + `,"model":null}`,
	} {
		if _, _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	notExecutable := filepath.Join(t.TempDir(), "offload")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Parse([]byte(`{"version":1,"command":"` + notExecutable + `","profile":"w","timeout":"1m"}`)); err == nil {
		t.Error("non-executable command accepted")
	}
	if _, _, err := Load(writeProfile(t, 0o644, `{`+ok+`}`)); err == nil {
		t.Error("group-readable file accepted")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(writeProfile(t, 0o600, `{`+ok+`}`), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(link); err == nil {
		t.Error("symlink accepted")
	}
	if _, _, err := Load("relative.json"); err == nil {
		t.Error("relative path accepted")
	}
}

func TestLibraryProfileSelectsExplicitTransportAndChangesIdentity(t *testing.T) {
	dir := t.TempDir()
	native := filepath.Join(dir, "native.json")
	if err := os.WriteFile(native, []byte(`{"profiles":{"canonical":{"backend":"codex","aliases":["alias"],"model":"native-model"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	body := `{"version":2,"config_path":"` + native + `","profile":"alias","timeout":"1m"}`
	p, fp, err := Parse([]byte(body))
	if err != nil || p.Version != 2 || p.ConfigPath != native {
		t.Fatalf("%+v %v", p, err)
	}
	client, err := p.NewClient()
	if err != nil || client.(interface{ Transport() string }).Transport() != "go-package" {
		t.Fatalf("library selection %v", err)
	}
	_, repeated, err := Parse([]byte(body))
	if err != nil || repeated != fp {
		t.Fatal("unchanged SDK profile changed runtime identity")
	}
	for _, invalid := range []string{
		strings.Replace(body, `"version":2`, `"version":1`, 1),
		strings.Replace(body, `"version":2`, `"version":2,"command":"`+executable(t)+`"`, 1),
		strings.Replace(body, `"config_path":"`+native+`",`, "", 1),
	} {
		if _, _, err := Parse([]byte(invalid)); err == nil {
			t.Fatal("mixed or missing transport configuration accepted")
		}
	}
	raw, _ := os.ReadFile(native)
	os.WriteFile(native, []byte(strings.Replace(string(raw), "native-model", "changed-model", 1)), 0600)
	_, changed, err := Parse([]byte(body))
	if err != nil || changed == fp {
		t.Fatal("changed native configuration reused the old runtime identity")
	}
}

func profileBase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "native.json")
	if err := os.WriteFile(path, []byte(`{"profiles":{"work":{"backend":"codex","model":"fixture"},"other":{"backend":"codex","model":"other"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return `"version":2,"config_path":"` + path + `","profile":"work","timeout":"1m"`
}
