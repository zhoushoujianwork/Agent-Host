package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/decision/jev"
	"agent-host/internal/discovery"
	"agent-host/internal/owneranswering"
	"agent-host/internal/state"
)

// This proves assembly/session isolation without opening a listener or starting
// native execution. The separate tool execution test exercises Host.Run.
func TestOwnerAnsweringDedicatedAudienceAndProfile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := store.RegisterProject(t.Context(), "project-a", "A", root)
	if err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(root, "native")
	if err = os.Mkdir(native, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(native, "settings.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := options{home: store.Home(), workspace: root, defaultWorkspace: root, defaultScope: root, nativeProfile: native, binary: "/bin/echo", model: "fixture", knowledgePluginDisabled: true}
	a := agentDefinition{ID: "default", Name: "General", Projects: []string{p.ID}, UserFile: "owner-private-USER.md", userProfile: "OWNER_PRIVATE_MARKER"}
	cfg := discovery.Config{Profile: "corp:owner", OwnerAnswering: discovery.OwnerAnsweringConfig{AgentID: "default", Instructions: "Use the existing authorized test tools."}}
	q := owneranswering.Question{ID: "question", Source: discovery.Source{Type: "dm", ConversationID: "peer-dm", ProjectID: p.ID}}
	client, err := jev.New("fixture-unused", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	h, err := composeAnsweringHost(t.Context(), store, cfg, base, a, q, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Config.AudienceID == "owner" || h.Config.NativeProfile == native || !strings.Contains(h.Config.NativeProfile, "audiences") || h.Config.ToolsDisabled || h.Config.UserProfile != "" || h.Config.ProjectID != p.ID {
		t.Fatal("dedicated capability/session policy lost", h.Config)
	}
	data, _ := json.Marshal(h.Config.ContextFiles)
	if strings.Contains(string(data), "OWNER_PRIVATE_MARKER") || h.Config.ContextHome != "" || h.Config.HistoryHome != "" || h.Config.PrepareFeedback != nil {
		t.Fatal("owner profile/history/feedback inherited")
	}
	one, err := h.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	two, err := h.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if one.NativeSessionID == two.NativeSessionID || one.AudienceID == "owner" {
		t.Fatal("fresh native sessions not isolated")
	}
	if _, err = store.Session(t.Context(), one.ID); err == nil {
		t.Fatal("question session exposed in owner partition")
	}
}
