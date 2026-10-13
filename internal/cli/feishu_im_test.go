package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/decision/jev"
	"agent-host/internal/event"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

func TestFeishuManagedIMCheckNeedsNoMeetingDingTalkOrNetwork(t *testing.T) {
	layout, config := newServiceFixture(t)
	native, marker := meetingCommandFixtures(t)
	config.Channel, config.Claude = state.ReplyTargetFeishu, native
	writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
	cfg := meetingConfigFixture()
	cfg.MeetingID, cfg.IMChat, cfg.OwnerOpenID = "", true, "ou_human"
	cfg.IMReactions = true
	writeBotJSON(t, filepath.Join(layout.Config, "feishu.json"), cfg)
	for _, name := range []string{"dingtalk.json", "dingtalk.secret"} {
		if err := os.Remove(filepath.Join(layout.Config, name)); err != nil {
			t.Fatal(err)
		}
	}
	transport := blockMeetingCheckNetwork(t)
	var out, errout bytes.Buffer
	if err := Run(t.Context(), []string{"service", "check", "--root", layout.Root}, nil, &out, &errout); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["transport"] != "feishu_cli_events" || report["im_chat_enabled"] != true || report["im_reactions_enabled"] != true || report["auto_join_enabled"] != false || report["platform_verified"] != false {
		t.Fatal("IM-only check reported a meeting-only or platform-verified transport", report)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) || transport.calls.Load() != 0 {
		t.Fatal("local IM check invoked a subprocess or network")
	}
	for _, path := range []string{layout.State, layout.Native, layout.Workspace} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatal("local IM check mutated runtime state")
		}
	}
	cfg.TenantID = "invalid tenant"
	writeBotJSON(t, filepath.Join(layout.Config, "feishu.json"), cfg)
	if _, err := loadFeishuMeetingConfig(filepath.Join(layout.Config, "feishu.json")); err == nil {
		t.Fatal("local configuration bypassed IM tenant validation")
	}
	cfg.TenantID, cfg.IMChat = "fixture-tenant", false
	writeBotJSON(t, filepath.Join(layout.Config, "feishu.json"), cfg)
	if _, err := loadFeishuMeetingConfig(filepath.Join(layout.Config, "feishu.json")); err == nil {
		t.Fatal("progress reactions were enabled without IM intake")
	}
}

func TestCombinedFeishuAgentResolverUsesActualSourceAudienceAndPolicy(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := filepath.Join(root, "native")
	if err := os.Mkdir(native, 0700); err != nil {
		t.Fatal(err)
	}
	client, err := jev.New("synthetic-not-used", "jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	base := options{home: filepath.Join(root, "state"), workspace: root, defaultWorkspace: root, defaultScope: root, nativeProfile: native, binary: "/bin/echo", model: "fixture-model"}
	catalog := agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Assistant"}, {ID: "team", Name: "Team"}}, GroupBindings: []agentBinding{{ConversationID: "oc_group", AgentID: "team"}}}
	parent := &channel.Service{Store: store, Channel: state.ReplyTargetFeishu, ClientID: "cli_fixture", Profile: "shared-profile"}
	profiles := map[string]string{}
	for _, tc := range []struct{ name, provider, kind, conversation, phrase string }{
		{"private", state.ReplyTargetFeishu, "1", "oc_private", "authenticated owner in a private conversation"},
		{"group", state.ReplyTargetFeishu, "2", "oc_group", "public to every member of the current enterprise group"},
		{"meeting", state.ReplyTargetFeishuMeeting, "2", "700000000000000001", "public to every participant in the current Feishu meeting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incoming := channel.Message{ConversationType: tc.kind, ConversationID: tc.conversation, TenantID: "tenant", ReplyTarget: state.ChannelReplyTarget{Kind: tc.provider}}
			var selected channel.AgentSelection
			if tc.provider == state.ReplyTargetFeishuMeeting {
				selected, err = catalog.selectMeetingAgent(parent.ClientID, incoming)
			} else {
				selected, err = catalog.selectFeishuAgent(parent.ClientID, incoming)
			}
			if err != nil || tc.name == "group" && selected.AgentID != "team" || tc.name == "private" && selected.AudienceID != state.OwnerAudienceID {
				t.Fatal("wrong configured agent or audience", selected, err)
			}
			message := state.ChannelMessage{Channel: tc.provider, AccountID: parent.ClientID, AgentID: selected.AgentID, AudienceID: selected.AudienceID, ConversationType: tc.kind, ConversationID: tc.conversation, TenantID: "tenant"}
			child, err := resolveAgentChannel(t.Context(), parent, catalog, base, client, nil, message)
			if err != nil {
				t.Fatal(err)
			}
			h := child.Host.(*host.Host)
			if h.Config.AudienceID != selected.AudienceID || !strings.Contains(h.Config.HostInstructions, tc.phrase) {
				t.Fatal("actual source policy was lost in the combined worker")
			}
			if tc.kind == "2" && h.Config.NativeProfile == native {
				t.Fatal("shared audience reused private native history")
			}
			profiles[tc.name] = h.Config.NativeProfile
			for _, mutate := range []func(*state.ChannelMessage){
				func(m *state.ChannelMessage) { m.AccountID = "cli_other" },
				func(m *state.ChannelMessage) { m.Channel = "dingtalk" },
				func(m *state.ChannelMessage) { m.AudienceID = "another-audience" },
			} {
				invalid := message
				mutate(&invalid)
				if _, err := resolveAgentChannel(t.Context(), parent, catalog, base, client, nil, invalid); err == nil {
					t.Fatal("combined resolver admitted a foreign source or audience")
				}
			}
		})
	}
	if profiles["private"] == profiles["group"] || profiles["private"] == profiles["meeting"] || profiles["group"] == profiles["meeting"] {
		t.Fatal("native histories were shared across audiences")
	}
}

func TestFeishuReplyDispatchIsPinnedToActualTransport(t *testing.T) {
	im, meeting := &countingMeetingReplier{}, &countingMeetingReplier{}
	r := feishuTextReplier{IM: im, Meeting: meeting}
	for _, kind := range []string{state.ReplyTargetFeishu, state.ReplyTargetFeishuMeeting} {
		if status, err := r.ReplyText(t.Context(), state.ChannelReplyTarget{Kind: kind}, "synthetic"); status != feishumeeting.Accepted || err != nil {
			t.Fatal(status, err)
		}
	}
	if im.calls != 1 || meeting.calls != 1 {
		t.Fatal("a source used both reply adapters")
	}
	for _, kind := range []string{"dingtalk", ""} {
		if status, err := r.ReplyText(t.Context(), state.ChannelReplyTarget{Kind: kind}, "synthetic"); status != feishumeeting.Failed || err == nil {
			t.Fatal("a foreign source acquired a Feishu reply adapter")
		}
	}
	if status, err := (feishuTextReplier{IM: im}).ReplyText(t.Context(), state.ChannelReplyTarget{Kind: state.ReplyTargetFeishuMeeting}, "synthetic"); status != feishumeeting.Failed || err == nil || im.calls != 1 {
		t.Fatal("a missing meeting transport fell back to IM")
	}
}

func TestFeishuIMOnlyRuntimeUsesOneSubscriptionAndReplyWithoutVC(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	calls := filepath.Join(root, "calls")
	t.Setenv("FEISHU_FIXTURE_CALLS", calls)
	cfg := feishumeeting.Config{Profile: "fixture", AppID: "cli_fixture", TenantID: "tenant", BotOpenID: "ou_bot", OwnerOpenID: "ou_human", AllowedSenderOpenIDs: []string{"ou_human"}, PollInterval: 5 * time.Second, IMChat: true}
	content, _ := json.Marshal(map[string]string{"text": "/status"})
	raw, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_id": "event-fixture", "event_type": "im.message.receive_v1", "app_id": cfg.AppID, "tenant_key": cfg.TenantID, "create_time": time.Now().UnixMilli()}, "event": map[string]any{"sender": map[string]any{"sender_id": map[string]string{"open_id": cfg.OwnerOpenID}, "sender_type": "user", "tenant_key": cfg.TenantID}, "message": map[string]any{"message_id": "om_fixture", "chat_id": "oc_fixture", "chat_type": "p2p", "message_type": "text", "content": string(content)}}})
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FEISHU_FIXTURE_CALLS"
case "$3 $4" in
  'event +subscribe')
    printf '%s\n' '` + string(raw) + `'
    read ignored
    ;;
  'im +messages-reply')
    printf '%s\n' '{"ok":true,"identity":"bot","data":{"message_id":"om_reply","chat_id":"oc_fixture"}}'
    ;;
  *) exit 92 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	delivered := make(chan struct{}, 1)
	observe := func(kind, text string) error {
		if kind == "channel.delivery" {
			delivered <- struct{}{}
		}
		return nil
	}
	service := &channel.Service{Channel: state.ReplyTargetFeishu, DisableReactions: true, Store: store, Host: waitingMeetingHost{}, ClientID: cfg.AppID, Profile: "shared-profile", Scope: root, Timeout: time.Second, Observe: observe}
	service.ResolveAgent = func(context.Context, state.ChannelMessage) (*channel.Service, error) { return service, nil }
	done := make(chan error, 1)
	go func() {
		done <- serveFeishuAutomaticEntry(ctx, cfg, filepath.Join(root, "state"), service, func(event.Event) error { return nil }, observe)
	}()
	select {
	case <-delivered:
		cancel()
	case err := <-done:
		t.Fatal("IM runtime ended before its reply", err)
	case <-ctx.Done():
		t.Fatal("IM runtime failed to deliver the standalone status command")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	log, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(log), "event +subscribe") != 1 || strings.Count(string(log), "im +messages-reply") != 1 || !strings.Contains(string(log), "--msg-type post --content") || strings.Contains(string(log), " vc ") {
		t.Fatal("IM-only runtime did not use exactly one subscription and reply", string(log), err)
	}
	messages, err := store.ChannelMessages(t.Context(), 10)
	if err != nil || len(messages) != 1 || messages[0].Channel != state.ReplyTargetFeishu || messages[0].ReplyStatus != feishumeeting.Accepted {
		t.Fatal("IM reply was not durably accepted", messages, err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "feishu-meeting")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("IM-only runtime opened meeting entry state")
	}
}
