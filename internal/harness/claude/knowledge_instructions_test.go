//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestKnowledgeUsageIsSystemGuidanceWithoutGrantingPermissions(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		toolsDisabled, extraction bool
	}{
		{"ordinary-tools", false, false},
		{"ordinary-tool-free", true, false},
		{"structured-extraction", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "success")
			request.ToolsDisabled, request.Extraction = tc.toolsDisabled, tc.extraction
			path := filepath.Join(t.TempDir(), "request.json")
			t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
			if _, err := adapter.Run(context.Background(), request, func(harness.Observation) error { return nil }); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var captured struct {
				Args []string `json:"args"`
			}
			if err := json.Unmarshal(raw, &captured); err != nil {
				t.Fatal(err)
			}
			i := slices.Index(captured.Args, "--append-system-prompt")
			if tc.extraction {
				if i >= 0 {
					t.Fatalf("structured extraction gained ordinary reply guidance: %v", captured.Args)
				}
				return
			}
			if i < 0 || !strings.Contains(captured.Args[i+1], responseStyleInstructions) || strings.Contains(strings.Join(captured.Args, " "), "bypassPermissions") {
				t.Fatalf("reply guidance or unchanged permissions missing: %v", captured.Args)
			}
			if strings.Contains(captured.Args[i+1], knowledgeUsageInstructions) == tc.toolsDisabled {
				t.Fatalf("knowledge tool guidance disagrees with tool availability: %v", captured.Args)
			}
		})
	}
	for _, toolsDisabled := range []bool{false, true} {
		if !strings.HasSuffix(withKnowledgeGuidance("project rules", toolsDisabled), "project rules") {
			t.Fatal("project rules lost")
		}
	}
}
