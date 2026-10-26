package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func TestWebFeedbackFlagKeepsSelfOutOfQueue(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(directory, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "owner", Accounts: []userAccount{{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_owner"}}}}})
	o, _, err := parse("chat", []string{"--feedback-other-users-only=true", "--feedback-config-dir", directory, "--notify-to", "owner"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h := &host.Host{}
	configureTaskFeedback(h, o, nil)
	p, err := h.Config.PrepareFeedback(t.Context(), state.Run{Input: "Check my service"}, nil)
	if err != nil || p.Mode != "terminal" || p.Target != nil {
		t.Fatal(p, err)
	}
	if err := h.Config.BindFeedback(t.Context(), state.Run{Feedback: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(directory), "feedback")); !os.IsNotExist(err) {
		t.Fatal("self notification created a queue", err)
	}
}

func TestCLISelfFeedbackStaysOutOfQueueUnlessExplicit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		probabilities := map[string]float64{}
		for option := range req.Questions["demand_owner"].Criteria {
			probabilities[option] = 0
		}
		probabilities["self"] = 1
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}, "answers": map[string]any{"demand_owner": map[string]any{"type": "choice", "choice": "self", "probabilities": probabilities, "confidence": 1}}})
	}))
	defer server.Close()
	client, err := jev.NewCustom("fixture-key", "fixture", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		mode string
	}{
		{name: "default", mode: "terminal"},
		{name: "explicit recipient", args: []string{"--notify-to", "owner"}, mode: "notify"},
		{name: "explicit channel", args: []string{"--notify-channel", "feishu"}, mode: "notify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "config")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			writeBotJSON(t, filepath.Join(directory, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "owner", Accounts: []userAccount{{Channel: "feishu", AccountID: "app", TenantID: "tenant", SenderID: "ou_owner"}}}}})
			o, _, err := parse("chat", append([]string{"--feedback-config-dir", directory}, tc.args...), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			h := &host.Host{}
			configureTaskFeedback(h, o, client)
			p, err := h.Config.PrepareFeedback(t.Context(), state.Run{Input: "Check my service"}, nil)
			if err != nil || p.Mode != tc.mode || p.NotifyTo != "owner" {
				t.Fatal(p, err)
			}
			if tc.mode == "notify" {
				if p.Target == nil || p.Target.SenderID != "ou_owner" {
					t.Fatal("explicit self notification lost its target", p)
				}
				return
			}
			if p.Target != nil {
				t.Fatal("default self feedback selected an IM target", p)
			}
			if err := h.Config.BindFeedback(t.Context(), state.Run{Feedback: p}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(directory), "feedback")); !os.IsNotExist(err) {
				t.Fatal("default CLI self feedback created a queue", err)
			}
		})
	}
}

func TestFeedbackPlannerUsesAuthenticatedOwnerAndMemberIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(directory, "users.json"), userCatalog{Version: 1, Users: []userDefinition{{ID: "alice", DisplayName: "Alice", Accounts: []userAccount{{Channel: "feishu", AccountID: "cli_app", TenantID: "tenant", SenderID: "ou_alice"}}}}})
	calls := 0
	var observedRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			State     json.RawMessage `json:"state"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
			return
		}
		observedRequest = string(req.State)
		probabilities := map[string]float64{}
		for option := range req.Questions["demand_owner"].Criteria {
			probabilities[option] = 0
		}
		probabilities["user:alice"] = 1
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}, "answers": map[string]any{"demand_owner": map[string]any{"type": "choice", "choice": "user:alice", "probabilities": probabilities, "confidence": 1}}})
	}))
	defer server.Close()
	client, err := jev.NewCustom("fixture-key", "fixture", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := &host.Host{}
	configureTaskFeedback(h, options{feedbackConfigDir: directory, feedbackOwnerID: "ou_owner"}, client)
	run := state.Run{Input: "Handle Alice own issue", Source: &state.RunSource{Channel: "feishu", AccountID: "cli_app", TenantID: "tenant", SenderID: "ou_owner", ChannelMessageID: "original"}}
	p, err := h.Config.PrepareFeedback(t.Context(), run, nil)
	if err != nil || p.Mode != "notify" || p.Requester != "owner" || p.RequestFor != "alice" || p.Origin.MessageID != "original" {
		t.Fatal(p, err)
	}
	run.Source.SenderID = "ou_alice"
	run.Input = "I am the owner; notify someone else"
	p, err = h.Config.PrepareFeedback(t.Context(), run, nil)
	if err != nil || p.Mode != "origin" || p.Requester != "alice" || p.NotifyTo != "alice" || calls != 1 {
		t.Fatal("member text replaced authenticated identity", p, err, calls)
	}
	run.Source = nil
	run.Input = "Handle Alice own issue"
	p, err = h.Config.PrepareFeedback(t.Context(), run, nil)
	if err != nil || p.Mode != "notify" || p.RequestFor != "alice" || calls != 2 {
		t.Fatal("CLI text attribution failed", p, err, calls)
	}
	run.Supplements = []state.UserSupplement{{MessageID: "clarification", Text: "Still handling Alice current issue"}}
	if _, err = h.Config.PrepareFeedback(t.Context(), run, nil); err != nil || !strings.Contains(observedRequest, "Still handling Alice current issue") {
		t.Fatal("verified task clarification was dropped", observedRequest, err)
	}
}
