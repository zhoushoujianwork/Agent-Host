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

func TestRestrictedRunExcludesAmbientProjectInstructions(t *testing.T) {
	adapter, req := fixtureAdapter(t, "restricted_readonly")
	req.RestrictedReadOnly = true
	req.HostInstructions = "Trusted Host authorization: member."
	if err := os.WriteFile(filepath.Join(req.Workspace, "AGENTS.md"), []byte("AMBIENT_PROJECT_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", requestPath)
	t.Setenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD", "1")
	result, err := adapter.Run(context.Background(), req, func(obs harness.Observation) error {
		if obs.Type == "project_instructions" {
			t.Error("restricted run loaded ambient project instructions")
		}
		return nil
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("run: %v %+v", err, result)
	}
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args                   []string `json:"args"`
		AdditionalInstructions string   `json:"additional_instructions"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.AdditionalInstructions != "" {
		t.Fatal("ambient additional-directory instruction discovery enabled")
	}
	index := slices.Index(captured.Args, "--append-system-prompt")
	if index < 0 || !strings.Contains(captured.Args[index+1], req.HostInstructions) || strings.Contains(captured.Args[index+1], "AMBIENT_PROJECT_MARKER") {
		t.Fatalf("unexpected restricted instructions: %v", captured.Args)
	}
}

func TestRestrictedRunRejectsUnexpectedNativeCapabilities(t *testing.T) {
	adapter, req := fixtureAdapter(t, "restricted_forbidden")
	req.RestrictedReadOnly = true
	result, err := adapter.Run(context.Background(), req, nil)
	if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "forbidden tool") {
		t.Fatalf("unexpected native inventory was not rejected: status=%s err=%v", result.Status, err)
	}
}
