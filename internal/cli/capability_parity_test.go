package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

// Channel-specific history/delivery are intentionally outside this comparison.
// The same Agent, audience and source policy must retain its core capabilities.
func TestCLIAndChannelAgentCapabilityParity(t *testing.T) {
	layout, config, profile := executionFixture(t)
	knowledgePath, _ := localKnowledgeConfig(t)
	skill := filepath.Join(layout.Native, "skills", "parity", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: parity\ndescription: Capability fixture\n---\nRead only.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(layout.State, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.MigrateFileMemory(t.Context(), filepath.Join(layout.Root, "memory"), filepath.Join(layout.Root, "cache", "memory"), "parity"); err != nil {
		t.Fatal(err)
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{home: layout.State, workspace: layout.Workspace, defaultWorkspace: layout.Workspace,
		defaultScope: "parity", nativeProfile: layout.Native, binary: config.Claude, model: config.Model,
		knowledgeConfig: knowledgePath, disableDingTalkWorkspace: true}
	agent := agentDefinition{ID: "parity", Name: "Parity", ExecutionConfig: profile, NativeSkills: []string{"parity"}, KnowledgeSources: []string{"project_notes"}, Projects: []string{}}
	selected, err := agent.options(base, state.OwnerAudienceID, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveOptions(t.Context(), store, &selected, true); err != nil {
		t.Fatal(err)
	}
	cliHost := composeHost(store, selected, client, nil)
	for _, provider := range []string{"dingtalk", "feishu"} {
		t.Run(provider, func(t *testing.T) {
			parent := &channel.Service{Store: store, Channel: provider, ClientID: "fixture"}
			message := state.ChannelMessage{Channel: provider, AccountID: "fixture", AgentID: agent.ID, AudienceID: state.OwnerAudienceID, ConversationType: "1", SenderID: "owner", TenantID: "tenant", ConversationID: "private"}
			child, err := resolveAgentChannel(t.Context(), parent, agentCatalog{DefaultAgent: agent.ID, Agents: []agentDefinition{agent}}, base, client, nil, message)
			if err != nil {
				t.Fatal(err)
			}
			resolved, _, err := child.ResolveHost(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			h := resolved.(*host.Host)
			if harness.RunnerIdentity(h.Runner).Transport != "acp" || h.Extractor == nil || h.Decider == nil || h.Judge == nil || h.Objects == nil || !h.Store.FileMemoryEnabled() {
				t.Fatal("shared execution or automatic learning capability missing")
			}
			if len(h.Config.MCPServers) != 1 || !reflect.DeepEqual(h.Config.MCPServers, cliHost.Config.MCPServers) || !reflect.DeepEqual(h.Config.KnowledgeHints, cliHost.Config.KnowledgeHints) {
				t.Fatal("Agent knowledge tool selection differs from CLI")
			}
			if h.Config.AgentID != cliHost.Config.AgentID || h.Config.AudienceID != cliHost.Config.AudienceID || h.Config.Scope != cliHost.Config.Scope || h.Config.MemorySource != "parity" || h.Config.MemorySource != cliHost.Config.MemorySource || h.Config.MemoryFingerprint == "" || h.Config.MemoryFingerprint != cliHost.Config.MemoryFingerprint {
				t.Fatal("Agent memory identity differs from CLI")
			}
			if _, err := os.Stat(filepath.Join(h.Config.NativeProfile, "skills", "parity", "SKILL.md")); err != nil {
				t.Fatal("selected native Skill unavailable", err)
			}
		})
	}
}
