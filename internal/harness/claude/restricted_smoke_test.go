//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"agent-host/internal/harness"
)

// Opt-in local acceptance against the installed executable, never a channel send.
// The profile path contains no credentials; credential values stay in child env.
func TestRestrictedNativeSmoke(t *testing.T) {
	profile := os.Getenv("AGENT_HOST_CLAUDE_RESTRICTED_SMOKE_PROFILE")
	if profile == "" {
		t.Skip("set an approved provider profile for native acceptance")
	}
	req := restrictedRequest(t)
	workspace, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	req.Workspace = workspace
	req.NativeProfile = profile
	provider, err := restrictedProviderEnvironment(req)
	if err != nil {
		t.Fatal(err)
	}
	// Keep native session persistence in a disposable profile too.
	req.NativeProfile = t.TempDir()
	settings, err := json.Marshal(map[string]any{"env": provider, "permissions": map[string]any{"allow": []string{"Bash", "Write", "Edit"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(req.NativeProfile, "settings.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(req.Workspace, "allowed.txt"), []byte("READ_SMOKE_OK\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(req.Workspace, "forbidden-marker")
	req.Prompt = "First use the Read tool to read allowed.txt and report its contents. Then attempt to use Bash to run touch forbidden-marker, and attempt Write or Edit to create forbidden-marker. If those tools are unavailable, state that they are unavailable. Never use any other path."
	req.HostInstructions = "This is a disposable read-only capability test. Only Read, Glob and Grep are authorized. Do not use any execution or mutation tool."
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var initialized, read bool
	result, err := New("").Run(ctx, req, func(obs harness.Observation) error {
		if obs.Type == "init" {
			var event struct {
				Subtype string   `json:"subtype"`
				Tools   []string `json:"tools"`
			}
			if json.Unmarshal(obs.Raw, &event) == nil && event.Subtype == "init" {
				initialized = true
				t.Logf("native tool inventory: %v", event.Tools)
				for _, tool := range event.Tools {
					if !slices.Contains([]string{"Read", "Glob", "Grep"}, tool) {
						t.Errorf("unexpected native tool inventory entry: %s", tool)
					}
				}
				if !slices.Contains(event.Tools, "Read") {
					t.Error("native inventory lacks Read")
				}
			}
		}
		if obs.Tool != nil && obs.Tool.Name == "Read" && obs.Tool.Stage == "call" {
			read = true
		}
		return nil
	})
	if err != nil {
		t.Fatal("native restricted invocation failed; no fallback attempted:", err)
	}
	if result.Status != "completed" || !initialized || !read {
		t.Fatalf("native acceptance incomplete: status=%s init=%t read=%t", result.Status, initialized, read)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("restricted native invocation created the forbidden marker")
	}
}
