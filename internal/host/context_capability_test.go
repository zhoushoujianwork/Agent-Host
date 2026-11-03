package host

import (
	"testing"

	"agent-host/internal/assistance"
	"agent-host/internal/state"
)

func TestDefaultWorkspaceMemoryCapabilityPreservesOwnerToolBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, audience string
		toolFree, want bool
	}{
		{"owner", state.OwnerAudienceID, false, true},
		{"other-audience", "group-a", false, false},
		{"tool-free", state.OwnerAudienceID, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, runner := testHost(t)
			h.Config.AudienceID = tc.audience
			h.Config.ContextHome = h.Store.Home()
			h.Config.ContextExecutable = "/fixture/agent-host"
			h.Config.ToolsDisabled = tc.toolFree
			run := runOK(t, h, newSession(t, h), "Recall the previously recorded procedure.")
			if run.ProjectID != "" || len(run.Projects) != 0 {
				t.Fatal("fixture must exercise an unregistered default workspace")
			}
			found := false
			for _, server := range runner.requests[0].MCPServers {
				found = found || server.Name == "host_context"
			}
			if found != tc.want {
				t.Fatalf("owner/tool capability boundary: mounted=%v wanted=%v", found, tc.want)
			}
			if found {
				binding, err := assistance.Resolve(t.Context(), h.Store.Home(), "", run.ID)
				// A finished task must not retain authority through a leaked old mount.
				if err == nil {
					t.Fatal("terminal task retained live memory capability", binding.MemoryOnly)
				}
				assembled := false
				for _, e := range run.Events {
					if e.Type == "context.assembled" {
						assembled = e.Data["memory_only"] == true
					}
				}
				if !assembled {
					t.Fatal("default workspace did not use a memory-only binding")
				}
			}
		})
	}
}
