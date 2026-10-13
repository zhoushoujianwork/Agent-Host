package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/assembly"
)

func TestAgentContextLoadsExplicitRoleAndOwnerProfile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "agents", "ops")
	userDir := filepath.Join(root, "users", "owner")
	for _, path := range []string{dir, userDir, filepath.Join(root, "workspace")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"AGENTS.md": "Verify the service state.", "SOUL.md": "An operations assistant.", "IDENTITY.md": "Name: Ops"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	profile := "Use Chinese and explain the outcome first."
	userPath := filepath.Join(userDir, "USER.md")
	if err := os.WriteFile(userPath, []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "agents.json")
	catalog := agentCatalog{DefaultAgent: "ops", Agents: []agentDefinition{{ID: "ops", Name: "Operations", InstructionsDir: "agents/ops", UserFile: "users/owner/USER.md"}}}
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAgents(path)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := loaded.definition("ops")
	base := options{workspace: filepath.Join(root, "workspace"), nativeProfile: filepath.Join(root, "native")}
	owner, err := agent.options(base, "owner", false, false)
	if err != nil || owner.userProfile != profile || len(owner.contextFiles) != 4 || !strings.Contains(owner.hostInstructions, "An operations assistant.") {
		t.Fatalf("owner assembly: %+v %v", owner, err)
	}
	public, err := agent.options(base, "group-123", true, false)
	if err != nil || public.userProfile != "" || len(public.contextFiles) != 3 || strings.Contains(public.hostInstructions, profile) {
		t.Fatalf("private profile entered group assembly: %+v %v", public, err)
	}
	before, err := assembly.Assemble("", "", "default", owner.workspace, assemblyConfig(owner))
	if err != nil {
		t.Fatal(err)
	}
	owner.userProfile += " Use short answers."
	after, err := assembly.Assemble("", "", "default", owner.workspace, assemblyConfig(owner))
	if err != nil || before.Fingerprint == after.Fingerprint {
		t.Fatalf("changed explicit user profile retained compatible native history: %v", err)
	}
	unchanged, err := os.ReadFile(userPath)
	if err != nil || string(unchanged) != profile {
		t.Fatal("configuration was changed while loading")
	}
}

func TestAgentContextRejectsAmbiguousUnsafeOrOversizedSources(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "agent")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("role"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, a := range []agentDefinition{
		{InstructionsFile: path, InstructionsDir: dir},
		{InstructionsDir: "missing"},
		{UserFile: "agent/AGENTS.md"},
	} {
		if err := a.loadContextFiles(root); err == nil {
			t.Fatalf("accepted invalid source selection: %+v", a)
		}
	}
	for _, body := range []string{strings.Repeat("x", agentInstructionBudget+1), "bad\x00text", string([]byte{0xff})} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		a := agentDefinition{InstructionsDir: dir}
		if err := a.loadContextFiles(root); err == nil {
			t.Fatal("accepted invalid instruction body")
		}
	}
	if err := os.WriteFile(path, []byte("role"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	a := agentDefinition{InstructionsDir: dir}
	if err := a.loadContextFiles(root); err == nil {
		t.Fatal("accepted public instruction file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	a.InstructionsDir = alias
	if err := a.loadContextFiles(root); err == nil {
		t.Fatal("accepted symlink instruction directory")
	}
}
