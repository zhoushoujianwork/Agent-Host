//go:build unix

package offloadchannel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryProfileDefaultsAndDisabledFingerprintCompatibility(t *testing.T) {
	cmd := executable(t)
	base := `"version":1,"command":"` + cmd + `","profile":"work","timeout":"1m"`
	p, disabled, err := Parse([]byte(`{` + base + `}`))
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Command string `json:"command"`
		Profile string `json:"profile"`
		Model   string `json:"model"`
		Timeout string `json:"timeout"`
		Memory  bool   `json:"memory"`
	}{p.Version, p.Command, p.Profile, p.Model, p.Timeout.String(), false})
	sum := sha256.Sum256(legacy)
	if disabled != FingerprintPrefix+hex.EncodeToString(sum[:]) {
		t.Fatal("disabled memory changed existing native-session fingerprints")
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
	cmd := executable(t)
	body := `{"version":1,"command":"` + cmd + `","profile":"work","model":"m","timeout":"30m","memory":{"enabled":false}}`
	p, fp, err := Load(writeProfile(t, 0o600, body))
	if err != nil || p.Profile != "work" || p.MemoryEnabled || !strings.HasPrefix(fp, FingerprintPrefix) {
		t.Fatalf("%+v %q %v", p, fp, err)
	}
	if cfg := p.ClientConfig(); cfg.Profile != "work" || cfg.Model != "m" || cfg.Timeout.Minutes() != 30 {
		t.Fatalf("%+v", cfg)
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
	ok := `"version":1,"command":"` + cmd + `","profile":"work","timeout":"1m"`
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
