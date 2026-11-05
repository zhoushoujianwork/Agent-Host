//go:build unix

package nativepolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/knowledge"
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
			fixtureSet(t, "AGENT_HOST_FIXTURE_REQUEST", path)
			if _, err := adapter.Run(context.Background(), request, func(harness.Observation) error { return nil }); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var captured struct {
				Args               []string `json:"args"`
				PromptInstructions string   `json:"instructions"`
			}
			if err := json.Unmarshal(raw, &captured); err != nil {
				t.Fatal(err)
			}
			i := slices.Index(captured.Args, "--append-system-prompt-file")
			if tc.extraction {
				if captured.PromptInstructions != "" {
					t.Fatalf("structured extraction gained ordinary reply guidance: %v", captured.Args)
				}
				return
			}
			if i < 0 || !strings.Contains(captured.PromptInstructions, responseStyleInstructions) || strings.Contains(strings.Join(captured.Args, " "), "bypassPermissions") {
				t.Fatalf("reply guidance or unchanged permissions missing: %v", captured.Args)
			}
			if !strings.Contains(captured.PromptInstructions, knowledge.CitationInstructions) || strings.Contains(captured.PromptInstructions, "source name and relative document path for local knowledge") {
				t.Fatal("system prompt must use the shared citation policy without the old local-path rule")
			}
			if !strings.Contains(captured.PromptInstructions, harness.DirectExecutionInstructions) {
				t.Fatal("native business execution lost the prohibition on nested Offload")
			}
			if strings.Contains(captured.PromptInstructions, knowledgeUsageInstructions) == tc.toolsDisabled {
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
