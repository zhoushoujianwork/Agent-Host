package channel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/dingtalkworkspace"
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
