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

	"agent-host/internal/harness"
	"agent-host/internal/harness/claude"
	"agent-host/internal/state"
)

// Opt in to a real native call with production model credentials, isolated state
// and tool-free execution. Channel delivery and memory admission remain fixtures;
// this test never connects to DingTalk or modifies production state.
func TestLiveNativeAfterAutomaticSessionRecovery(t *testing.T) {
	if os.Getenv("AGENT_HOST_RECOVERY_LIVE") != "1" {
		t.Skip("requires explicit live acceptance opt-in")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(home, ".agent-host", "production", "config")
	read := func(name string, value any) {
		data, err := os.ReadFile(filepath.Join(configRoot, name))
		if err != nil {
			t.Fatal("cannot read live configuration")
		}
		if json.Unmarshal(data, value) != nil {
			t.Fatal("invalid live configuration")
		}
	}
	var config struct {
		Claude string `json:"claude"`
		Model  string `json:"model"`
	}
	var environment map[string]string
	read("service.json", &config)
	read("environment.json", &environment)
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
	f.service.Timeout = 2 * time.Minute
	f.host.Config.Model = config.Model
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		return harness.Result{Status: "failed", Model: "<synthetic>", Text: "Synthetic precondition: API failure"}, errors.New("injected execution failure")
	}
	failed := f.process(t, f.message(t, "injected-failure", "Injected original task; never replay.", "text", "dm"))
	if failed.Status != "failed" {
		t.Fatal("failed-session precondition absent")
	}
	f.host.Runner = claude.New(config.Claude)
	recovered := f.process(t, f.message(t, "fresh-message", "你好，请用一句中文介绍你可以帮我做什么，不要调用工具。", "text", "dm"))
	run := f.run(t, recovered)
	if recovered.Status != "completed" || recovered.SessionID == failed.SessionID || !strings.Contains(recovered.ReplyText, "本条消息") || strings.Contains(recovered.ReplyText, "/new") || run.Output == "" {
		t.Fatalf("native recovery failed: status=%s execution=%s error=%s", recovered.Status, run.ExecutionStatus, run.Error)
	}
	continued := f.process(t, f.message(t, "next-message", "用更短的一句话回答刚才的问题，不要调用工具。", "text", "dm"))
	nextRun := f.run(t, continued)
	if continued.Status != "completed" || continued.SessionID != recovered.SessionID || strings.Contains(continued.ReplyText, "本条消息") {
		t.Fatalf("native continuation failed: status=%s error=%s", continued.Status, nextRun.Error)
	}
	if f.run(t, failed).ExecutionStatus != "failed" {
		t.Fatal("injected failure was replayed or overwritten")
	}
	report := map[string]any{"evidence_class": "injected failure, real Claude recovery/continuation; fixture channel and memory", "passed": true, "date": time.Now().UTC(), "requested_model": config.Model, "recovered_model": run.Model, "recovered_output": run.Output, "continued_output": nextRun.Output, "automatic_rotation": true, "same_session_continuation": true, "original_failure_preserved": true, "dingtalk_messages_sent": false}
	if output := os.Getenv("AGENT_HOST_RECOVERY_REPORT"); output != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real native recovery and continuation passed; requested model=%s", config.Model)
}
