package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func TestFeishuResolvedProjectContextIsOwnerOnlyAndRespectsNoTools(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "state")
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"workspace", "project", "native"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	project, err := store.RegisterProject(t.Context(), "fixture", "Fixture", filepath.Join(root, "project"))
	if err != nil {
		t.Fatal(err)
	}
	native, marker := meetingCommandFixtures(t)
	network := blockMeetingCheckNetwork(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{home: home, workspace: filepath.Join(root, "workspace"), defaultWorkspace: filepath.Join(root, "workspace"), defaultScope: filepath.Join(root, "workspace"), nativeProfile: filepath.Join(root, "native"), binary: native, model: "fixture-model"}
	catalog := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant", Projects: []string{project.ID}}}}
	parent := &channel.Service{Store: store, Channel: state.ReplyTargetFeishu, ClientID: "cli_fixture", Profile: "shared-profile"}
	for _, tc := range []struct {
		name, provider, conversationType, conversation string
		noTools, wantContext                           bool
	}{
		{"owner", state.ReplyTargetFeishu, "1", "oc_private", false, true},
		{"group", state.ReplyTargetFeishu, "2", "oc_group", false, false},
		{"meeting", state.ReplyTargetFeishuMeeting, "2", "700000000000000001", false, false},
		{"owner-no-tools", state.ReplyTargetFeishu, "1", "oc_private", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incoming := channel.Message{ConversationType: tc.conversationType, ConversationID: tc.conversation, SenderID: "ou_owner", TenantID: "tenant", ReplyTarget: state.ChannelReplyTarget{Kind: tc.provider}}
			var selection channel.AgentSelection
			var err error
			if tc.provider == state.ReplyTargetFeishuMeeting {
				selection, err = catalog.selectMeetingAgent(parent.ClientID, incoming)
			} else {
				selection, err = catalog.selectFeishuAgent(parent.ClientID, incoming)
			}
			if err != nil {
				t.Fatal(err)
			}
			selected := base
			selected.noTools = tc.noTools
			message := state.ChannelMessage{Channel: tc.provider, AccountID: parent.ClientID, AgentID: selection.AgentID, AudienceID: selection.AudienceID, ConversationType: incoming.ConversationType, ConversationID: incoming.ConversationID, SenderID: incoming.SenderID, TenantID: incoming.TenantID}
			child, err := resolveAgentChannel(t.Context(), parent, catalog, selected, client, nil, message)
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"ResolveHost", "ResolveProjects"} {
				t.Run(method, func(t *testing.T) {
					var resolved channel.Host
					var runtime assembly.Runtime
					var err error
					if method == "ResolveHost" {
						resolved, runtime, err = child.ResolveHost(t.Context(), project.ID)
					} else {
						resolved, runtime, err = child.ResolveProjects(t.Context(), []string{project.ID}, project.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					h, ok := resolved.(*host.Host)
					if !ok {
						t.Fatal("project resolver did not return the composed Host")
					}
					if h.Config.ProjectID != project.ID || len(h.Config.Projects) != 1 || h.Config.Projects[0].ID != project.ID || h.Config.AudienceID != selection.AudienceID || runtime.AudienceID != selection.AudienceID || h.Config.ToolsDisabled != tc.noTools {
						t.Fatal("registered project, audience or tool policy was lost during resolution")
					}
					if tc.wantContext {
						if h.Config.AudienceID != state.OwnerAudienceID || h.Config.ContextHome != home || h.Config.ContextExecutable != executable {
							t.Fatal("owner project resolution omitted its scoped context configuration")
						}
					} else if h.Config.ContextHome != "" || h.Config.ContextExecutable != "" {
						t.Fatal("public or tool-free resolution inherited owner context capability")
					}
					if h.Config.ContextConfig != "" {
						t.Fatal("Feishu resolution acquired an unconfigured discovery source")
					}
				})
			}
		})
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) || network.calls.Load() != 0 {
		t.Fatal("context assembly invoked a native process or network")
	}
}
