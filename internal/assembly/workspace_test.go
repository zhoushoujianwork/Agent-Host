package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func contextFixture(t *testing.T) (string, WorkspaceInput) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := WorkspaceInput{Run: state.Run{ID: "run-one", AgentID: "default", AudienceID: "owner", Scope: "project:api", Workspace: root, CreatedAt: time.Now().UTC()}, Files: []ContextFile{{Name: "AGENTS.md", Source: "user-managed-agent", Content: "Inspect the actual results."}, {Name: "USER.md", Source: "user-managed-profile", Content: "Use Chinese."}}, UserProfile: "Use Chinese.", Memories: []state.Memory{{ID: "memory-one", AgentID: "default", AudienceID: "owner", Scope: "project:api", Status: "active", Kind: "preference", Text: "Prefer short answers.", Revision: 2, SourceRunID: "old-run", SourceType: "user_statement"}}}
	return filepath.Join(root, "cache"), input
}

func TestTaskWorkspacePreservesSourcesAndDetectsModifiedInputs(t *testing.T) {
	cache, input := contextFixture(t)
	view, err := MaterializeWorkspace(cache, input)
	if err != nil {
		t.Fatal(err)
	}
	if view.Path != filepath.Join(cache, "workspaces", input.Run.ID) || view.Manifest.ExecutionWorkspace != input.Run.Workspace || len(view.Manifest.Memories) != 1 || view.Manifest.Memories[0].Revision != 2 {
		t.Fatalf("task input identities: %+v", view)
	}
	user, err := os.ReadFile(filepath.Join(view.Path, "USER.md"))
	if err != nil || !strings.Contains(string(user), "Use Chinese.") || !strings.Contains(string(user), "Prefer short answers.") {
		t.Fatalf("missing explicit or learned preference view: %s %v", user, err)
	}
	if _, err := MaterializeWorkspace(cache, input); err == nil {
		t.Fatal("immutable task context was overwritten")
	}
	if err := os.Mkdir(filepath.Join(view.Path, "native-skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWorkspace(view); err != nil {
		t.Fatalf("separate native skill package invalidated outer context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(view.Path, "MEMORY.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWorkspace(view); err == nil {
		t.Fatal("modified recalled memory went undetected")
	}
}

func TestTaskWorkspaceRejectsPrivateCrossScopeAndStaleInputs(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*WorkspaceInput)
	}{
		{"public-profile", func(in *WorkspaceInput) { in.Run.AudienceID = "group-one" }},
		{"cross-scope", func(in *WorkspaceInput) { in.Memories[0].Scope = "project:other" }},
		{"cross-agent", func(in *WorkspaceInput) { in.Memories[0].AgentID = "ops" }},
		{"disabled-memory", func(in *WorkspaceInput) { in.Memories[0].Status = "invalidated" }},
		{"expired-memory", func(in *WorkspaceInput) { expired := time.Now().Add(-time.Hour); in.Memories[0].ExpiresAt = &expired }},
		{"unrecognized-file", func(in *WorkspaceInput) {
			in.Files = append(in.Files, ContextFile{Name: "credentials.json", Content: "secret"})
		}},
		{"escaping-context-id", func(in *WorkspaceInput) { in.Run.ID = "../other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache, input := contextFixture(t)
			test.edit(&input)
			if _, err := MaterializeWorkspace(cache, input); err == nil {
				t.Fatal("ineligible context was published")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("failed input validation wrote cache data")
			}
		})
	}
}

func TestPublicTaskWorkspaceContainsOnlyPublicContext(t *testing.T) {
	cache, input := contextFixture(t)
	input.Run.AudienceID = "group-one"
	input.UserProfile = ""
	input.Files = input.Files[:1]
	input.Memories[0].AudienceID = "group-one"
	view, err := MaterializeWorkspace(cache, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(view.Path, "USER.md")); !os.IsNotExist(err) {
		t.Fatal("public task acquired private USER view")
	}
	if err := VerifyWorkspace(view); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredProfileAndMemorySnapshotRotateNativeCompatibility(t *testing.T) {
	_, input := contextFixture(t)
	configuration := Configuration{AgentID: "default", AudienceID: "owner", NativeProfile: filepath.Join(input.Run.Workspace, "native")}
	base, err := Assemble("", "", input.Run.Scope, input.Run.Workspace, configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Configuration){
		func(c *Configuration) { c.UserProfile = "Use Chinese." },
		func(c *Configuration) { c.MemoryFingerprint = "current-revision-digest" },
	} {
		changed := configuration
		edit(&changed)
		updated, err := Assemble("", "", input.Run.Scope, input.Run.Workspace, changed)
		if err != nil || updated.Fingerprint == base.Fingerprint {
			t.Fatalf("changed task configuration reused native compatibility: %+v %v", changed, err)
		}
	}
}
