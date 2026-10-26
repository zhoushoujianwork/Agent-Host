package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/decision/jev"
	"agent-host/internal/discovery"
	"agent-host/internal/harness"
	"agent-host/internal/owneranswering"
	"agent-host/internal/state"
)

type answeringTestRunner struct{ requests []harness.Request }

func (r *answeringTestRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.requests = append(r.requests, req)
	if err := emit(harness.Observation{Type: "tool", Tool: &harness.ToolObservation{ID: "tool-one", Name: "mcp__fixture__check", Stage: "call", Input: json.RawMessage(`{"target":"test"}`)}}); err != nil {
		return harness.Result{}, err
	}
	if err := emit(harness.Observation{Type: "tool", Tool: &harness.ToolObservation{ID: "tool-one", Name: "mcp__fixture__check", Stage: "result", Result: json.RawMessage(`{"verified":true}`)}}); err != nil {
		return harness.Result{}, err
	}
	return harness.Result{Status: "completed", Text: "Investigating privately.\n" + answeringOutcomeStart + "\n" + `{"outcome":"answer","text":"Fixture tool verified the service.","reason":"","clarification":""}` + "\n" + answeringOutcomeEnd, Model: "fixture", NativeSessionID: req.NativeSessionID}, nil
}

func TestOwnerAnsweringOwnerCommandBoundToBotAndTenant(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := ownerAnsweringCommand(&owneranswering.Coordinator{Store: store}, dingtalk.Config{ClientID: "bot", CorpID: "tenant", OwnerUserID: "owner"})
	base := state.ChannelMessage{Channel: "dingtalk", AccountID: "bot", TenantID: "tenant", SenderID: "owner", ConversationType: "1", AudienceID: "owner", Text: "/dismiss unknown.1"}
	for _, kind := range []string{"owner", "wrong-account", "wrong-tenant", "peer", "group", "other-audience", "other-channel"} {
		t.Run(kind, func(t *testing.T) {
			m := base
			switch kind {
			case "wrong-account":
				m.AccountID = "other"
			case "wrong-tenant":
				m.TenantID = "other"
			case "peer":
				m.SenderID = "peer"
			case "group":
				m.ConversationType = "2"
			case "other-audience":
				m.AudienceID = "peer"
			case "other-channel":
				m.Channel = "feishu"
			}
			handled, reply, err := command(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "owner" {
				if !handled || !strings.Contains(reply, "Unknown question") {
					t.Fatal("owner reference not handled", reply)
				}
			} else if handled || reply != "" {
				t.Fatal("unbound sender admitted")
			}
		})
	}
}
func TestOwnerAnsweringComposedHostToolsAndPartition(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.RegisterProject(t.Context(), "project-a", "A", root)
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
	a := agentDefinition{ID: "default", Name: "General", Projects: []string{project.ID}, UserFile: "owner-private-USER.md", userProfile: "OWNER_PRIVATE_MARKER", contextFiles: []assembly.ContextFile{{Name: "AGENTS.md", Source: "fixture", Content: "Use authorized fixture procedures."}}}
	cfg := discovery.Config{Profile: "corp:owner", OwnerAnswering: discovery.OwnerAnsweringConfig{AgentID: "default", Instructions: "Use authorized Skills/tools only for the test service."}}
	q := owneranswering.Question{ID: "question", Source: discovery.Source{Type: "dm", ConversationID: "peer-dm", ProjectID: project.ID}}
	client, err := jev.New("fixture-unused", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	h, err := composeAnsweringHost(t.Context(), store, cfg, base, a, q, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Config.AudienceID == "owner" || h.Config.ToolsDisabled || h.Config.UserProfile != "" || h.Config.HistoryHome != "" || h.Config.ContextHome != "" || h.Config.PrepareFeedback != nil || h.Config.BindFeedback != nil || h.Config.ProjectID != project.ID {
		t.Fatal("answering capability/partition boundary lost", h.Config)
	}
	if h.Config.NativeProfile == native || !strings.HasPrefix(h.Config.NativeProfile, filepath.Join(native, "instances")) {
		t.Fatal("owner native profile reused")
	}
	data, _ := json.Marshal(h.Config.ContextFiles)
	if strings.Contains(string(data), "OWNER_PRIVATE_MARKER") {
		t.Fatal("owner private context inherited")
	}
	runner := &answeringTestRunner{}
	h.Runner = runner
	h.Decider = sessionTestDecider{}
	h.Extractor = nil
	h.Judge = nil
	h.Objects = nil
	first, err := runAnsweringQuestion(t.Context(), h, q)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runAnsweringQuestion(t.Context(), h, q)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == "" || second.RunID == first.RunID || len(runner.requests) != 2 || runner.requests[0].NativeSessionID == runner.requests[1].NativeSessionID || runner.requests[0].Resume || runner.requests[0].ToolsDisabled {
		t.Fatal("fresh tool-enabled question session missing")
	}
	if !strings.Contains(runner.requests[0].HostInstructions, "Host exclusively owns these deliveries") {
		t.Fatal("delivery authority guidance absent")
	}
	if _, err = store.Run(t.Context(), first.RunID); err == nil {
		t.Fatal("answering run entered owner partition")
	}
	if run, err := h.Store.Run(t.Context(), first.RunID); err != nil || run.Output == "" {
		t.Fatal("question run missing", err)
	}
	a.Projects = []string{"another-project"}
	if _, err = composeAnsweringHost(t.Context(), store, cfg, base, a, q, client, nil); err == nil {
		t.Fatal("Agent project allowlist bypassed")
	}
}
func TestOwnerAnsweringWorkspaceReceiptOutcomes(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{`{"status":"provider_pending","result":{"ok":true,"outcome":"pending","data":{"openTaskId":"task"}}}`, "provider_pending"}, {`{"status":"completed","result":{"ok":true,"outcome":"success","data":{"status":"SUCCESS","openMessageId":"message"}}}`, "accepted"}, {`{"status":"completed","result":{}}`, "unknown"}, {`{"status":"failed","result":{}}`, "failed"}, {`{"status":"pending"}`, "unknown"}} {
		var value any
		json.Unmarshal([]byte(tc.raw), &value)
		r := answeringWorkspaceReceipt(value, nil)
		if r.Status != tc.want {
			t.Fatal("invented delivery", r)
		}
		if strings.Contains(tc.raw, "openMessageId") && len(r.MessageIDs) != 1 {
			t.Fatal("send ID lost")
		}
	}
	if r := answeringWorkspaceReceipt(map[string]any{"status": "completed", "result": map[string]any{"ok": true, "outcome": "success"}}, errors.New("receipt interrupted")); r.Status != "unknown" {
		t.Fatal("uncertain send accepted")
	}
}
func TestOwnerAnsweringStandaloneDiscoveryRejected(t *testing.T) {
	// Validation rejects the enabled path before native execution or state open.
	cfg := discovery.Config{DWSExecutable: os.Args[0], Profile: "corp:owner", IntervalSeconds: 30, InitialWindowHours: 2, ReconciliationWindowHours: 1, RetentionDays: 1, PageLimit: 1, MaxMessages: 10, TimeoutSeconds: 10, Model: "fixture", Sources: []discovery.Source{{Type: "dm", ConversationID: "dm", PeerOpenDingTalkID: "peer", ProjectID: "project-a"}}, OwnerAnswering: discovery.OwnerAnsweringConfig{Enabled: true, AgentID: "default", Instructions: "Test scope", Bindings: []discovery.AnsweringBinding{{ConversationID: "dm", PeerOpenDingTalkID: "peer", Owner: discovery.AnsweringAuthor{ID: "owner", Type: "person"}, Peer: discovery.AnsweringAuthor{ID: "peer", Type: "person"}}}}}
	path := filepath.Join(t.TempDir(), "discovery.json")
	data, _ := json.Marshal(cfg)
	os.WriteFile(path, data, 0600)
	var out, errout bytes.Buffer
	err := runDiscovery(t.Context(), []string{"collect", "--config", path, "--home", t.TempDir()}, &out, &errout)
	if err == nil || !strings.Contains(err.Error(), "not standalone discovery") {
		t.Fatal("standalone enabled answering", err)
	}
	if _, err = composeDiscovery(t.Context(), nil, cfg, "/bin/echo"); err == nil || !strings.Contains(err.Error(), "managed DingTalk") {
		t.Fatal("unattached discovery composition accepted answering", err)
	}
}
