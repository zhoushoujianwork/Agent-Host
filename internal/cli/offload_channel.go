package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/channel/feishu"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/event"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

// This opt-in path shares transport admission and delivery, without constructing
// the legacy Host, semantic router or automatic memory workers.
func resolveOffloadChannelOptions(o *options) error {
	if o.offloadProfile == nil || o.offloadClient == nil {
		return errors.New("Offload channel execution requires an explicit profile")
	}
	if o.audienceID != state.OwnerAudienceID {
		return errors.New("Offload channel audiences are resolved from authenticated channel messages")
	}
	if o.noTools || o.debugNonOwner || len(o.allowedTools) != 0 || o.permissionMode != "" && o.permissionMode != "bypassPermissions" {
		return errors.New("Offload channel execution does not support Host tool restrictions")
	}
	if o.agentsConfig != "" || o.projectID != "" || len(o.projectIDs) != 0 || o.primaryProject != "" || len(o.additionalDirectories) != 0 {
		return errors.New("Offload channels currently require one workspace without an Agent catalog or project routing")
	}
	if o.emailConfig != "" || o.knowledgeConfig != "" || len(o.knowledgeConfigs) != 0 || o.dingtalkWorkspaceConfig != "" || o.discoveryConfig != "" || o.skillsRoot != "" || o.workspaceCache != "" || len(o.mcpServers) != 0 || len(o.pluginNames) != 0 {
		return errors.New("Offload channel execution does not support Host memory, MCP, discovery or managed plugin injection yet")
	}
	workspace, err := state.CanonicalWorkspace(o.workspace)
	if err != nil {
		return err
	}
	o.workspace, o.defaultWorkspace = workspace, workspace
	o.scope = o.defaultScope
	if o.scope == "" {
		o.scope = workspace
	}
	o.defaultScope = o.scope
	o.model = o.offloadProfile.Model
	o.runtimeFingerprint = offloadchannel.FingerprintPrefix + channel.Key(o.executionFingerprint, workspace, o.scope, o.agentID, o.hostInstructions)
	return nil
}

func composeOffloadChannelHost(store *state.Store, o options, sink event.Sink) channel.Host {
	return &offloadchannel.Host{Store: store.WithPartition(o.agentID, o.audienceID), Client: o.offloadClient,
		Config: offloadchannel.Config{AgentID: o.agentID, AudienceID: o.audienceID, Scope: o.scope,
			Workspace: o.workspace, RuntimeFingerprint: o.runtimeFingerprint, Model: o.model,
			Instructions: o.hostInstructions, Write: true}, Sink: sink, Memory: o.offloadMemory}
}

func configureOffloadChannel(service *channel.Service, o options, tenant string, sink event.Sink) {
	catalog := agentCatalog{DefaultAgent: o.agentID, Agents: []agentDefinition{{ID: o.agentID, Name: o.agentID}}}
	service.Host = composeOffloadChannelHost(service.Store, o, sink)
	service.Scope, service.Timeout = o.scope, o.timeout
	service.MaxConcurrentConversations = o.maxConcurrentConversations
	service.Profile = channel.Key("offload-channel-v1", service.Channel, service.ClientID, tenant, service.OwnerID, o.runtimeFingerprint)
	service.SelectAgent = func(_ context.Context, message dingtalk.Message) (channel.AgentSelection, error) {
		return catalog.selectAgent(service.ClientID, message)
	}
	service.SelectMessageAgent = func(_ context.Context, message channel.Message) (channel.AgentSelection, error) {
		return catalog.selectFeishuAgent(service.ClientID, message)
	}
	service.ResolveAgent = func(_ context.Context, message state.ChannelMessage) (*channel.Service, error) {
		kind := service.Channel
		if kind == "" {
			kind = "dingtalk"
		}
		expectedAudience := state.OwnerAudienceID
		if message.ConversationType == "2" {
			expectedAudience = channel.Key("group-audience-v1", kind, service.ClientID, message.TenantID, message.ConversationID)
		} else if message.SenderID != service.OwnerID {
			return nil, errors.New("Offload private conversation is not owned by this channel owner")
		}
		if message.Channel != kind || message.AccountID != service.ClientID || tenant != "" && message.TenantID != tenant || message.AgentID != o.agentID || message.AudienceID != expectedAudience {
			return nil, errors.New("message has an incompatible Offload channel audience binding")
		}
		selected := o
		selected.audienceID = expectedAudience
		owner := message.SenderID == service.OwnerID
		selected.hostInstructions += "\n\n" + channelAuthorizationInstructions(service.OwnerID, owner)
		child := *service
		child.Store = service.Store.WithPartition(selected.agentID, expectedAudience)
		child.Host = composeOffloadChannelHost(child.Store, selected, sink)
		child.ResolveAgent, child.SelectAgent, child.SelectMessageAgent = nil, nil, nil
		return &child, nil
	}
}

func withOffloadChannelStore(ctx context.Context, o options, out io.Writer, serve func(*state.Store, event.Sink, func(string, string) error) error) (runErr error) {
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
		return sink(event.Event{Version: event.Version, At: time.Now().UTC(), Type: kind, Phase: "channel", Component: "channel.offload", Message: message})
	}
	return serve(store, sink, observe)
}

func offloadChannelCheck(out io.Writer, kind string, o options) error {
	_, err := fmt.Fprintf(out, "{\"status\":\"local_configuration_valid\",\"engine\":\"offload\",\"channel\":%q,\"memory_enabled\":%t,\"memory_processing_enabled\":%t,\"platform_verified\":false,\"native_auth_verified\":false}\n", kind, o.offloadMemory != nil, o.offloadMemory != nil && o.offloadMemory.Decider != nil)
	return err
}

func runOffloadDingTalk(ctx context.Context, command string, cfg dingtalk.Config, o options, out io.Writer) error {
	if err := resolveOffloadChannelOptions(&o); err != nil {
		return err
	}
	if command == "check" {
		return offloadChannelCheck(out, "dingtalk", o)
	}
	return withOffloadChannelStore(ctx, o, out, func(store *state.Store, sink event.Sink, observe func(string, string) error) error {
		service := &channel.Service{Store: store, ClientID: cfg.ClientID, OwnerID: cfg.OwnerUserID,
			Replier: dingtalk.Replier{}, Reactor: &dingtalk.Reactor{Config: cfg, RobotCode: cfg.RobotCode}, Observe: observe}
		configureOffloadChannel(service, o, cfg.CorpID, sink)
		if err := service.Validate(); err != nil {
			return err
		}
		return serveChannel(ctx, dingtalk.Receiver{Config: cfg}, service, observe)
	})
}

func runOffloadFeishu(ctx context.Context, command string, cfg feishumeeting.Config, o options, out io.Writer) error {
	if !cfg.IMChat || cfg.AutoJoin || cfg.MeetingID != "" || command == "probe" {
		return errors.New("Offload supports Feishu IM text only; meeting/probe execution is unsupported")
	}
	if err := resolveOffloadChannelOptions(&o); err != nil {
		return err
	}
	if _, err := exec.LookPath("lark-cli"); err != nil {
		return errors.New("lark-cli is unavailable")
	}
	if command == "check" {
		return offloadChannelCheck(out, "feishu", o)
	}
	return withOffloadChannelStore(ctx, o, out, func(store *state.Store, sink event.Sink, observe func(string, string) error) error {
		service := &channel.Service{Channel: state.ReplyTargetFeishu, Store: store, ClientID: cfg.AppID, OwnerID: cfg.OwnerOpenID,
			TextReplier: feishu.Replier{Config: feishuIMConfig(cfg)}, DisableReactions: !cfg.IMReactions, Observe: observe}
		if cfg.IMReactions {
			service.APIReactor = feishu.Reactor{Config: feishuIMConfig(cfg)}
		}
		configureOffloadChannel(service, o, cfg.TenantID, sink)
		if err := service.Validate(); err != nil {
			return err
		}
		return serveFeishuAutomaticEntry(ctx, cfg, o.home, service, sink, observe)
	})
}
