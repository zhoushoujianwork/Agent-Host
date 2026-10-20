//go:build unix

package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestACPIncludesValidatedProcedurePlugin(t *testing.T) {
	adapter, request := fixtureAdapter(t, "normal")
	plugin := nativeSkillFixture(t)
	request.SkillPluginDir, request.SkillPluginDigest = plugin.Path, plugin.Digest
	var provided bool
	options, err := adapter.ACPOptions(t.Context(), request, func(o harness.Observation) error {
		provided = provided || o.Type == "skills_provided"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	plugins := options["plugins"].([]map[string]string)
	if !provided || len(plugins) != 1 || plugins[0]["path"] != plugin.Path || plugins[0]["type"] != "local" {
		t.Fatalf("procedure mount missing: %+v", options)
	}
}

func TestACPNativeInventoryRetainsNamesWithoutPrivateConfiguration(t *testing.T) {
	raw := json.RawMessage(`{"type":"system","subtype":"init","model":"native","tools":["Skill","Read"],"skills":["jms-ssh"],"mcp_servers":[],"plugins":[{"name":"agent-host","version":"1","path":"PRIVATE_SENTINEL"}],"apiKeySource":"PRIVATE_SENTINEL"}`)
	_, evidence, err := ValidateACPInitialization(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(evidence), "jms-ssh") || !strings.Contains(string(evidence), "Skill") || strings.Contains(string(evidence), "PRIVATE_SENTINEL") {
		t.Fatalf("inventory evidence=%s", evidence)
	}
}
