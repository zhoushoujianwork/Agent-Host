//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestrictedProviderEnvironment(t *testing.T) {
	adapter, req := fixtureAdapter(t, "restricted_auth")
	req.RestrictedReadOnly = true
	req.NativeProfile = t.TempDir()
	if err := os.WriteFile(filepath.Join(req.NativeProfile, "settings.json"), []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-profile-token","ANTHROPIC_BASE_URL":"https://provider.invalid","UNREVIEWED_PROFILE_ENV":"must-not-inherit"},"hooks":{"SessionStart":[{"command":"ambient-hook"}]},"permissions":{"allow":["Bash"]},"apiKeyHelper":"must-not-run"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "fake-inherited-token")
	t.Setenv("UNREVIEWED_PROFILE_ENV", "")
	capture := filepath.Join(t.TempDir(), "provider.json")
	request := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_PROVIDER_ENV", capture)
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", request)
	if result, err := adapter.Run(context.Background(), req, nil); err != nil || result.Status != "completed" {
		t.Fatalf("run failed: %v %+v", err, result)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env["token"] != "fake-profile-token" || env["base_url"] != "https://provider.invalid" || env["unreviewed"] != "" {
		t.Fatal("reviewed provider configuration was not isolated in the child environment")
	}
	raw, err = os.ReadFile(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"fake-profile-token", "https://provider.invalid", "ambient-hook", "must-not-run", "must-not-inherit"} {
		if strings.Contains(string(raw), marker) {
			t.Fatal("provider secret or ambient setting leaked into argv")
		}
	}
}

func TestRestrictedProviderSettingsRejectUnsafeOrHelperOnly(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, kind := range []string{"helper", "permissions", "directory_permissions", "symlink", "malformed", "nonstring"} {
		t.Run(kind, func(t *testing.T) {
			req := restrictedRequest(t)
			req.NativeProfile = t.TempDir()
			path := filepath.Join(req.NativeProfile, "settings.json")
			content := `{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-token"}}`
			switch kind {
			case "helper":
				content = `{"apiKeyHelper":"never execute"}`
			case "malformed":
				content = `{"env":`
			case "nonstring":
				content = `{"env":{"ANTHROPIC_AUTH_TOKEN":123}}`
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "permissions":
				if err := os.Chmod(path, 0660); err != nil {
					t.Fatal(err)
				}
			case "directory_permissions":
				if err := os.Chmod(req.NativeProfile, 0770); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(req.NativeProfile, "actual.json")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := restrictedProviderEnvironment(req); err == nil {
				t.Fatal("accepted unsafe provider settings")
			}
		})
	}
}
