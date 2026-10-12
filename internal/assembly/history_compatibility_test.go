package assembly

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestEmptyHistoryPolicyPreservesLegacyFingerprintConfiguration(t *testing.T) {
	config := Configuration{AgentID: "default", AudienceID: "owner", PermissionMode: "default", HostInstructions: "trusted role",
		KnowledgeFingerprint: "knowledge-policy", Binary: "/fixture/claude", Model: "fixture", NativeProfile: "/fixture/profile",
		AllowedTools: []string{"Read"}}
	// This is the configuration shape embedded in runtime fingerprints before
	// conversation history was added. Empty policy must preserve its JSON bytes.
	legacy := struct {
		AgentID, AudienceID          string
		PermissionMode               string
		HostInstructions             string
		KnowledgeFingerprint         string
		Binary, Model, NativeProfile string
		ToolsDisabled                bool
		AllowedTools                 []string
	}{config.AgentID, config.AudienceID, config.PermissionMode, config.HostInstructions, config.KnowledgeFingerprint,
		config.Binary, config.Model, config.NativeProfile, config.ToolsDisabled, config.AllowedTools}
	before, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(config)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("history-disabled configuration changed existing native compatibility: before=%s after=%s err=%v", before, after, err)
	}
	config.ConversationHistoryPolicy = "conversation-history-v1"
	enabled, err := json.Marshal(config)
	if err != nil || bytes.Equal(before, enabled) {
		t.Fatal("enabled history policy did not participate in compatibility", err)
	}
	var previous, current map[string]any
	if json.Unmarshal(before, &previous) != nil || json.Unmarshal(enabled, &current) != nil || current["ConversationHistoryPolicy"] != config.ConversationHistoryPolicy {
		t.Fatal("enabled history configuration omitted its policy")
	}
	delete(current, "ConversationHistoryPolicy")
	if !reflect.DeepEqual(previous, current) {
		t.Fatal("history changed unrelated configuration fields", previous, current)
	}
}
