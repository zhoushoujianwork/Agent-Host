package businessruntime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func directory(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixture(t *testing.T) (Policy, string) {
	t.Helper()
	source := filepath.Join(directory(t), "source")
	if err := os.WriteFile(source, []byte("synthetic-credential-a"), 0600); err != nil {
		t.Fatal(err)
	}
	root := directory(t)
	if err := os.Mkdir(filepath.Join(root, "home"), 0700); err != nil {
		t.Fatal(err)
	}
	return Policy{HomeFiles: []HomeFile{{Source: source, Target: ".tccli/test.credential"}}}, root
}

func TestOmittedPolicyPreservesPreviousProfileContract(t *testing.T) {
	profile := directory(t)
	alias := filepath.Join(directory(t), "profile-alias")
	if err := os.Symlink(profile, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", profile, alias, filepath.Join(profile, "absent")} {
		p, err := Load(path)
		if err != nil || !p.Empty() || p.ID() != "" || p.Guidance() != "" {
			t.Fatal("omitted policy added profile constraints", err)
		}
	}
	if _, err := Load(filepath.Join(profile, "business-sets", "missing-policy")); err == nil {
		t.Fatal("selected policy disappeared silently")
	}
}

func TestRefreshOwnershipAndNoCredentialIdentity(t *testing.T) {
	p, root := fixture(t)
	profile := directory(t)
	if err := p.Prepare(profile); err != nil {
		t.Fatal(err)
	}
	id := p.ID()
	if err := p.Apply(root); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(profile)
	if err != nil || saved.ID() != id {
		t.Fatal("manifest roundtrip failed", err)
	}
	if err := os.WriteFile(p.HomeFiles[0].Source, []byte("synthetic-credential-b"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(root); err != nil {
		t.Fatal(err)
	}
	if p.ID() != id {
		t.Fatal("credential content affected policy identity")
	}
	copy, err := os.ReadFile(filepath.Join(root, "home", p.HomeFiles[0].Target))
	if err != nil || string(copy) != "synthetic-credential-b" {
		t.Fatal("rotation failed", err)
	}
	info, _ := os.Stat(filepath.Join(root, "home", p.HomeFiles[0].Target))
	if info.Mode().Perm() != 0600 {
		t.Fatal("copy is not private")
	}
	for _, path := range []string{filepath.Join(profile, Manifest), filepath.Join(root, ownership)} {
		raw, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(raw), "synthetic-credential") {
			t.Fatal("secret in policy", err)
		}
	}
	if strings.Contains(p.Guidance(), "synthetic-credential") {
		t.Fatal("secret in guidance")
	}
	other := p
	other.HomeFiles = []HomeFile{{Source: p.HomeFiles[0].Source, Target: "other"}}
	if other.Apply(root) == nil || (Policy{}).Apply(root) == nil {
		t.Fatal("foreign or removed policy reused owned environment")
	}
	if err := os.Remove(p.HomeFiles[0].Source); err != nil {
		t.Fatal(err)
	}
	if p.Apply(root) == nil {
		t.Fatal("missing rotated source accepted")
	}
}

func TestBuildHubExactBusinessLeafDoesNotImportNativeConfiguration(t *testing.T) {
	p, root := fixture(t)
	sourceRoot := filepath.Dir(p.HomeFiles[0].Source)
	if err := os.MkdirAll(filepath.Join(sourceRoot, "skills", "unselected"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"settings.json", "CLAUDE.md", "skills/unselected/SKILL.md"} {
		if err := os.WriteFile(filepath.Join(sourceRoot, path), []byte("unselected native fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p.HomeFiles[0].Target = ".claude/build-hub/token"
	if err := p.Apply(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "home", ".claude", "build-hub", "token")
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "synthetic-credential-a" {
		t.Fatal("business token unavailable", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("business token is not private", err)
	}
	for _, path := range []string{filepath.Join(root, "home", ".claude"), filepath.Dir(target)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("business parents are not private", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "home", ".claude"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "build-hub" {
		t.Fatal("native configuration imported", err)
	}
	entries, err = os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 || entries[0].Name() != "token" {
		t.Fatal("business directory imported", err)
	}
	if err := os.WriteFile(p.HomeFiles[0].Source, []byte("rotated-build-hub-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(root); err != nil {
		t.Fatal("business leaf refresh failed", err)
	}
}

func TestRejectUnsafePolicies(t *testing.T) {
	p, _ := fixture(t)
	for _, target := range []string{"", ".", "../outside", "/absolute", "a/../b", "a//b", ".claude/settings.json", ".claude/skills/x/SKILL.md", ".claude/build-hub/other", ".claude/build-hub", ".claude/build-hub/token/child", ".Claude/build-hub/token", ".claude/Build-Hub/token", ".claude/build-hub/Token", ".claude/build-hub/./token", ".claude//build-hub/token", ".codex/auth.json", ".agents/x", "AGENTS.md", "a/CLAUDE.md", "skills/x", ".zshenv", ".zprofile", ".profile", ".bash_profile", ".bashrc", "a\\b"} {
		t.Run(target, func(t *testing.T) {
			q := p
			q.HomeFiles = []HomeFile{{Source: p.HomeFiles[0].Source, Target: target}}
			if q.Validate() == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
	for _, pair := range [][2]string{{"a", "a"}, {"a", "a/b"}, {"a/b", "a"}} {
		q := p
		q.HomeFiles = []HomeFile{{p.HomeFiles[0].Source, pair[0]}, {p.HomeFiles[0].Source, pair[1]}}
		if q.Validate() == nil {
			t.Fatal("overlap accepted")
		}
	}
	link := filepath.Join(directory(t), "link")
	if err := os.Symlink(p.HomeFiles[0].Source, link); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{link, filepath.Dir(link), filepath.Join(filepath.Dir(link), "missing")} {
		q := Policy{HomeFiles: []HomeFile{{source, "credential"}}}
		if q.Validate() == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if err := os.WriteFile(p.HomeFiles[0].Source, make([]byte, maxFile+1), 0600); err != nil {
		t.Fatal(err)
	}
	if p.Validate() == nil {
		t.Fatal("oversized source accepted")
	}
	if (Policy{HomeFiles: make([]HomeFile, 17)}).Validate() == nil {
		t.Fatal("unbounded bindings accepted")
	}
}

func TestDestinationAndManifestConflicts(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "parent-link", "startup", "foreign-policy"} {
		t.Run(kind, func(t *testing.T) {
			p, root := fixture(t)
			home := filepath.Join(root, "home")
			p.BinDirectory = directory(t)
			sourceBefore, _ := os.ReadFile(p.HomeFiles[0].Source)
			switch kind {
			case "file":
				os.Mkdir(filepath.Join(home, ".tccli"), 0700)
				os.WriteFile(filepath.Join(home, p.HomeFiles[0].Target), []byte("foreign"), 0600)
			case "symlink":
				os.Mkdir(filepath.Join(home, ".tccli"), 0700)
				os.Symlink(p.HomeFiles[0].Source, filepath.Join(home, p.HomeFiles[0].Target))
			case "parent-link":
				os.Symlink(filepath.Dir(p.HomeFiles[0].Source), filepath.Join(home, ".tccli"))
			case "startup":
				os.WriteFile(filepath.Join(home, ".zshenv"), []byte("foreign startup"), 0600)
			case "foreign-policy":
				os.WriteFile(filepath.Join(root, ownership), []byte(`{}`), 0600)
			}
			if p.Apply(root) == nil {
				t.Fatal("conflict accepted")
			}
			after, _ := os.ReadFile(p.HomeFiles[0].Source)
			if string(after) != string(sourceBefore) {
				t.Fatal("source changed")
			}
		})
	}
	p, _ := fixture(t)
	profile := directory(t)
	if err := os.Symlink(p.HomeFiles[0].Source, filepath.Join(profile, Manifest)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(profile); err == nil {
		t.Fatal("linked manifest accepted")
	}
}

func TestRealIsolatedShellToolsAndFiles(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	p, root := fixture(t)
	p.BinDirectory = filepath.Join(directory(t), "tools' quoted")
	if err := os.Mkdir(p.BinDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	tool := []byte("#!/bin/sh\n[ \"$HOME\" = \"$EXPECTED_HOME\" ] || exit 21\n[ -z \"${POP_TOKEN-}\" ] || exit 22\n[ ! -e \"$HOME/.claude\" ] || exit 23\ncat \"$HOME/.tccli/test.credential\"\n")
	if err := os.WriteFile(filepath.Join(p.BinDirectory, "fixture-business"), tool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(root); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(root); err != nil {
		t.Fatal("startup is not idempotent", err)
	}
	for _, shell := range [][]string{{zsh, "-c"}, {zsh, "-lc"}, {"/bin/bash", "-lc"}} {
		cmd := exec.Command(shell[0], append(shell[1:], "fixture-business")...)
		cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "EXPECTED_HOME=" + filepath.Join(root, "home"), "PATH=/usr/bin:/bin"}
		raw, err := cmd.CombinedOutput()
		if err != nil || string(raw) != "synthetic-credential-a" {
			t.Fatalf("shell tool fixture failed: %v %s", err, raw)
		}
	}
	// Order is canonical and the manifest contains policy paths only.
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "synthetic-credential") {
		t.Fatal("secret in configuration")
	}
}
