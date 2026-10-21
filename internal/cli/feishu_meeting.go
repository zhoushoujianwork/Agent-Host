package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/feishu"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/componentconfig"
	"agent-host/internal/event"
	"agent-host/internal/host"
	"agent-host/internal/routing"
	"agent-host/internal/state"
	"agent-host/internal/taskfeedback"
	"agent-host/internal/terminal"
)

const feishuMeetingUsage = `Feishu private/group and meeting text through an existing lark-cli profile.

  agent-host feishu check --config PATH [execution options]
  agent-host feishu serve --config PATH [execution options]
  agent-host feishu status [--home PATH]

  agent-host feishu-meeting check --config PATH [execution options]
  agent-host feishu-meeting serve --config PATH [execution options]
  agent-host feishu-meeting status [--home PATH]

Configuration is private JSON: profile, app_id, bot_open_id, meeting_id,
tenant_id, allowed_sender_open_ids, poll_interval_seconds (at least 5), auto_join,
im_chat, im_reactions and owner_open_id. im_chat enables owner private text and same-tenant
group text with a verified bot mention, on the same event connection and worker pool.
im_reactions enables four progress reactions on original private/group messages.
It requires the published bot scope im:message.reactions:write_only (or im:message).
Meeting chat has no IM reaction target and keeps reactions disabled.
Without auto_join, join the bot with lark-cli before starting this receiver.
With auto_join, meeting_id is optional; an existing meeting is verified before use.
Allowlisted meeting invitations and direct meeting cards/links/numbers join automatically.
Entry uses official CLI WebSocket events; meeting questions continue to use polling.
Use the long meeting ID and actual bot open ID, not the numeric join_user ID.
Only allowlisted human AI: / AI： text questions become tasks; replies use [AI].
Every startup skips currently available history, including downtime messages.
Durably queued work recovers; unknown execution or delivery is never replayed.
Use AI: /new, AI: /status and AI: /help for standalone Host controls.
The meeting shares one conversation and its own memory audience per Agent.

Execution options are shared with dingtalk: --home, --workspace, --claude,
--model, --agents-config, --native-profile, --no-tools, --allowed-tools,
--knowledge-config, --jev-provider, --jev-endpoint, --jev-model, --timeout,
--max-concurrent-conversations and --format text|json.
check is local only: no connection, native invocation or host state creation.
serve continuously polls until stopped; it owns its state home.
Use a separate state home and native profile for meeting trials.
This channel implements text only; it does not receive or transmit audio.
`

type feishuMeetingConfigFile struct {
	Profile              string   `json:"profile"`
	AppID                string   `json:"app_id"`
	BotOpenID            string   `json:"bot_open_id"`
	MeetingID            string   `json:"meeting_id"`
	TenantID             string   `json:"tenant_id"`
	AllowedSenderOpenIDs []string `json:"allowed_sender_open_ids"`
	PollIntervalSeconds  int64    `json:"poll_interval_seconds"`
	AutoJoin             bool     `json:"auto_join,omitempty"`
	IMChat               bool     `json:"im_chat,omitempty"`
	IMReactions          bool     `json:"im_reactions,omitempty"`
	OwnerOpenID          string   `json:"owner_open_id,omitempty"`
}

func loadFeishuMeetingConfig(path string) (feishumeeting.Config, error) {
	var c feishuMeetingConfigFile
	if path == "" {
		return feishumeeting.Config{}, errors.New("provide --config with the private Feishu meeting configuration")
	}
	var raw json.RawMessage
	if err := readServiceJSON(path, &raw); err != nil {
		return feishumeeting.Config{}, err
	}
	settings, err := componentconfig.Settings(path, raw, "")
	if err != nil {
		return feishumeeting.Config{}, err
	}
	if err := strictUnifiedJSON(settings, &c); err != nil {
		return feishumeeting.Config{}, fmt.Errorf("Feishu meeting configuration: %w", err)
	}
	if c.PollIntervalSeconds < 5 || c.PollIntervalSeconds > int64((1<<63-1)/time.Second) {
		return feishumeeting.Config{}, errors.New("Feishu poll_interval_seconds must be at least 5 and fit a duration")
	}
	cfg := feishumeeting.Config{Profile: c.Profile, AppID: c.AppID, BotOpenID: c.BotOpenID, MeetingID: c.MeetingID, TenantID: c.TenantID,
		AllowedSenderOpenIDs: c.AllowedSenderOpenIDs, PollInterval: time.Duration(c.PollIntervalSeconds) * time.Second, AutoJoin: c.AutoJoin, IMChat: c.IMChat, IMReactions: c.IMReactions, OwnerOpenID: c.OwnerOpenID}
	if err := cfg.Validate(); err != nil {
		return feishumeeting.Config{}, err
	}
	if cfg.IMChat {
		if err := feishuIMConfig(cfg).Validate(); err != nil {
			return feishumeeting.Config{}, err
		}
	}
	return cfg, nil
}

func (c agentCatalog) selectMeetingAgent(account string, m channel.Message) (channel.AgentSelection, error) {
	id := c.DefaultAgent
	for _, b := range c.GroupBindings {
		if b.ConversationID == m.ConversationID {
			id = b.AgentID
			break
		}
	}
	if _, err := c.definition(id); err != nil {
		return channel.AgentSelection{}, err
	}
	return channel.AgentSelection{AgentID: id, AudienceID: channel.Key("group-audience-v1", state.ReplyTargetFeishuMeeting, account, m.TenantID, m.ConversationID)}, nil
}

func runFeishuMeeting(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, feishuMeetingUsage)
		return err
	}
	command := args[0]
	if command != "serve" && command != "check" && command != "status" {
		return errors.New("feishu-meeting requires serve, check or status")
	}
	o, positional, err := parse("feishu-meeting "+command, args[1:], errout)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(positional) != 0 || o.oneShot || o.resume != "" || o.runFilter != "" || o.note != "" || o.reason != "" || o.expiresAt != "" || o.discoveryConfig != "" {
		return errors.New("feishu-meeting commands accept meeting configuration and execution options only")
	}
	if command == "status" {
		store, err := state.Open(o.home, true)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, store.Close()) }()
		messages, err := store.ChannelMessages(ctx, 20)
		if err != nil {
			return err
		}
		filtered := make([]state.ChannelMessage, 0, len(messages))
		for _, m := range messages {
			if m.Channel == state.ReplyTargetFeishuMeeting || m.Channel == state.ReplyTargetFeishu {
				filtered = append(filtered, m)
			}
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(filtered)
	}
	if err := enableChannelHistory(&o); err != nil {
		return err
	}
	cfg, err := loadFeishuMeetingConfig(o.feishuConfig)
	if err != nil {
		return err
	}
	agents, err := loadAgents(o.agentsConfig)
	if err != nil {
		return err
	}
	agents.ownerID = cfg.OwnerOpenID
	o.feedbackOwnerID = cfg.OwnerOpenID
	if cfg.IMChat {
		if err := agents.users.validateOwner(state.ReplyTargetFeishu, cfg.AppID, cfg.TenantID, cfg.OwnerOpenID); err != nil {
			return err
		}
	}
	cfg.AllowPrivateUsers = agents.users != nil
	if _, err := exec.LookPath("lark-cli"); err != nil {
		return errors.New("lark-cli is unavailable")
	}
	if _, err := exec.LookPath(o.binary); err != nil {
		return errors.New("configured Claude executable is unavailable")
	}
	client, err := newJevClient(o.provider, o.jevEndpoint, o.jevModel)
	if err != nil {
		return err
	}
	if command == "check" {
		if err := checkAgents(ctx, agents, o); err != nil {
			return err
		}
		inspect := o.projectID != ""
		if o.knowledgeConfig != "" {
			_, err := os.Stat(filepath.Join(o.home, "state.db"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("cannot inspect existing project state for knowledge selection")
			}
			inspect = inspect || err == nil
		}
		if inspect {
			store, err := state.OpenForProjectInspection(o.home)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := resolveOptions(ctx, store, &o, true); err != nil {
				return err
			}
		} else if err := resolveKnowledge(&o); err != nil {
			return err
		}
		transport, scope := "feishu_meeting_cli_poll", "allowlisted_addressed_meeting_text"
		if cfg.IMChat {
			transport, scope = "feishu_cli_events_and_meeting_poll", "owner_private_and_bot_mentioned_group_text"
			if cfg.AutoJoin || cfg.MeetingID != "" {
				scope += "_and_allowlisted_addressed_meeting_text"
			} else {
				transport = "feishu_cli_events"
			}
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "local_configuration_valid", "transport": transport, "scope": scope, "agents": len(agents.Agents), "model": o.model, "jev_provider": client.Provider(), "jev_model": client.Model(), "platform_verified": false, "native_auth_verified": false, "voice_supported": false, "auto_join_enabled": cfg.AutoJoin, "im_chat_enabled": cfg.IMChat, "im_reactions_enabled": cfg.IMReactions})
	}
	release, err := state.Acquire(o.home)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, release()) }()
	store, err := state.Open(o.home, false)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	if err := agents.validateReferences(ctx, store, o); err != nil {
		return err
	}
	if err := resolveOptions(ctx, store, &o, true); err != nil {
		return err
	}
	o.defaultWorkspace, o.defaultScope = o.workspace, o.scope
	if err := errors.Join(store.Recover(ctx), store.RecoverChannels(ctx)); err != nil {
		return err
	}
	var mu sync.Mutex
	renderer := terminal.Renderer{Out: out, JSON: o.format == "json"}
	sink := func(e event.Event) error {
		mu.Lock()
		defer mu.Unlock()
		return renderer.Render(e)
	}
	observe := func(kind, message string) error {
		return sink(event.Event{Version: event.Version, At: time.Now().UTC(), Type: kind, Phase: "channel", Component: "channel.feishu-meeting", Message: message})
	}
	profile := channel.Key("feishu-meeting-v1", cfg.Profile, cfg.AppID, cfg.TenantID, cfg.BotOpenID, cfg.MeetingID, o.workspace, o.model, o.binary, fmt.Sprint(o.noTools), strings.Join(o.allowedTools, "\x00"), client.Provider(), client.Model())
	if cfg.AutoJoin {
		profile = channel.Key("feishu-meeting-auto-v1", cfg.Profile, cfg.AppID, cfg.TenantID, cfg.BotOpenID, o.workspace, o.model, o.binary, fmt.Sprint(o.noTools), strings.Join(o.allowedTools, "\x00"), client.Provider(), client.Model())
	}
	channelKind := state.ReplyTargetFeishuMeeting
	if cfg.IMChat {
		channelKind = state.ReplyTargetFeishu
		profile = channel.Key("feishu-channel-v1", cfg.Profile, cfg.AppID, cfg.TenantID, cfg.BotOpenID, cfg.OwnerOpenID, fmt.Sprint(cfg.AutoJoin), cfg.MeetingID, o.workspace, o.model, o.binary, fmt.Sprint(o.noTools), strings.Join(o.allowedTools, "\x00"), client.Provider(), client.Model())
	}
	service := &channel.Service{Channel: channelKind, DisableReactions: !cfg.IMReactions, TextReplier: feishumeeting.Replier{Config: cfg},
		Store: store, Host: composeHost(store, o, client, sink), Router: routing.New(client), ClientID: cfg.AppID,
		Scope: o.scope, Profile: profile, Timeout: o.timeout, MaxConcurrentConversations: o.maxConcurrentConversations, Observe: observe}
	service.OwnerID = cfg.OwnerOpenID
	service.StartFeedback = func(ctx context.Context) (func(), error) {
		return startTaskFeedbackWorker(ctx, o, taskfeedback.Account{Channel: "feishu", AccountID: cfg.AppID, TenantID: cfg.TenantID}, service, feishu.Replier{Config: feishuIMConfig(cfg)}, observe)
	}
	if cfg.IMChat {
		service.ApprovalSender = feishu.Replier{Config: feishuIMConfig(cfg)}
	}
	if cfg.IMReactions {
		service.APIReactor = feishu.Reactor{Config: feishuIMConfig(cfg)}
	}
	service.SelectMessageAgent = func(ctx context.Context, m channel.Message) (channel.AgentSelection, error) {
		if m.ReplyTarget.Kind == state.ReplyTargetFeishu {
			return agents.selectFeishuAgent(cfg.AppID, m)
		}
		return agents.selectMeetingAgent(cfg.AppID, m)
	}
	service.ResolveAgent = func(ctx context.Context, m state.ChannelMessage) (*channel.Service, error) {
		return resolveAgentChannel(ctx, service, agents, o, client, sink, m)
	}
	stopMemory, err := startManagedMemoryControl(ctx, store, o, client, func(ctx context.Context, run state.Run) (*host.Host, error) {
		return resolveSessionTakeover(ctx, service, agents, o, client, run)
	})
	if err != nil {
		return fmt.Errorf("start memory workbench control: %w", err)
	}
	defer stopMemory()
	if err := service.Validate(); err != nil {
		return err
	}
	if cfg.AutoJoin || cfg.IMChat {
		return serveFeishuAutomaticEntry(ctx, cfg, o.home, service, sink, observe)
	}
	receiver := feishumeeting.Receiver{Config: cfg, ProgressPath: filepath.Join(o.home, "feishu-meeting", channel.Key(cfg.Profile, cfg.AppID, cfg.TenantID, cfg.BotOpenID, cfg.MeetingID)+".json")}
	return serveChannelRuntime(ctx, "Feishu meeting", func(ctx context.Context, observe func(string, string) error) error {
		return receiver.Run(ctx, service.ReceiveMessage, observe)
	}, service.Work, observe)
}
