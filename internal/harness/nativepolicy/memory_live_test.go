package nativepolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
	"github.com/google/uuid"
)

func TestCleanMemoryLiveExplicitCompatibleProfile(t *testing.T) {
	if os.Getenv("AGH_CLEAN_MEMORY_LIVE") != "1" {
		t.Skip("explicit native acceptance only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	library, err := offloadexec.NewLibrary(filepath.Join(home, ".config/offload/config.json"), "claude-cc-sonnet-5-5", "", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWithLibrary("", library)
	tools := 0
	result, err := adapter.Run(context.Background(), harness.Request{
		Workspace: workspace, NativeSessionID: uuid.NewString(), Model: library.Model(),
		ToolsDisabled: true, Extraction: true,
		Prompt:     "Validation evidence: the test datum marker has value MEMORY_TOOLFREE_PINE_4261. Return only the required JSON object containing that marker, without tools or operations.",
		JSONSchema: `{"type":"object","additionalProperties":false,"required":["marker"],"properties":{"marker":{"type":"string"}}}`,
	}, func(observation harness.Observation) error {
		if observation.Tool != nil {
			tools++
		}
		return nil
	})
	if err != nil || result.Status != "completed" || tools != 0 || result.Evidence == nil || result.Evidence.Backend != "claude" || result.Evidence.Profile != "claude-cc-sonnet-5-5" {
		t.Fatalf("status=%s model=%s tool_events=%d error=%v", result.Status, result.Model, tools, err)
	}
	var output struct {
		Marker string `json:"marker"`
	}
	if json.Unmarshal(result.StructuredOutput, &output) != nil || output.Marker != "MEMORY_TOOLFREE_PINE_4261" {
		t.Fatalf("invalid structured extraction: %s", result.StructuredOutput)
	}
	t.Logf("internal_profile=%s actual_model=%s tools=%d structured=true", result.Evidence.Profile, result.Model, tools)
}
