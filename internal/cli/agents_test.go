package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func TestAgentConfigBindingsAndStrictValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	valid := `{"default_agent":"default","agents":[{"id":"default","name":"General"},{"id":"team","name":"Team","projects":[],"knowledge_sources":[]}],"group_bindings":[{"conversation_id":"group-one","agent_id":"team"}]}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadAgents(path)
	if err != nil {
		t.Fatal(err)
	}
	team, _ := c.definition("team")
	if team.Projects == nil || team.KnowledgeSources == nil {
		t.Fatal("explicit empty context selection became inherited access")
	}
	message := dingtalk.Message{ConversationType: "2", TenantID: "enterprise", ConversationID: "group-one", SenderID: "one"}
	first, err := c.selectAgent("bot", message)
	if err != nil || first.AgentID != "team" || first.AudienceID == "owner" {
		t.Fatalf("explicit group binding: %+v %v", first, err)
	}
	message.SenderID = "two"
	second, _ := c.selectAgent("bot", message)
	if first != second {
		t.Fatal("group partition includes sender")
	}
	message.ConversationID = "group-two"
	other, _ := c.selectAgent("bot", message)
	if other.AgentID != "default" || other.AudienceID == first.AudienceID {
		t.Fatal("default group lost its independent audience")
	}
	message.ConversationType = "1"
	private, _ := c.selectAgent("bot", message)
	if private.AgentID != "default" || private.AudienceID != "owner" {
		t.Fatal("private compatibility partition changed")
	}
	for _, raw := range []string{
		strings.Replace(valid, `"default_agent":"default"`, `"default_agent":"missing"`, 1),
		strings.Replace(valid, `"id":"team"`, `"id":"default"`, 1),
		strings.Replace(valid, `"id":"team"`, `"id":"../team"`, 1),
		strings.Replace(valid, `"agent_id":"team"`, `"agent_id":"missing"`, 1),
		strings.Replace(valid, `"name":"Team"`, `"name":"Team","NAME":"Other"`, 1),
		strings.Replace(valid, `"name":"Team"`, `"name":"Team","unexpected":"value"`, 1),
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadAgents(path); err == nil {
			t.Fatalf("accepted invalid configuration: %s", raw)
		}
	}
}

func TestAgentRuntimeUsesPinnedIdentityAndIndependentProfiles(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"workspace", "native", "a", "b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a", "b"} {
		if _, err := store.RegisterProject(ctx, name, name, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	native := filepath.Join(root, "native")
	for name, body := range map[string]string{"settings.json": "{}", "CLAUDE.md": "private instructions", "private-history.json": "private conversation"} {
		if err := os.WriteFile(filepath.Join(native, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{home: filepath.Join(root, "state"), workspace: filepath.Join(root, "workspace"), defaultWorkspace: filepath.Join(root, "workspace"), defaultScope: filepath.Join(root, "workspace"), nativeProfile: native, binary: "/bin/echo", model: "base-model"}
	c := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "General"}, {ID: "team", Name: "Team", Projects: []string{"a"}, Model: "team-model", role: "Help with builds."}}}
	parent := &channel.Service{Store: store, ClientID: "bot", Profile: "queue-profile"}
	message := state.ChannelMessage{AgentID: "team", ConversationType: "2", TenantID: "enterprise", ConversationID: "group-one"}
	message.AudienceID = channel.Key("group-audience-v1", "dingtalk", parent.ClientID, message.TenantID, message.ConversationID)
	child, err := resolveAgentChannel(ctx, parent, c, base, client, nil, message)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := child.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != "a" {
		t.Fatalf("catalog was not filtered: %+v %v", projects, err)
	}
	h, runtime, err := child.ResolveHost(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	actual := h.(*host.Host)
	if runtime.AgentID != "team" || runtime.AudienceID != message.AudienceID || runtime.PermissionMode != "dontAsk" || !actual.Config.RestrictedReadOnly || runtime.NativeProfile == native || actual.Config.Model != "team-model" || !strings.Contains(runtime.HostInstructions, "Help with builds.") {
		t.Fatalf("instance runtime not propagated: %+v", runtime)
	}
	entries, err := os.ReadDir(runtime.NativeProfile)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("private history/instructions copied: %+v %v", entries, err)
	}
	if _, _, err := child.ResolveHost(ctx, "b"); err == nil {
		t.Fatal("out-of-catalog project accepted")
	}
	message.ConversationID = "group-two"
	message.AudienceID = channel.Key("group-audience-v1", "dingtalk", parent.ClientID, message.TenantID, message.ConversationID)
	second, err := resolveAgentChannel(ctx, parent, c, base, client, nil, message)
	if err != nil {
		t.Fatal(err)
	}
	_, secondRuntime, err := second.ResolveHost(ctx, "a")
	if err != nil || secondRuntime.NativeProfile == runtime.NativeProfile || secondRuntime.Fingerprint == runtime.Fingerprint {
		t.Fatalf("cross-group native environment reused: %+v %v", secondRuntime, err)
	}
	message.AudienceID = "owner"
	if _, err := resolveAgentChannel(ctx, parent, c, base, client, nil, message); err == nil {
		t.Fatal("group fell back into private owner audience")
	}
	message.AgentID = "removed"
	message.AudienceID = channel.Key("group-audience-v1", "dingtalk", parent.ClientID, message.TenantID, message.ConversationID)
	if _, err := resolveAgentChannel(ctx, parent, c, base, client, nil, message); err == nil {
		t.Fatal("removed pinned agent silently used default")
	}
}

func TestManagedCheckValidatesAgentsWithoutCreatingProfiles(t *testing.T) {
	layout, config := newServiceFixture(t)
	config.AgentsConfig = filepath.Join(layout.Config, "agents.json")
	writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
	c := agentCatalog{DefaultAgent: "team", Agents: []agentDefinition{{ID: "team", Name: "Team", Projects: []string{}, KnowledgeSources: []string{}}}}
	writeBotJSON(t, config.AgentsConfig, c)
	if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(layout.Native)
	if len(entries) != 0 {
		t.Fatal("check created a native profile")
	}
	c.Agents[0].Projects = []string{"missing-project"}
	writeBotJSON(t, config.AgentsConfig, c)
	if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("check accepted an unknown project reference")
	}
}

func TestAgentKnowledgeSubsetDoesNotOpenExcludedCredentials(t *testing.T) {
	config, credential := knowledgeFixture(t)
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	o := options{knowledgeConfig: config, projectID: "api", knowledgeNames: []string{}}
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 {
		t.Fatalf("excluded source was opened: %v", err)
	}
	o.knowledgeNames = []string{"docs"}
	if err := resolveKnowledge(&o); err == nil {
		t.Fatal("selected source with unavailable credentials was silently omitted")
	}
}
