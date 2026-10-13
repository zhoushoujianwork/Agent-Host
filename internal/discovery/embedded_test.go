package discovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/dingtalkworkspace"
)

func TestEmbeddedCollectionPreservesSelectorsAndPartialEvidence(t *testing.T) {
	cfg := fixtureConfig(t)
	cfg.DWSExecutable = ""
	root := t.TempDir()
	cfg.DingTalkWorkspaceConfig = filepath.Join(root, "workspace.json")
	workspace := dingtalkworkspace.Config{Profile: cfg.Profile, StateDirectory: filepath.Join(root, "state"), Services: []string{"contact", "todo"}}
	raw, _ := json.Marshal(workspace)
	os.WriteFile(cfg.DingTalkWorkspaceConfig, raw, 0600)
	if err := cfg.Check(testContext, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	window := Range{now.Add(-24 * time.Hour), now}
	p := NewDWSProvider(cfg)
	queries := 0
	partial := false
	p.run = func(ctx context.Context, r dingtalkworkspace.Request) (json.RawMessage, error) {
		if r.Config != cfg.DingTalkWorkspaceConfig || r.Fingerprint == "" {
			t.Fatal("missing embedded binding")
		}
		if r.Args[0] == "profile" {
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
		}
		queries++
		joined := strings.Join(r.Args, " ")
		for _, expected := range []string{"chat +chat-messages", "--profile corp:owner", "--open-dingtalk-id peer-id", "--no-reactions", "--page-all"} {
			if !strings.Contains(joined, expected) {
				t.Fatal("source selection changed", joined)
			}
		}
		if strings.Contains(joined, "--yes") || strings.Contains(joined, "send") {
			t.Fatal("collection requested an action")
		}
		if partial {
			return failureEnvelopeFixture(t, cfg, window), &dingtalkworkspace.CommandFailure{Code: 5}
		}
		return envelopeFixture(cfg, window, true), nil
	}
	result, err := p.Fetch(testContext, cfg.Sources[0], window)
	if err != nil || len(result.Evidence) != 1 || !result.Coverage.Complete {
		t.Fatal("embedded source evidence lost", result, err)
	}
	partial = true
	result, err = p.Fetch(testContext, cfg.Sources[0], window)
	if err == nil || len(result.Evidence) != 1 || result.Coverage.Complete || !result.Coverage.Partial {
		t.Fatal("partial source evidence hidden", result, err)
	}
	workspace.Profile = "other:owner"
	raw, _ = json.Marshal(workspace)
	os.WriteFile(cfg.DingTalkWorkspaceConfig, raw, 0600)
	if _, err = p.Fetch(testContext, cfg.Sources[0], window); err == nil || queries != 2 {
		t.Fatal("identity drift triggered a source read", err)
	}
	workspace.Profile = cfg.Profile
	raw, _ = json.Marshal(workspace)
	os.WriteFile(cfg.DingTalkWorkspaceConfig, raw, 0600)
	cfg.DWSExecutable = os.Args[0]
	if err := cfg.Check(testContext, nil); err == nil {
		t.Fatal("dual backends allowed implicit fallback")
	}
}
