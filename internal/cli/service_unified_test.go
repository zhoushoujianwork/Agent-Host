package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/knowledge"
	"agent-host/internal/state"
)

func unifiedFixture(t *testing.T) (string, string) {
	t.Helper()
	root := channelDirectoryFixture(t)
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(filepath.Join(dir, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fixture-claude")
	if err := os.WriteFile(binary, []byte("not executable code"), 0700); err != nil {
		t.Fatal(err)
	}
	execution := serviceConfig{Claude: binary, Model: "fixture-model", JevProvider: "typesafe", JevModel: "jev-latest", Timeout: "1m"}
	writeBotJSON(t, filepath.Join(dir, "host.json"), map[string]any{"version": 1, "default_profile": "default", "profiles": map[string]any{"default": executionProfile{Execution: execution, EnvironmentFile: "credentials/environment.json"}}})
	writeBotJSON(t, filepath.Join(dir, "credentials", "environment.json"), map[string]string{"TYPESAFE_API_KEY": serviceTestKey})
	if err := os.WriteFile(filepath.Join(dir, "credentials", "bot.secret"), []byte(cliBotSecret), 0600); err != nil {
		t.Fatal(err)
	}
	return root, dir
}

func TestUnifiedDingTalkIndependentFeatures(t *testing.T) {
	for _, mode := range []string{"bot", "dws", "both"} {
		t.Run(mode, func(t *testing.T) {
			root, dir := unifiedFixture(t)
			settings := map[string]any{}
			if mode != "dws" {
				settings["bot"] = map[string]any{"enabled": true, "client_id": "fixture-app", "robot_code": "fixture-robot", "client_secret_file": "credentials/bot.secret", "corp_id": "fixture-corp", "owner_user_id": "fixture-owner"}
			} else {
				settings["bot"] = map[string]any{"enabled": false, "client_secret_file": "missing"}
			}
			if mode != "bot" {
				settings["dws"] = map[string]any{"enabled": true, "profile": "fixture-corp:fixture-owner", "services": []string{"contact"}}
			} else {
				settings["dws"] = map[string]any{"enabled": false, "invalid_business_field": true}
			}
			writeBotJSON(t, filepath.Join(dir, "channels.dingtalk.json"), map[string]any{"version": 1, "enabled": true, "settings": settings})
			writeBotJSON(t, filepath.Join(dir, "plugins.email.json"), map[string]any{"version": 1, "enabled": false, "settings": map[string]any{"invalid": true}})
			// Stored source selections must not force a disabled knowledge plugin on.
			writeBotJSON(t, filepath.Join(dir, "agents.json"), map[string]any{"default_agent": "default", "agents": []map[string]any{{"id": "default", "name": "Fixture", "knowledge_sources": []string{"unused"}}}})
			var host unifiedHost
			if err := readServiceJSON(filepath.Join(dir, "host.json"), &host); err != nil {
				t.Fatal(err)
			}
			profile := host.Profiles["default"]
			profile.Execution.AgentsConfig = "agents.json"
			host.Profiles["default"] = profile
			writeBotJSON(t, filepath.Join(dir, "host.json"), host)
			channels, err := discoverUnifiedChannels(dir)
			if err != nil || len(channels) != 1 {
				t.Fatal(channels, err)
			}
			layout, c, _, err := loadService(channels[0].Root)
			if err != nil {
				t.Fatal(err)
			}
			if c.DWSOnly != (mode == "dws") || c.DisableDWS != (mode == "bot") {
				t.Fatal("incorrect features", c)
			}
			if err := prepareUnifiedRuntime(layout.Root); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runService(t.Context(), []string{"check", "--config-dir", dir}, &out, &out); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"state/state.db", "dws", "workspace/token.json"} {
				if _, err := os.Stat(filepath.Join(root, "channels", "dingtalk", path)); !os.IsNotExist(err) {
					t.Fatal("check initialized state", path, err)
				}
			}
			if mode == "dws" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := runDWSChannel(ctx, "run", layout, c, &out, &out); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUnifiedMemoryIdentitySurvivesWorkspaceChange(t *testing.T) {
	root := channelDirectoryFixture(t)
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	o := options{workspace: root, defaultScope: "retained-original-scope"}
	if err := resolveOptions(t.Context(), store, &o, false); err != nil {
		t.Fatal(err)
	}
	if o.scope != "retained-original-scope" || o.workspace != root {
		t.Fatal("cwd changed memory identity", o)
	}
	a := agentDefinition{ID: "default", Workspace: root}
	selected, err := a.options(o, "owner", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if selected.defaultScope != o.defaultScope {
		t.Fatal("agent cwd override changed stable identity")
	}
	if knowledgeValidator(options{knowledgeConfig: "must-not-load", pluginNames: []string{}}) != nil {
		t.Fatal("disabled Agent plugin retained a knowledge validator")
	}
}

func TestUnifiedPluginsSharedAndAudienceBounded(t *testing.T) {
	root, dir := unifiedFixture(t)
	writeBotJSON(t, filepath.Join(dir, "channels.dingtalk.json"), map[string]any{"version": 1, "enabled": true, "settings": map[string]any{"dws": map[string]any{"enabled": true, "profile": "corp:owner", "services": []string{"contact"}}}})
	writeBotJSON(t, filepath.Join(dir, "plugins.email.json"), map[string]any{"version": 1, "enabled": true, "access": map[string]any{"channels": []string{"dingtalk", "feishu"}, "audiences": []string{"owner"}}, "settings": map[string]any{"accounts": []map[string]any{{"id": "one", "address": "one@163.com", "provider": "163", "credential_file": "credentials/bot.secret", "send_enabled": false}}}})
	one, err := unifiedPlugin(dir, "email", "dingtalk")
	if err != nil {
		t.Fatal(err)
	}
	two, err := unifiedPlugin(dir, "email", "feishu")
	if err != nil || one != two {
		t.Fatal("mail not shared", one, two, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "knowledge"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "knowledge", "note.md"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(dir, "plugins.knowledge.json"), map[string]any{"version": 1, "enabled": true, "access": map[string]any{"channels": []string{"dingtalk"}, "audiences": []string{"owner"}}, "settings": map[string]any{"version": 1, "sources": []map[string]any{{"name": "notes", "provider": "local_markdown", "root": "../knowledge", "paths": []string{"note.md"}, "principal": "owner", "description": "notes", "projects": []string{"default"}}}}})
	if path, err := unifiedPlugin(dir, "knowledge", "feishu"); err != nil || path != "" {
		t.Fatal("unauthorized channel", path, err)
	}
	c, err := knowledge.Load(filepath.Join(dir, "plugins.knowledge.json"))
	if err != nil || len(c.Sources) != 1 || len(c.Sources[0].Audiences) != 1 || c.Sources[0].Audiences[0] != "owner" {
		t.Fatal(c, err)
	}
	var envelope map[string]any
	raw, _ := os.ReadFile(one)
	json.Unmarshal(raw, &envelope)
	envelope["access"] = map[string]any{"channels": []string{"dingtalk"}, "audiences": []string{"group"}}
	writeBotJSON(t, one, envelope)
	if _, err := unifiedPlugin(dir, "email", "dingtalk"); err == nil {
		t.Fatal("email group grant accepted")
	}
}

func TestUnifiedRejectUnknownAndEmptyFeatures(t *testing.T) {
	_, dir := unifiedFixture(t)
	path := filepath.Join(dir, "channels.dingtalk.json")
	writeBotJSON(t, path, map[string]any{"version": 1, "enabled": true, "settings": map[string]any{}})
	if _, err := discoverUnifiedChannels(dir); err == nil {
		t.Fatal("empty channel accepted")
	}
	writeBotJSON(t, path, map[string]any{"version": 1, "enabled": true, "settings": map[string]any{"dws": map[string]any{"enabled": true, "profile": "corp:owner", "services": []string{"contact"}}}})
	if err := os.WriteFile(filepath.Join(dir, "plugins.emali.json"), []byte(`{"version":1,"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverUnifiedChannels(dir); err == nil {
		t.Fatal("unknown plugin accepted")
	}
}
