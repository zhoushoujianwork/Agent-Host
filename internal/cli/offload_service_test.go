package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOffloadServiceChecksWithoutLegacyCredentialsOrEnvironment(t *testing.T) {
	for _, kind := range []string{"dingtalk", "feishu"} {
		t.Run(kind, func(t *testing.T) {
			layout, _ := newServiceFixture(t)
			profile := offloadProfileFixture(t)
			config := serviceConfig{Channel: kind, OffloadConfig: profile}
			writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
			if err := os.Remove(filepath.Join(layout.Config, "environment.json")); err != nil {
				t.Fatal(err)
			}
			_, marker := meetingCommandFixtures(t)
			if kind == "feishu" {
				cfg := meetingConfigFixture()
				cfg.MeetingID, cfg.IMChat, cfg.OwnerOpenID = "", true, "ou_owner"
				writeBotJSON(t, filepath.Join(layout.Config, "feishu.json"), cfg)
			}
			t.Setenv("TYPESAFE_API_KEY", "")
			t.Setenv("JEV_API_KEY", "")
			t.Setenv("CLAUDE_CONFIG_DIR", "/preserved/offload/native-config")
			transport := blockMeetingCheckNetwork(t)
			var out bytes.Buffer
			if err := runService(t.Context(), []string{"check", "--root", layout.Root}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			var report map[string]any
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report["engine"] != "offload" || report["memory_enabled"] != false || report["channel"] != kind || report["native_config_directory"] != nil {
				t.Fatalf("wrong execution report: %s", out.String())
			}
			if transport.calls.Load() != 0 || os.Getenv("CLAUDE_CONFIG_DIR") != "/preserved/offload/native-config" {
				t.Fatal("check used the network or changed Offload's native environment")
			}
			for _, path := range []string{marker, filepath.Join(layout.State, "state.db")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("check launched a process or initialized execution state: %s (%v)", path, err)
				}
			}
		})
	}
}

func TestOffloadServiceRejectsMixedExecutionAndUnsupportedCapabilities(t *testing.T) {
	profile := offloadProfileFixture(t)
	for _, mutate := range []func(*serviceConfig){
		func(c *serviceConfig) { c.ExecutionConfig = "/acp.json" },
		func(c *serviceConfig) { c.Claude = "/claude" },
		func(c *serviceConfig) { c.Model = "legacy-model" },
		func(c *serviceConfig) { c.JevProvider = "typesafe" },
		func(c *serviceConfig) { c.NoTools = true },
		func(c *serviceConfig) { c.AllowedTools = []string{"Read"} },
		func(c *serviceConfig) { c.AgentsConfig = "/agents.json" },
		func(c *serviceConfig) { c.KnowledgeConfig = "/knowledge.json" },
		func(c *serviceConfig) { c.DiscoveryConfig = "/discovery.json" },
		func(c *serviceConfig) { c.Channel = "feishu-meeting" },
	} {
		c := serviceConfig{OffloadConfig: profile}
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Fatalf("unsupported configuration accepted: %+v", c)
		}
	}
}

func TestOffloadUnifiedProfileNeedsNoProviderEnvironmentOrMemoryMigration(t *testing.T) {
	root, directory := unifiedFixture(t)
	profile := offloadProfileFixture(t)
	writeBotJSON(t, filepath.Join(directory, "host.json"), unifiedHost{Version: 1, DefaultProfile: "light",
		Memory:   &hostMemoryConfig{Backend: "files"},
		Profiles: map[string]executionProfile{"light": {Execution: serviceConfig{OffloadConfig: profile}}}})
	writeBotJSON(t, filepath.Join(directory, "channels.dingtalk.json"), map[string]any{
		"version": 1, "enabled": true, "settings": map[string]any{
			"bot": map[string]any{"enabled": true, "client_id": "fixture-app", "robot_code": "fixture-robot", "client_secret_file": "credentials/bot.secret", "corp_id": "fixture-corp", "owner_user_id": "fixture-owner"},
			"dws": map[string]any{"enabled": false},
		}})
	writeBotJSON(t, filepath.Join(directory, "datasource.plugins.email.json"), map[string]any{"version": 1, "enabled": false, "settings": map[string]any{"invalid": true}})
	if err := os.Remove(filepath.Join(directory, "credentials", "environment.json")); err != nil {
		t.Fatal(err)
	}
	layout, config, env, err := loadService(filepath.Join(root, "channels", "dingtalk"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 || config.SkillsRoot != "" || config.OffloadConfig != profile {
		t.Fatalf("legacy dependencies mounted: %+v %v", config, env)
	}
	args := strings.Join(config.arguments(layout, "check"), " ")
	if !strings.Contains(args, "--offload-config "+profile) || strings.Contains(args, "--native-profile") || strings.Contains(args, "--jev") || strings.Contains(args, "--claude") {
		t.Fatal(args)
	}
	if err := prepareUnifiedRuntime(layout.Root); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runService(t.Context(), []string{"check", "--config-dir", directory}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Disabled channel settings are not loaded and cannot start workers.
	writeBotJSON(t, filepath.Join(directory, "channels.feishu.json"), map[string]any{"version": 1, "enabled": false, "settings": map[string]any{"invalid": true}})
	channels, err := discoverUnifiedChannels(directory)
	if err != nil || len(channels) != 1 || channels[0].Name != "dingtalk" {
		t.Fatalf("disabled channel was activated: %+v %v", channels, err)
	}
}

func TestOffloadManagedNoWorkbenchSkipsViewerAndPython(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(map[bool]string{false: "channels-dir", true: "config-dir"}[unified], func(t *testing.T) {
			layout, _ := newServiceFixture(t)
			writeBotJSON(t, filepath.Join(layout.Config, "service.json"), serviceConfig{OffloadConfig: offloadProfileFixture(t)})
			if err := os.Remove(filepath.Join(layout.Config, "environment.json")); err != nil {
				t.Fatal(err)
			}
			directory := channelDirectoryFixture(t)
			writeBotJSON(t, filepath.Join(directory, "channel-one.json"), map[string]any{"enabled": true, "runtime_root": layout.Root})
			args := []string{"run", "--channels-dir", directory, "--no-workbench", "--viewer-source", "/missing/assets", "--viewer-python", "/missing/python"}
			if unified {
				_, configDir := unifiedFixture(t)
				writeBotJSON(t, filepath.Join(configDir, "host.json"), unifiedHost{Version: 1, DefaultProfile: "light", Profiles: map[string]executionProfile{"light": {Execution: serviceConfig{OffloadConfig: offloadProfileFixture(t)}}}})
				writeBotJSON(t, filepath.Join(configDir, "channels.dingtalk.json"), map[string]any{"version": 1, "enabled": true, "settings": map[string]any{"bot": map[string]any{"enabled": true, "client_id": "fixture-app", "robot_code": "fixture-robot", "client_secret_file": "credentials/bot.secret", "corp_id": "fixture-corp", "owner_user_id": "fixture-owner"}}})
				args = []string{"run", "--config-dir", configDir, "--no-workbench", "--viewer-source", "/missing/assets", "--viewer-python", "/missing/python"}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // preflight still runs, but no child channel process may start.
			if err := runService(ctx, args, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}
