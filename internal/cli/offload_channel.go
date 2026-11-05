package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/channel/feishu"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/event"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

// This opt-in path shares transport admission and delivery, without constructing
// the legacy Host, semantic router or automatic memory workers.
func resolveOffloadChannelOptions(o *options) error {
	if o.offloadProfile == nil || o.offloadClient == nil {
		return errors.New("Offload channel execution requires an explicit profile")
	}
	if _, err := loadDreamRuntime(o.dreamConfig); err != nil {
		return err
	}
	if _, ok := o.offloadClient.(*offloadexec.Library); !ok {
		return errors.New("clean Offload execution requires a Go package profile; CLI compatibility selectors are unsupported")
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
	if err := prepareOffloadCleanProfile(o); err != nil {
		return err
	}
	return nil
}

func offloadCleanProfilePath(home, agent, audience string) string {
	return filepath.Join(home, "native-clean", "instances", agent, "audiences", channel.Key(audience))
}

func prepareOffloadCleanProfile(o *options) error {
	if err := os.MkdirAll(o.home, 0700); err != nil {
		return fmt.Errorf("create clean Offload home: %w", err)
	}
	canonicalHome, err := filepath.EvalSymlinks(o.home)
	if err != nil {
		return errors.New("clean Offload home is unavailable")
	}
	o.home = canonicalHome
	paths := []string{filepath.Join(o.home, "native-clean"), filepath.Join(o.home, "native-clean", "instances"), filepath.Join(o.home, "native-clean", "instances", o.agentID), filepath.Join(o.home, "native-clean", "instances", o.agentID, "audiences"), offloadCleanProfilePath(o.home, o.agentID, o.audienceID)}
	for _, path := range paths {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create clean Offload native profile: %w", err)
		}
		if _, err := privateServicePath(path, true); err != nil {
			return fmt.Errorf("clean Offload native profile %s: %w", path, err)
		}
	}
	profile := paths[len(paths)-1]
	settings := filepath.Join(profile, "settings.json")
	file, err := os.OpenFile(settings, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, writeErr := file.Write([]byte("{}\n"))
		err = errors.Join(writeErr, file.Close())
	} else if errors.Is(err, os.ErrExist) {
		err = nil
	} else {
		return fmt.Errorf("create clean Offload native settings: %w", err)
	}
	if err != nil {
		return fmt.Errorf("write clean Offload native settings: %w", err)
	}
	if _, err = privateServicePath(settings, false); err != nil {
		return fmt.Errorf("clean Offload native settings: %w", err)
	}
	body, err := os.ReadFile(settings)
	if err != nil || string(body) != "{}\n" {
		return errors.New("clean Offload native settings must remain empty")
	}
	skills := filepath.Join(profile, "skills")
	if err := os.Mkdir(skills, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create clean Offload Skill directory: %w", err)
	}
	if _, err := privateServicePath(skills, true); err != nil {
		return fmt.Errorf("clean Offload Skill directory: %w", err)
	}
	entries, err := os.ReadDir(skills)
	if err != nil {
		return errors.New("clean Offload Skill directory is unreadable")
	}
	if len(entries) != 0 {
		return errors.New("clean Offload native profile must contain zero Skills")
	}
	o.nativeProfile = profile
	o.runtimeFingerprint = offloadchannel.FingerprintPrefix + channel.Key("offload-clean-native-v1", "environment=v2", "exact-skills=v1", profile, o.executionFingerprint, o.workspace, o.scope, o.agentID, o.hostInstructions)
	return nil
}

// Full Host chat without a catalog uses the same zero-Skill profile as the lite
// path without adopting the lite path's runtime or business configuration.
func selectNoCatalogChatProfile(command string, o *options) error {
	if command != "chat" || o.agentsConfig != "" {
		return nil
	}
	return selectEmptyNativeChatProfile(o)
}

func selectEmptyNativeChatProfile(o *options) error {
	selected := *o
	if err := prepareOffloadCleanProfile(&selected); err != nil {
		return err
	}
	fingerprint, err := nativeSkillSelectionFingerprint("", nil)
	if err != nil {
		return err
	}
	o.home = selected.home
	o.nativeProfile = selected.nativeProfile
	o.nativeSkillsFingerprint = fingerprint
	return nil
}

func composeOffloadChannelHost(store *state.Store, o options, sink event.Sink) channel.Host {
	hotPerson, hotRefresh := o.hotPersonID, o.hotRefresh
	if o.audienceID != state.OwnerAudienceID {
		hotPerson, hotRefresh = "", nil
	}
	return &offloadchannel.Host{Store: store.WithPartition(o.agentID, o.audienceID), Client: o.offloadClient,
		Config: offloadchannel.Config{AgentID: o.agentID, AudienceID: o.audienceID, Scope: o.scope,
			Workspace: o.workspace, NativeProfile: o.nativeProfile, RuntimeFingerprint: o.runtimeFingerprint, Model: o.model,
			Instructions: o.hostInstructions, Write: true, HotPersonID: hotPerson, HotDocuments: hotRefresh}, Sink: sink, Memory: o.offloadMemory}
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
		if err := prepareOffloadCleanProfile(&selected); err != nil {
			return nil, err
		}
		owner := message.SenderID == service.OwnerID
		selected.hostInstructions += "\n\n" + channelAuthorizationInstructions(service.OwnerID, owner)
		child := *service
		child.Store = service.Store.WithPartition(selected.agentID, expectedAudience)
		child.Host = composeOffloadChannelHost(child.Store, selected, sink)
		child.ResolveAgent, child.SelectAgent, child.SelectMessageAgent = nil, nil, nil
		return &child, nil
	}
}

func withOffloadChannelStore(ctx context.Context, o options, out io.Writer, serve func(context.Context, *state.Store, event.Sink, func(string, string) error) error) (runErr error) {
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
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop, done, err := startDreamBackground(serviceCtx, store, o)
	if err != nil {
		return err
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serve(serviceCtx, store, sink, observe) }()
	if done == nil {
		return <-serveDone
	}
	select {
	case runErr = <-serveDone:
		stop()
		backgroundErr := <-done
		if errors.Is(backgroundErr, context.Canceled) {
			backgroundErr = nil
		}
		return errors.Join(runErr, backgroundErr)
	case backgroundErr := <-done:
		cancel()
		return errors.Join(backgroundErr, <-serveDone)
	}
}

func offloadChannelCheck(out io.Writer, kind string, o options) error {
	dreamConfig, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return err
	}
	dreamEnabled, hotEnabled := false, false
	if dreamConfig != nil {
		dreamEnabled, hotEnabled = dreamConfig.Enabled, dreamConfig.HotEnabled
	}
	_, err = fmt.Fprintf(out, "{\"status\":\"local_configuration_valid\",\"engine\":\"offload\",\"channel\":%q,\"memory_enabled\":%t,\"memory_processing_enabled\":%t,\"dream_enabled\":%t,\"dream_hot_enabled\":%t,\"platform_verified\":false,\"native_auth_verified\":false}\n", kind, o.offloadMemory != nil, o.offloadMemory != nil && o.offloadMemory.Decider != nil, dreamEnabled, hotEnabled)
	return err
}

func runOffloadDingTalk(ctx context.Context, command string, cfg dingtalk.Config, o options, out io.Writer) error {
	if err := resolveOffloadChannelOptions(&o); err != nil {
		return err
	}
	dreamConfig, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return err
	}
	if err = validateDreamRuntimeIdentity(dreamConfig, o, cfg.OwnerUserID); err != nil {
		return err
	}
	if command == "check" {
		return offloadChannelCheck(out, "dingtalk", o)
	}
	return withOffloadChannelStore(ctx, o, out, func(serviceCtx context.Context, store *state.Store, sink event.Sink, observe func(string, string) error) error {
		if err := prepareDreamHOT(store, &o); err != nil {
			return err
		}
		if o.hotPersonID != "" && o.hotPersonID != cfg.OwnerUserID {
			return errors.New("dream HOT person must match configured DingTalk owner")
		}
		service := &channel.Service{Store: store, ClientID: cfg.ClientID, OwnerID: cfg.OwnerUserID,
			Replier: dingtalk.Replier{}, Reactor: &dingtalk.Reactor{Config: cfg, RobotCode: cfg.RobotCode}, Observe: observe}
		configureOffloadChannel(service, o, cfg.CorpID, sink)
		if err := service.Validate(); err != nil {
			return err
		}
		return serveChannel(serviceCtx, dingtalk.Receiver{Config: cfg}, service, observe)
	})
}

func runOffloadFeishu(ctx context.Context, command string, cfg feishumeeting.Config, o options, out io.Writer) error {
	if !cfg.IMChat || cfg.AutoJoin || cfg.MeetingID != "" || command == "probe" {
		return errors.New("Offload supports Feishu IM text only; meeting/probe execution is unsupported")
	}
	if err := resolveOffloadChannelOptions(&o); err != nil {
		return err
	}
	dreamConfig, err := loadDreamRuntime(o.dreamConfig)
	if err != nil {
		return err
	}
	if err = validateDreamRuntimeIdentity(dreamConfig, o, cfg.OwnerOpenID); err != nil {
		return err
	}
	if _, err := exec.LookPath("lark-cli"); err != nil {
		return errors.New("lark-cli is unavailable")
	}
	if command == "check" {
		return offloadChannelCheck(out, "feishu", o)
	}
	return withOffloadChannelStore(ctx, o, out, func(serviceCtx context.Context, store *state.Store, sink event.Sink, observe func(string, string) error) error {
		if err := prepareDreamHOT(store, &o); err != nil {
			return err
		}
		if o.hotPersonID != "" && o.hotPersonID != cfg.OwnerOpenID {
			return errors.New("dream HOT person must match configured Feishu owner")
		}
		service := &channel.Service{Channel: state.ReplyTargetFeishu, Store: store, ClientID: cfg.AppID, OwnerID: cfg.OwnerOpenID,
			TextReplier: feishu.Replier{Config: feishuIMConfig(cfg)}, DisableReactions: !cfg.IMReactions, Observe: observe}
		if cfg.IMReactions {
			service.APIReactor = feishu.Reactor{Config: feishuIMConfig(cfg)}
		}
		configureOffloadChannel(service, o, cfg.TenantID, sink)
		if err := service.Validate(); err != nil {
			return err
		}
		return serveFeishuAutomaticEntry(serviceCtx, cfg, o.home, service, sink, observe)
	})
}
