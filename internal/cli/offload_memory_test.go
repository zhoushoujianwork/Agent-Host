package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

func configureMemoryRig(t *testing.T, r *offloadRig, endpoint string, processing bool) {
	t.Helper()
	writeBotJSON(t, r.o.offloadConfig, map[string]any{"version": 1, "command": r.o.offloadProfile.Command, "profile": "alias", "timeout": "30s",
		"memory": map[string]any{"enabled": true, "processing_enabled": processing, "provider": "custom", "endpoint": endpoint, "model": "fixture-memory"}})
	t.Setenv("JEV_API_KEY", "fixture-memory-key")
	if err := loadExecutionOptions(&r.o); err != nil {
		t.Fatal(err)
	}
	if err := resolveOffloadChannelOptions(&r.o); err != nil {
		t.Fatal(err)
	}
	configureOffloadChannel(r.service, r.o, offloadTestTenant, nil)
}

// This exercises the existing typed Jev protocol and classifier, without a
// network provider. The classification decisions are explicit test inputs.
func memoryHTTPFixture(t *testing.T, r *offloadRig, fail *atomic.Bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if len(r.replies.snapshot()) == 0 {
			t.Error("classifier called before the answer was delivered")
		}
		if req.Header.Get("Authorization") != "Bearer fixture-memory-key" {
			t.Error("wrong classifier credential source")
		}
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "fixture-memory-key must never appear in errors")
			return
		}
		var input struct {
			State     map[string]any `json:"state"`
			Questions map[string]struct {
				Type     string         `json:"type"`
				Criteria map[string]any `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if _, ok := input.State["result"]; ok {
			t.Error("assistant output offered as memory evidence")
		}
		statement, _ := input.State["statement"].(string)
		confidence := 1.0
		response := jev.Response{Model: "fixture-memory-resolved", Answers: map[string]jev.Answer{}, Usage: jev.Usage{InputTokens: 1, OutputTokens: 1}}
		for id, q := range input.Questions {
			choice := "independent"
			switch id {
			case "eligibility":
				choice = "skip"
				if strings.HasPrefix(statement, "The release code is ") {
					choice = "remember"
				}
			case "memory_kind":
				choice = "none"
				if strings.HasPrefix(statement, "The release code is ") {
					choice = "project_fact"
				}
			}
			probabilities := map[string]float64{}
			for key := range q.Criteria {
				probabilities[key] = 0
			}
			probabilities[choice] = 1
			response.Answers[id] = jev.Answer{Type: "choice", Choice: choice, Confidence: &confidence, Probabilities: probabilities}
		}
		json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return server
}

func waitMemoryStatus(t *testing.T, r *offloadRig, id, want string) state.Run {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		m, err := r.store.ChannelMessage(t.Context(), id)
		if err == nil && m.RunID != "" {
			run, err := r.store.Run(t.Context(), m.RunID)
			if err == nil && run.MemoryStatus == want {
				return run
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("memory never reached %s", want)
	return state.Run{}
}

func TestOffloadMemoryBothChannelsRecallAcrossFreshNativeSessions(t *testing.T) {
	for _, kind := range []string{"dingtalk", "feishu"} {
		t.Run(kind, func(t *testing.T) {
			r := newOffloadRig(t, kind)
			var fail atomic.Bool
			server := memoryHTTPFixture(t, r, &fail)
			configureMemoryRig(t, r, server.URL, true)
			stop := startOffloadWork(t, r.service)
			defer stop()
			firstID := r.receive("remember", offloadTestOwner, "dm", "1", "The release code is ACORN-482.")
			r.waitReplies(1)
			first := waitMemoryStatus(t, r, firstID, "saved")
			message, err := r.store.ChannelMessage(t.Context(), firstID)
			if err != nil {
				t.Fatal(err)
			}
			s, err := r.service.Host.Session(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			if err = r.store.BindChannelSession(t.Context(), message.Binding, s.ID); err != nil {
				t.Fatal(err)
			}
			secondID := r.receive("recall", offloadTestOwner, "dm", "1", "What is the release code?")
			r.waitReplies(2)
			second := waitMemoryStatus(t, r, secondID, "unchanged")
			if second.SessionID == first.SessionID || second.NativeSessionID == first.NativeSessionID || len(second.Context) != 1 || second.Context[0].SourceRunID != first.ID {
				t.Fatalf("not sourced fresh-session recall: %+v", second)
			}
			if !strings.Contains(r.instructions(2), "ACORN-482") || !strings.Contains(r.instructions(2), offloadTestOwner) || strings.Contains(r.argv(2), "\n-r\n") {
				t.Fatal("native history was used instead of memory")
			}
		})
	}
}

func TestOffloadMemoryOfflineRetryDoesNotLaunchExecution(t *testing.T) {
	r := newOffloadRig(t, "dingtalk")
	var fail atomic.Bool
	fail.Store(true)
	server := memoryHTTPFixture(t, r, &fail)
	configureMemoryRig(t, r, server.URL, true)
	stop := startOffloadWork(t, r.service)
	id := r.receive("failed", offloadTestOwner, "dm", "1", "The release code is ACORN-482.")
	r.waitReplies(1)
	run := waitMemoryStatus(t, r, id, "failed")
	stop()
	if strings.Contains(run.MemoryError, "fixture-memory-key") || run.ExecutionStatus != "completed" {
		t.Fatalf("bad failure receipt: %+v", run)
	}
	// The offline command owns the writer lock rather than attaching to a
	// running channel. The fixture Store has no service writer lease.
	fail.Store(false)
	var out bytes.Buffer
	args := []string{"run", "retry-memory", "--offload-config", r.o.offloadConfig, "--home", r.store.Home(), run.ID}
	if err := Run(t.Context(), args, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result state.Run
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.MemoryStatus != "saved" || r.calls() != 1 || len(r.replies.snapshot()) != 1 {
		t.Fatalf("retry replayed a task/reply: %+v", result)
	}
}

func TestOffloadMemoryConfigurationDefaultsAndReadOnlyNeedNoKey(t *testing.T) {
	profile := offloadProfileFixture(t)
	raw, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data["memory"] = map[string]any{"enabled": true, "processing_enabled": false}
	writeBotJSON(t, profile, data)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	o := options{offloadConfig: profile}
	if err = loadExecutionOptions(&o); err != nil || o.offloadMemory == nil || o.offloadMemory.Decider != nil {
		t.Fatalf("read-only required a classifier: %+v %v", o, err)
	}
	data["memory"] = map[string]any{"enabled": true}
	writeBotJSON(t, profile, data)
	if err = loadExecutionOptions(&o); err == nil || o.offloadMemory != nil || o.offloadProfile != nil {
		t.Fatalf("missing credential not rejected atomically: %+v %v", o, err)
	}
	t.Setenv("TYPESAFE_API_KEY", "fixture-memory-key")
	if err = loadExecutionOptions(&o); err != nil || o.offloadMemory.Decider == nil || o.offloadProfile.Memory.Provider != "typesafe" {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	// Explicit processing config must not reactivate a disabled plugin.
	data["memory"] = map[string]any{"enabled": false, "provider": "unknown"}
	writeBotJSON(t, profile, data)
	t.Setenv("TYPESAFE_API_KEY", "")
	if err = loadExecutionOptions(&o); err != nil || o.offloadMemory != nil {
		t.Fatal(err)
	}
}

func TestOffloadMemoryFileBackendRequiresExplicitMigration(t *testing.T) {
	root, dir := unifiedFixture(t)
	profile := offloadProfileFixture(t)
	raw, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	json.Unmarshal(raw, &data)
	data["memory"] = map[string]any{"enabled": true, "processing_enabled": false}
	writeBotJSON(t, profile, data)
	writeBotJSON(t, filepath.Join(dir, "host.json"), unifiedHost{Version: 1, DefaultProfile: "light", Memory: &hostMemoryConfig{Backend: "files"}, Profiles: map[string]executionProfile{"light": {Execution: serviceConfig{OffloadConfig: profile}}}})
	writeBotJSON(t, filepath.Join(dir, "channels.dingtalk.json"), map[string]any{"version": 1, "enabled": true, "settings": map[string]any{"bot": map[string]any{"enabled": true, "client_id": "fixture-app", "robot_code": "fixture-robot", "client_secret_file": "credentials/bot.secret", "corp_id": "fixture-corp", "owner_user_id": "fixture-owner"}}})
	if _, _, _, err = loadService(filepath.Join(root, "channels", "dingtalk")); err == nil || !strings.Contains(err.Error(), "explicit memory migrate") {
		t.Fatalf("file memory silently fell back: %v", err)
	}
}
