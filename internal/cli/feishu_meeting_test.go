package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func meetingConfigFixture() feishuMeetingConfigFile {
	return feishuMeetingConfigFile{
		Profile: "fixture-meeting", AppID: "cli_fixture", BotOpenID: "ou_bot",
		MeetingID: "700000000000000001", TenantID: "fixture-tenant",
		AllowedSenderOpenIDs: []string{"ou_human"}, PollIntervalSeconds: 5,
	}
}

// A regression may only invoke these local fixtures, never the user's CLI.
func meetingCommandFixtures(t *testing.T) (native, marker string) {
	t.Helper()
	bin := t.TempDir()
	marker = filepath.Join(bin, "unexpected-process")
	t.Setenv("AGENT_HOST_MEETING_TEST_CALLS", marker)
	t.Setenv("PATH", bin)
	for _, name := range []string{"lark-cli", "fixture-claude"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' invoked >> \"$AGENT_HOST_MEETING_TEST_CALLS\"\nexit 91\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(bin, "fixture-claude"), marker
}

func blockMeetingCheckNetwork(t *testing.T) *rejectingHTTPTransport {
	t.Helper()
	transport := &rejectingHTTPTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
	return transport
}

func assertMeetingLocalReport(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"status": "local_configuration_valid", "transport": "feishu_meeting_cli_poll",
		"scope": "allowlisted_addressed_meeting_text", "platform_verified": false,
		"native_auth_verified": false, "voice_supported": false,
	} {
		if report[key] != want {
			t.Fatalf("local check report %s = %v, want %v", key, report[key], want)
		}
	}
	return report
}

func TestFeishuMeetingCheckDoesNotCreateStateInvokeProcessesOrConnect(t *testing.T) {
	root := t.TempDir()
	config, agents := filepath.Join(root, "meeting.json"), filepath.Join(root, "agents.json")
	cfg := meetingConfigFixture()
	writeBotJSON(t, config, cfg)
	writeBotJSON(t, agents, agentCatalog{DefaultAgent: "helper", Agents: []agentDefinition{{ID: "helper", Name: "Meeting helper", Projects: []string{}, KnowledgeSources: []string{}}}})
	native, marker := meetingCommandFixtures(t)
	t.Setenv("TYPESAFE_API_KEY", serviceTestKey)
	t.Setenv("JEV_API_KEY", "")
	transport := blockMeetingCheckNetwork(t)
	home, profile := filepath.Join(root, "uncreated-state"), filepath.Join(root, "uncreated-native")
	var out, errout bytes.Buffer
	err := Run(context.Background(), []string{"feishu-meeting", "check", "--config", config, "--agents-config", agents,
		"--home", home, "--native-profile", profile, "--workspace", root, "--claude", native,
		"--model", "fixture-model", "--jev-provider", "typesafe"}, nil, &out, &errout)
	if err != nil {
		t.Fatal(err)
	}
	report := assertMeetingLocalReport(t, out.Bytes())
	if report["agents"] != float64(1) || report["model"] != "fixture-model" {
		t.Fatalf("configured runtime missing: %v", report)
	}
	for _, path := range []string{home, profile, marker} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("local check created state or invoked a subprocess: %s: %v", filepath.Base(path), err)
		}
	}
	if transport.calls.Load() != 0 {
		t.Fatal("local check contacted a network service")
	}
	for _, private := range []string{serviceTestKey, cfg.Profile, cfg.AppID, cfg.BotOpenID, cfg.MeetingID, cfg.TenantID, cfg.AllowedSenderOpenIDs[0]} {
		if strings.Contains(out.String()+errout.String(), private) {
			t.Fatal("local check exposed private credentials or platform identity")
		}
	}
}

func TestFeishuMeetingConfigurationIsPrivateStrictAndDurationBounded(t *testing.T) {
	valid, err := json.Marshal(meetingConfigFixture())
	if err != nil {
		t.Fatal(err)
	}
	maxSeconds := int64((1<<63 - 1) / time.Second)
	for name, raw := range map[string]string{
		"unknown key":       strings.TrimSuffix(string(valid), "}") + `,"app_secret":"` + serviceTestKey + `"}`,
		"duplicate key":     strings.TrimSuffix(string(valid), "}") + `,"PROFILE":"other"}`,
		"trailing value":    string(valid) + `{}`,
		"null":              "null",
		"short poll":        strings.Replace(string(valid), `"poll_interval_seconds":5`, `"poll_interval_seconds":4`, 1),
		"negative poll":     strings.Replace(string(valid), `"poll_interval_seconds":5`, `"poll_interval_seconds":-1`, 1),
		"fractional poll":   strings.Replace(string(valid), `"poll_interval_seconds":5`, `"poll_interval_seconds":5.5`, 1),
		"duration overflow": strings.Replace(string(valid), `"poll_interval_seconds":5`, fmt.Sprintf(`"poll_interval_seconds":%d`, maxSeconds+1), 1),
		"integer overflow":  strings.Replace(string(valid), `"poll_interval_seconds":5`, `"poll_interval_seconds":9223372036854775808`, 1),
		"missing tenant":    strings.Replace(string(valid), `"tenant_id":"fixture-tenant"`, `"tenant_id":""`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meeting.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadFeishuMeetingConfig(path); err == nil || strings.Contains(err.Error(), serviceTestKey) {
				t.Fatalf("invalid config accepted or secret disclosed: %v", err)
			}
		})
	}
	for _, mutation := range []string{"public", "symlink", "directory", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meeting.json")
			writeBotJSON(t, path, meetingConfigFixture())
			switch mutation {
			case "public":
				err = os.Chmod(path, 0644)
			case "symlink":
				if err = os.Rename(path, path+".original"); err == nil {
					err = os.Symlink(path+".original", path)
				}
			case "directory", "missing":
				if err = os.Remove(path); err == nil && mutation == "directory" {
					err = os.Mkdir(path, 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadFeishuMeetingConfig(path); err == nil {
				t.Fatal("accepted an unavailable or unprotected meeting config")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "meeting.json")
	cfg := meetingConfigFixture()
	cfg.PollIntervalSeconds = maxSeconds
	writeBotJSON(t, path, cfg)
	loaded, err := loadFeishuMeetingConfig(path)
	if err != nil || loaded.PollInterval != time.Duration(maxSeconds)*time.Second || loaded.PollInterval <= 0 {
		t.Fatalf("largest whole-second duration rejected or wrapped: %v", err)
	}
}

func TestFeishuMeetingManagedCheckDispatchesWithoutDingTalkFiles(t *testing.T) {
	layout, config := newServiceFixture(t)
	native, marker := meetingCommandFixtures(t)
	config.Channel, config.Claude = state.ReplyTargetFeishuMeeting, native
	writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
	writeBotJSON(t, filepath.Join(layout.Config, "feishu-meeting.json"), meetingConfigFixture())
	for _, name := range []string{"dingtalk.json", "dingtalk.secret"} {
		if err := os.Remove(filepath.Join(layout.Config, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TYPESAFE_API_KEY", "inherited-key-must-not-use")
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited/native")
	transport := blockMeetingCheckNetwork(t)
	var out, errout bytes.Buffer
	if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, &out, &errout); err != nil {
		t.Fatal(err)
	}
	report := assertMeetingLocalReport(t, out.Bytes())
	if report["runtime_root"] != layout.Root || report["native_config_directory"] != layout.Native {
		t.Fatal("managed check did not preserve its installed runtime")
	}
	for _, path := range []string{layout.State, layout.Workspace, layout.Native} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("managed local check wrote runtime files: %s: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) || transport.calls.Load() != 0 {
		t.Fatal("managed local check invoked a subprocess or contacted the network")
	}
	if os.Getenv("TYPESAFE_API_KEY") != "inherited-key-must-not-use" || os.Getenv("CLAUDE_CONFIG_DIR") != "/inherited/native" {
		t.Fatal("managed check did not restore its caller's environment")
	}
	for _, private := range []string{serviceTestKey, "inherited-key-must-not-use", meetingConfigFixture().AppID} {
		if strings.Contains(out.String()+errout.String(), private) {
			t.Fatal("managed check disclosed private configuration")
		}
	}
	// A configured channel must not fall back to another platform when invalid.
	for _, mutate := range []func(*serviceConfig){
		func(c *serviceConfig) { c.Channel = "feishu" },
		func(c *serviceConfig) { c.DiscoveryConfig = filepath.Join(layout.Config, "discovery.json") },
	} {
		invalid := config
		mutate(&invalid)
		writeBotJSON(t, filepath.Join(layout.Config, "service.json"), invalid)
		if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("managed check accepted incompatible platform configuration")
		}
	}
}

func TestFeishuMeetingAgentSelectionSharesOnlyTheMeetingAudience(t *testing.T) {
	c := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "General"}, {ID: "team", Name: "Team"}},
		GroupBindings: []agentBinding{{ConversationID: "700000000000000001", AgentID: "team"}}}
	m := channel.Message{ConversationID: "700000000000000001", ConversationType: "2", TenantID: "tenant", SenderID: "ou_first"}
	first, err := c.selectMeetingAgent("cli_app", m)
	if err != nil || first.AgentID != "team" || first.AudienceID == "owner" {
		t.Fatalf("meeting binding: %+v %v", first, err)
	}
	m.SenderID = "ou_second"
	second, err := c.selectMeetingAgent("cli_app", m)
	if err != nil || second != first {
		t.Fatalf("meeting participants did not share their audience: %+v %v", second, err)
	}
	for _, changed := range []struct{ account, tenant, meeting string }{
		{"cli_other", m.TenantID, m.ConversationID},
		{"cli_app", "other-tenant", m.ConversationID},
		{"cli_app", m.TenantID, "700000000000000002"},
	} {
		other := m
		other.TenantID, other.ConversationID = changed.tenant, changed.meeting
		selected, err := c.selectMeetingAgent(changed.account, other)
		if err != nil || selected.AudienceID == first.AudienceID || selected.AudienceID == "owner" {
			t.Fatalf("meeting partition collision: %+v %v", selected, err)
		}
	}
	for _, kind := range []string{"1", "2"} {
		ding, err := c.selectAgent("cli_app", dingtalk.Message{ConversationType: kind, ConversationID: m.ConversationID, TenantID: m.TenantID})
		if err != nil || ding.AudienceID == first.AudienceID {
			t.Fatalf("DingTalk or owner audience reused by a meeting: %+v %v", ding, err)
		}
	}
}

func TestFeishuMeetingAgentRuntimeRejectsCrossPlatformAndPrivateBindings(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := filepath.Join(root, "native")
	if err := os.Mkdir(native, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"settings.json": "{}", "CLAUDE.md": "private role", "private-history.json": "private history"} {
		if err := os.WriteFile(filepath.Join(native, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{home: filepath.Join(root, "state"), workspace: root, defaultWorkspace: root, defaultScope: root, nativeProfile: native, binary: "/bin/echo", model: "fixture-model"}
	catalog := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Meeting assistant", role: "Help meeting participants."}}}
	parent := &channel.Service{Store: store, Channel: state.ReplyTargetFeishuMeeting, ClientID: "cli_app", Profile: "meeting-profile"}
	message := state.ChannelMessage{Channel: state.ReplyTargetFeishuMeeting, AccountID: parent.ClientID, AgentID: "default", ConversationType: "2", TenantID: "tenant", ConversationID: "700000000000000001"}
	message.AudienceID = channel.Key("group-audience-v1", parent.Channel, parent.ClientID, message.TenantID, message.ConversationID)
	child, err := resolveAgentChannel(ctx, parent, catalog, base, client, nil, message)
	if err != nil {
		t.Fatal(err)
	}
	h := child.Host.(*host.Host)
	if h.Config.AudienceID != message.AudienceID || h.Config.NativeProfile == native || !strings.Contains(h.Config.HostInstructions, "public to every participant in the current Feishu meeting") || !strings.Contains(h.Config.HostInstructions, "Help meeting participants.") {
		t.Fatal("meeting runtime did not retain public, isolated audience policy")
	}
	entries, err := os.ReadDir(h.Config.NativeProfile)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("meeting profile inherited owner instructions or history: %+v %v", entries, err)
	}
	for name, mutate := range map[string]func(*state.ChannelMessage){
		"DingTalk audience": func(m *state.ChannelMessage) {
			m.AudienceID = channel.Key("group-audience-v1", "dingtalk", parent.ClientID, m.TenantID, m.ConversationID)
		},
		"DingTalk source":  func(m *state.ChannelMessage) { m.Channel = "dingtalk" },
		"another app":      func(m *state.ChannelMessage) { m.AccountID = "cli_other" },
		"another tenant":   func(m *state.ChannelMessage) { m.TenantID = "other" },
		"another meeting":  func(m *state.ChannelMessage) { m.ConversationID = "700000000000000002" },
		"owner audience":   func(m *state.ChannelMessage) { m.AudienceID = "owner" },
		"missing audience": func(m *state.ChannelMessage) { m.AudienceID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := message
			mutate(&invalid)
			if _, err := resolveAgentChannel(ctx, parent, catalog, base, client, nil, invalid); err == nil {
				t.Fatal("accepted a message outside the pinned meeting audience")
			}
		})
	}
	// A valid meeting audience cannot be resumed through the DingTalk service.
	dingParent := *parent
	dingParent.Channel = "dingtalk"
	if _, err := resolveAgentChannel(ctx, &dingParent, catalog, base, client, nil, message); err == nil {
		t.Fatal("DingTalk runtime accepted a Feishu meeting audience")
	}
}
