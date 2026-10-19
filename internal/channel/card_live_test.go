package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness/claude"
	"agent-host/internal/state"
)

// Opt-in native acceptance uses synthetic card content and isolated state.
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
	f.service.Timeout = 2 * time.Minute
	f.host.Runner = claude.New(profile.Execution.Claude)
	m := f.process(t, inputCallback(t, f, "synthetic-forwarded-card", "interactiveCard", "", map[string]any{
		"title": "Synthetic network approval", "cardContent": []any{
			map[string]string{"elementType": "TEXT", "value": "Approval CARD-731: allow buildhub-test to connect to 192.0.2.10 on TCP port 3306. Requested by Alice. Awaiting review."},
		}}, nil))
	run := f.run(t, m)
	if m.Status != "completed" || m.ReplyStatus != "accepted" || !strings.Contains(run.Output, "CARD-731") || !strings.Contains(run.Output, "3306") {
		t.Fatalf("native card reading failed: status=%s execution=%s error=%s output=%s", m.Status, run.ExecutionStatus, run.Error, run.Output)
	}
	t.Logf("Synthetic forwarded card read by native model %s; fixture delivery; output: %s", run.Model, run.Output)
}
