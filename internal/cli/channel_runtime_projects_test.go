package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/projectruntime"
	"agent-host/internal/state"
)

func TestChannelRuntimeProjectWiringPreservesAgentAllowlist(t *testing.T) {
	root, err := state.CanonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "github", "pop")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-c", "init.defaultBranch=main", "init", "-q", repo)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize local fixture: %v %s", err, output)
	}
	catalog := &projectruntime.Catalog{Root: filepath.Join(root, "runtime"), LookupRoots: []string{filepath.Join(root, "github")}}
	candidates, err := catalog.Candidates(t.Context(), repo, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatal("fixture lookup failed", candidates, err)
	}
	project := candidates[0]
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := filepath.Join(root, "native")
	if err := os.MkdirAll(native, 0700); err != nil {
		t.Fatal(err)
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{home: filepath.Join(root, "state"), workspace: repo, defaultWorkspace: repo, defaultScope: "default-workspace", nativeProfile: native, binary: "/bin/echo", model: "fixture", projectCatalog: catalog}
	parent := &channel.Service{Store: store, ClientID: "fixture", Profile: "fixture"}
	message := state.ChannelMessage{AgentID: "default", AudienceID: "owner", ConversationType: "1", MessageID: "inspect", Text: "Inspect project pop authentication."}
	for _, tc := range []struct {
		name      string
		allowlist []string
		want      int
	}{
		{"unrestricted", nil, 1},
		{"empty", []string{}, 0},
		{"different-project", []string{"different"}, 0},
		{"explicit-project", []string{project.ID}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agents := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant", Projects: tc.allowlist}}}
			child, err := resolveAgentChannel(t.Context(), parent, agents, base, client, nil, message)
			if err != nil {
				t.Fatal(err)
			}
			projects, err := child.PrepareProjects(t.Context(), message, nil)
			if err != nil || len(projects) != tc.want {
				t.Fatal("candidate lookup bypassed Agent allowlist", projects, err)
			}
			if tc.want == 0 {
				if err := child.EnrollProjects(t.Context(), message, []state.Project{project}); err == nil {
					t.Fatal("enrollment bypassed Agent allowlist")
				}
			} else if err := child.EnrollProjects(t.Context(), message, projects); err != nil {
				t.Fatal(err)
			}
			// An independent default decision stays default even when its cwd is
			// also the checkout of an enrolled project.
			resolved, runtime, err := child.ResolveProjects(t.Context(), nil, "")
			if err != nil || runtime.ProjectID != "" || resolved.(*host.Host).Config.ProjectID != "" {
				t.Fatal("channel default was routed a second time", runtime.ProjectID, err)
			}
		})
	}
}
