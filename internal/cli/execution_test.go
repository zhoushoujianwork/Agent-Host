package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/harness/acp"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func executionFixture(t *testing.T) (serviceLayout, serviceConfig, string) {
	t.Helper()
	layout, config := newServiceFixture(t)
	profile := filepath.Join(layout.Config, "execution.json")
	writeBotJSON(t, profile, acp.Config{Version: 1, Transport: "acp", Engine: "claude", Command: config.Claude, AdapterVersion: acp.ClaudeBridgeVersion})
	return layout, config, profile
}

func TestExecutionProfileIsSharedByCLIAndServiceChannels(t *testing.T) {
	layout, config, profile := executionFixture(t)
	for _, entry := range []string{"chat", "dingtalk serve", "feishu-meeting serve"} {
		o, _, err := parse(entry, []string{"--execution-config", profile, "--workspace", layout.Workspace}, io.Discard)
		if err != nil || o.executionProfile == nil || o.executionFingerprint == "" {
			t.Fatalf("%s: %+v %v", entry, o, err)
		}
	}
	config.ExecutionConfig = profile
	for _, provider := range []string{"dingtalk", "feishu", "feishu-meeting"} {
		config.Channel = provider
		args := config.arguments(layout, "serve")
		found := false
		for i, arg := range args {
			if arg == "--execution-config" && i+1 < len(args) && args[i+1] == profile {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s dropped execution profile", provider)
		}
	}
}

func TestAgentACPOverrideAndFingerprintKeepLegacySeparate(t *testing.T) {
	layout, config, profile := executionFixture(t)
	path := filepath.Join(layout.Config, "agents.json")
	writeBotJSON(t, path, map[string]any{"default_agent": "default", "agents": []map[string]any{{"id": "default", "name": "ACP Agent", "execution_config": "execution.json", "projects": []string{}}}})
	catalog, err := loadAgents(path)
	if err != nil {
		t.Fatal(err)
	}
	base := options{workspace: layout.Workspace, defaultWorkspace: layout.Workspace, defaultScope: layout.Workspace, nativeProfile: layout.Native, binary: config.Claude, model: config.Model, noTools: true, disableDingTalkWorkspace: true, knowledgePluginDisabled: true}
	selected, err := catalog.Agents[0].options(base, "owner", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if selected.executionConfig != profile || selected.executionProfile == nil {
		t.Fatal("relative Agent profile was not selected")
	}
	before, err := assembly.Assemble("", "", layout.Workspace, layout.Workspace, assemblyConfig(base))
	if err != nil {
		t.Fatal(err)
	}
	after, err := assembly.Assemble("", "", layout.Workspace, layout.Workspace, assemblyConfig(selected))
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("ACP reused print runtime identity")
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(layout.State, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, provider := range []string{"dingtalk", "feishu"} {
		parent := &channel.Service{Store: store, Channel: provider, ClientID: "bot"}
		message := state.ChannelMessage{Channel: provider, AccountID: "bot", AgentID: "default", AudienceID: "owner", ConversationType: "1", SenderID: "owner", TenantID: "tenant", ConversationID: "private"}
		child, err := resolveAgentChannel(t.Context(), parent, catalog, base, client, nil, message)
		if err != nil {
			t.Fatal(err)
		}
		runtime, ok := child.Host.(*host.Host)
		if !ok {
			t.Fatal("channel did not compose Host")
		}
		if identity := harness.RunnerIdentity(runtime.Runner); identity.Transport != "acp" {
			t.Fatalf("%s selected %+v", provider, identity)
		}
	}
	if _, err = os.Stat(filepath.Join(layout.Native, "agent-host-acp")); !os.IsNotExist(err) {
		t.Fatal("configuration selection executed ACP")
	}
}

func TestInvalidACPProfileFailsWithoutLegacyFallback(t *testing.T) {
	_, _, profile := executionFixture(t)
	writeBotJSON(t, profile, map[string]any{"version": 1, "transport": "acp", "engine": "claude", "command": "/missing/agent", "adapter_version": acp.ClaudeBridgeVersion})
	if _, _, err := parse("chat", []string{"--execution-config", profile}, io.Discard); err == nil {
		t.Fatal("missing ACP command accepted")
	}
}
