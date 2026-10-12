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

func TestInstanceProfilePolicyAndInstructionsArePerInvocation(t *testing.T) {
	adapter, request := fixtureAdapter(t, "normal")
	path := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
	t.Setenv("AGENT_HOST_FIXTURE_VERSION", "2.1.223")
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient/private-profile")
	if err := os.WriteFile(filepath.Join(request.Workspace, "AGENTS.md"), []byte("Use the local fixture convention."), 0600); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{filepath.Join(t.TempDir(), "group-a"), filepath.Join(t.TempDir(), "group-b")} {
		request.NativeProfile = profile
		request.PermissionMode = "bypassPermissions"
		request.HostInstructions = "Trusted Host public-reply policy and instance role."
		result, err := adapter.Run(context.Background(), request, nil)
		if err != nil || result.Status != "completed" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var captured struct {
			Args       []string `json:"args"`
			Profile    string   `json:"native_profile"`
			AutoMemory string   `json:"auto_memory_disabled"`
			Cwd        string   `json:"cwd"`
		}
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		if captured.Profile != profile || captured.AutoMemory != "1" || captured.Cwd != request.Workspace {
			t.Fatalf("incorrect child environment: %+v", captured)
		}
		index := slices.Index(captured.Args, "--permission-mode")
		if index < 0 || captured.Args[index+1] != "bypassPermissions" {
			t.Fatalf("explicit business permissions missing: %v", captured.Args)
		}
		index = slices.Index(captured.Args, "--append-system-prompt")
		if index < 0 || !strings.Contains(captured.Args[index+1], request.HostInstructions) || !strings.Contains(captured.Args[index+1], "Use the local fixture convention.") || slices.Contains(captured.Args, "--system-prompt") {
			t.Fatalf("native prompt was replaced or append content missing: %v", captured.Args)
		}
		if os.Getenv("CLAUDE_CONFIG_DIR") != "/ambient/private-profile" {
			t.Fatal("instance modified process-global native profile")
		}
	}
}

func TestNativeInstanceSettingsRejectMalformedValues(t *testing.T) {
	base := harness.Request{Prompt: "task", Workspace: t.TempDir(), NativeSessionID: testSession}
	for _, profile := range []string{"relative", "/tmp/nul\x00profile"} {
		request := base
		request.NativeProfile = profile
		if _, err := arguments(request); err == nil {
			t.Fatalf("accepted native profile %q", profile)
		}
	}
	for _, mode := range []string{"--bypass", "unknown", "bypassPermissions\x00"} {
		request := base
		request.PermissionMode = mode
		if _, err := arguments(request); err == nil {
			t.Fatalf("accepted permission mode %q", mode)
		}
	}
}
