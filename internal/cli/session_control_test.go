package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type sessionTestRunner struct{ requests []harness.Request }

func (*sessionTestRunner) SupportsFork() bool { return true }
func (r *sessionTestRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.requests = append(r.requests, req)
	return harness.Result{Status: "completed", Text: "continued", Model: req.Model, NativeSessionID: req.NativeSessionID}, nil
}

type sessionTestDecider struct{}

func (sessionTestDecider) Decide(context.Context, string, []state.Memory) (memory.Decision, error) {
	return memory.Decision{Kind: "none"}, nil
}

func TestChannelSessionControlContinuesDingTalkAndFeishuWithoutIMDelivery(t *testing.T) {
	for _, provider := range []string{"dingtalk", "feishu"} {
		t.Run(provider, func(t *testing.T) {
			fixture := seedReviewFixture(t)
			root := fixture.workspace
			store, err := state.Open(fixture.home, false)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err := store.MigrateFileMemory(t.Context(), filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory"), "takeover-test"); err != nil {
				t.Fatal(err)
			}
			base := options{home: fixture.home, workspace: root, defaultWorkspace: root, defaultScope: root, model: "fixture", binary: "/bin/echo"}
			client, err := jev.New("synthetic-not-used", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			agents := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant"}}}
			service := &channel.Service{Store: store, Channel: provider, ClientID: "app", OwnerID: "owner"}
			message := state.ChannelMessage{ID: "source", Channel: provider, AccountID: "app", TenantID: "tenant", MessageID: "om_source", ConversationID: "oc_chat", ConversationType: "1", SenderID: "owner", AgentID: "default", AudienceID: "owner", Profile: "fixture", Binding: "binding", Scope: root, MessageType: "text", Text: "fixture test procedure"}
			replyURL := "https://example.invalid/reply"
			if provider == "feishu" {
				message.ReplyTarget = &state.ChannelReplyTarget{Kind: "feishu", ID: "oc_chat", MessageID: "om_source", UUID: "reply-source"}
				replyURL = ""
			} else {
				message.ReplyExpiresAt = time.Now().Add(time.Hour)
			}
			if _, err := store.EnqueueChannelMessage(t.Context(), message, replyURL); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimNextChannelMessage(t.Context(), "fixture"); err != nil {
				t.Fatal(err)
			}
			child, err := resolveAgentChannel(t.Context(), service, agents, base, client, nil, message)
			if err != nil {
				t.Fatal(err)
			}
			resolved, _, err := child.ResolveProjects(t.Context(), nil, "")
			if err != nil {
				t.Fatal(err)
			}
			runner := &sessionTestRunner{}
			prepare := func(h *host.Host) *host.Host {
				h.Runner = runner
				h.Decider = sessionTestDecider{}
				h.Extractor = nil
				h.Judge = nil
				return h
			}
			h := prepare(resolved.(*host.Host))
			seed, err := h.RoomChannelSession(t.Context(), message.ID)
			if err != nil {
				t.Fatal(err)
			}
			original, err := h.RunChannel(t.Context(), seed, message.Text, message.ID)
			if err != nil {
				t.Fatal(err)
			}
			control := &sessionControl{store: store, timeout: time.Minute, slots: make(chan struct{}, 4), resolve: func(ctx context.Context, run state.Run) (*host.Host, error) {
				h, err := resolveSessionTakeover(ctx, service, agents, base, client, run)
				if err != nil {
					return nil, err
				}
				return prepare(h), nil
			}}
			invoke := func(runID, request string) *httptest.ResponseRecorder {
				t.Helper()
				body, _ := json.Marshal(map[string]string{"run_id": runID, "request_id": request, "message": "continue"})
				w := httptest.NewRecorder()
				control.ServeHTTP(w, httptest.NewRequest("POST", "/session-chat", bytes.NewReader(body)))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				return w
			}
			invoke(original.ID, "web-1")
			if len(runner.requests) != 2 || runner.requests[1].ForkFromNativeSessionID != seed.NativeSessionID {
				t.Fatal("lost native channel context", runner.requests)
			}
			if runner.requests[0].SkillPluginDigest == "" || runner.requests[1].SkillPluginDigest != runner.requests[0].SkillPluginDigest {
				t.Fatal("Web continuation changed preselected source Skills", runner.requests)
			}
			invoke(original.ID, "web-1")
			if len(runner.requests) != 2 {
				t.Fatal("duplicate replayed native tools")
			}
			sessions, err := h.RoomSession(t.Context(), state.RoomInput{Key: message.Binding, RequestID: "im-next", SenderID: "owner", Text: "back on IM"})
			if err != nil {
				t.Fatal(err)
			}
			if sessions.Room.ParentSessionID == seed.ID || sessions.Room.ForkFromNativeSessionID != runner.requests[1].NativeSessionID {
				t.Fatal("channel did not inherit Web continuation", sessions)
			}
			receipt, err := store.ChannelMessage(t.Context(), message.ID)
			if err != nil || receipt.ReplyStatus != "pending" || receipt.RunID != original.ID {
				t.Fatal("Web modified original channel delivery", receipt, err)
			}
		})
	}
}
