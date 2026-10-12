package assembly

import (
	"path/filepath"
	"testing"
)

func TestCompleteInstancePolicyDeterminesCompatibility(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := Configuration{AgentID: "helper", AudienceID: "group-a", NativeProfile: filepath.Join(workspace, "native"), PermissionMode: "bypassPermissions", HostInstructions: "role-a", Model: "shared"}
	base, err := Assemble("", "", "shared-project-scope", workspace, config)
	if err != nil {
		t.Fatal(err)
	}
	if base.AgentID != config.AgentID || base.AudienceID != config.AudienceID || base.NativeProfile != config.NativeProfile || base.PermissionMode != config.PermissionMode || base.HostInstructions != config.HostInstructions {
		t.Fatalf("assembly discarded instance policy: %+v", base)
	}
	for _, tc := range []struct {
		name   string
		change func(*Configuration)
	}{
		{"agent", func(c *Configuration) { c.AgentID = "reviewer" }},
		{"audience", func(c *Configuration) { c.AudienceID = "group-b" }},
		{"profile", func(c *Configuration) { c.NativeProfile += "-other" }},
		{"permissions", func(c *Configuration) { c.PermissionMode = "default" }},
		{"role-policy", func(c *Configuration) { c.HostInstructions = "role-b" }},
		{"knowledge", func(c *Configuration) { c.KnowledgeFingerprint = "other-source" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := config
			tc.change(&changed)
			runtime, err := Assemble("", "", "shared-project-scope", workspace, changed)
			if err != nil || runtime.Fingerprint == base.Fingerprint {
				t.Fatalf("changed %s retained session compatibility: %+v %v", tc.name, runtime, err)
			}
		})
	}
	config.PermissionMode = "invalid"
	if _, err := Assemble("", "", "scope", workspace, config); err == nil {
		t.Fatal("invalid native permissions accepted")
	}
}
