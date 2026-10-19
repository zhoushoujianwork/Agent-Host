package channel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/dingtalkworkspace"
	"agent-host/internal/harness"
	"agent-host/internal/harness/claude"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

// Opt-in native acceptance uses isolated state and synthetic card content by
// default. Explicit message/conversation IDs select one real card read instead.
// Delivery and memory are fixtures; no DingTalk messages are sent.
func TestForwardedCardNativeLive(t *testing.T) {
	root := os.Getenv("AGENT_HOST_CARD_LIVE_RUNTIME")
	if root == "" {
		t.Skip("set AGENT_HOST_CARD_LIVE_RUNTIME to opt in to native card acceptance")
	}
	var config struct {
		Profiles map[string]struct {
			EnvironmentFile string `json:"environment_file"`
			Execution       struct {
				Claude string `json:"claude"`
				Model  string `json:"model"`
			} `json:"execution"`
		} `json:"profiles"`
	}
	read := func(path string, target any) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, target) != nil {
			t.Fatal("cannot load native acceptance configuration")
		}
	}
	read(filepath.Join(root, "config/host.json"), &config)
	profile := config.Profiles["dingtalk"]
	var environment map[string]string
	read(filepath.Join(root, "config", profile.EnvironmentFile), &environment)
	for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDECODE"} {
		t.Setenv(key, environment[key])
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	f := newServiceFixture(t)
	workspace, err := state.CanonicalWorkspace(f.host.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	f.host.Config.Scope, f.service.Scope = workspace, workspace
	f.host.Config.Model = profile.Execution.Model
	f.host.Config.HostInstructions = host.ChannelInstructions("", false)
	f.service.Timeout = 2 * time.Minute
	f.host.Runner = claude.New(profile.Execution.Claude)
	input := inputCallback(t, f, "synthetic-forwarded-card", "interactiveCard", "", map[string]any{
		"title": "Synthetic network approval", "cardContent": []any{
			map[string]string{"elementType": "TEXT", "value": "Approval CARD-731: allow buildhub-test to connect to 192.0.2.10 on TCP port 3306. Requested by Alice. Awaiting review."},
		}}, nil)
	expected := []string{"CARD-731", "3306"}
	if id := os.Getenv("AGENT_HOST_CARD_LIVE_MESSAGE_ID"); id != "" {
		path := filepath.Join(root, "config/channels.dingtalk.json")
		c, fp, err := dingtalkworkspace.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		s := dingtalkworkspace.Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: dingtalkworkspace.Process(filepath.Join(root, "bin/agent-host"))}
		f.service.OwnerID = f.config.OwnerUserID
		f.service.ReadForwardedCard = func(ctx context.Context, m state.ChannelMessage) (string, error) {
			return s.ReadForwardedCard(ctx, m.ConversationID, m.MessageID)
		}
		input = inputCallback(t, f, id, "interactiveCard", "", nil, nil)
		input.ConversationID = os.Getenv("AGENT_HOST_CARD_LIVE_CONVERSATION_ID")
		expected = []string{os.Getenv("AGENT_HOST_CARD_LIVE_EXPECT")}
	}
	m := f.process(t, input)
	run := f.run(t, m)
	if m.Status != "completed" || m.ReplyStatus != "accepted" || strings.TrimSpace(run.Output) == "" {
		t.Fatalf("native card reading failed: status=%s execution=%s error=%s output=%s", m.Status, run.ExecutionStatus, run.Error, run.Output)
	}
	for _, text := range expected {
		if text == "" || !strings.Contains(run.Output, text) {
			t.Fatalf("native card interpretation omitted expected text %q: %s", text, run.Output)
		}
	}
	if !strings.ContainsAny(run.Output, "?？") {
		t.Fatalf("forwarded approval was not followed by a handling question: %s", run.Output)
	}
	t.Logf("Forwarded card read by native model %s; isolated state and fixture delivery; output: %s", run.Model, run.Output)
}

// This smoke check uses real native processes and synthetic card/reply adapters;
// it never reads a live ticket, sends a platform message or performs business work.
func TestPrivateCardConcurrentNativeLive(t *testing.T) {
	root := os.Getenv("AGENT_HOST_CARD_LIVE_RUNTIME")
	if root == "" {
		t.Skip("set AGENT_HOST_CARD_LIVE_RUNTIME for native concurrency acceptance")
	}
	var cfg struct {
		Profiles map[string]struct {
			EnvironmentFile string `json:"environment_file"`
			Execution       struct {
				Claude string `json:"claude"`
				Model  string `json:"model"`
			} `json:"execution"`
		} `json:"profiles"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "config", "host.json"))
	if err != nil || json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("cannot load native execution configuration")
	}
	profile := cfg.Profiles["dingtalk"]
	path := profile.EnvironmentFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, "config", path)
	}
	raw, err = os.ReadFile(path)
	var env map[string]string
	if err != nil || json.Unmarshal(raw, &env) != nil {
		t.Fatal("cannot load native environment")
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	f := newConcurrentFixture(t)
	f.service.MaxConcurrentConversations = 2
	f.service.Timeout = 2 * time.Minute
	native := filepath.Join(t.TempDir(), "native")
	if err := os.MkdirAll(native, 0700); err != nil {
		t.Fatal(err)
	}
	type interval struct{ start, end time.Time }
	spans := make(chan interval, 2)
	adapter := claude.New(profile.Execution.Claude)
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		req.Model = profile.Execution.Model
		req.NativeProfile = native
		req.ToolsDisabled = true
		req.HostInstructions += "\nThis is a synthetic read-only concurrency check. Include the exact card title marker in your reply. No real business operation is requested."
		start := time.Now()
		result, err := adapter.Run(ctx, req, nil)
		spans <- interval{start, time.Now()}
		return result, err
	}
	for _, marker := range []string{"CONCURRENT_ALPHA", "CONCURRENT_BRAVO"} {
		f.receive(t, inputCallback(t, f.serviceFixture, marker, "interactiveCard", "", map[string]any{"title": marker, "cardContent": []any{map[string]string{"elementType": "TEXT", "value": "Synthetic fixture ticket only. No real resource or operation."}}}, nil))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	delivered := make(chan string, 2)
	f.replier.reply = func(_ context.Context, call replyCall) (string, error) { delivered <- call.url; return "accepted", nil }
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	for range 2 {
		waitForWorkerSignal(t, ctx, delivered)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a, b := <-spans, <-spans
	if !a.start.Before(b.end) || !b.start.Before(a.end) {
		t.Fatal("native execution intervals did not overlap")
	}
	drainReactions(t, f.service)
	for _, marker := range []string{"CONCURRENT_ALPHA", "CONCURRENT_BRAVO"} {
		message := f.receipt(t, marker)
		run := f.run(t, message)
		if run.ExecutionStatus != "completed" || !strings.Contains(run.Output, marker) || run.MemoryStatus != "skipped" {
			t.Fatalf("native card result failed: execution=%s memory=%s error=%s output=%s", run.ExecutionStatus, run.MemoryStatus, run.Error, run.Output)
		}
		rows, err := f.service.Store.ChannelReactions(t.Context(), message.ID)
		if err != nil || len(rows) == 0 || rows[len(rows)-1].Stage != "completed" || rows[len(rows)-1].Status != "accepted" {
			t.Fatal("card feedback incomplete", rows, err)
		}
	}
	t.Log("Two real native card runs overlapped; synthetic feedback reached completed, with no platform sends.")
}
