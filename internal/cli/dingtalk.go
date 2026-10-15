package cli

import (
	"bytes"
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

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/componentconfig"
	"agent-host/internal/decision/jev"
	"agent-host/internal/discovery"
	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness/claude"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/objectmemory"
	"agent-host/internal/routing"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

const dingtalkUsage = `DingTalk private and group-mention channel (application bot, Stream mode).

  agent-host dingtalk check --config PATH [options]
  agent-host dingtalk serve --config PATH [options]
  agent-host dingtalk status [--home PATH]

Configuration is JSON with client_id, robot_code, client_secret_file, corp_id, owner_user_id.
Accepts owner direct messages and verified mentions by same-enterprise group members.
Ordinary group messages are ignored. Tasks automatically route within the agent's catalog.
Describe the task directly; uncertain project matches go to the default workspace.
The native assistant handles any missing task details without project-registration prompts.
/new resets the current compatible conversation and keeps its memory.
/status shows a safe conversation summary; /help lists supported controls.
Project registration and agent configuration remain local operations.
The secret is read from a private local file and is never printed.

Common execution options: --home, --workspace or --project, --model, --claude, --no-tools,
--allowed-tools, --agents-config, --native-profile, --jev-provider, --jev-endpoint, --jev-model,
--timeout, --format text|json, --discovery-config.
Agent history search/read is mounted automatically when tools are enabled.
Owner-private tasks also discover the built-in DingTalk workspace MCP.
workspace_config selects its private configuration; default is data-dingtalk-workspace.json beside the bot JSON.
Optional system DWS discovery uses explicit selected conversations in a private file.
check validates local configuration and credentials without connecting or sending.
serve owns its state home; stop it before local memory review/retry commands.
Verified owners retain native execution; other senders use restricted reads and owner approvals.
Terminal output streams; DingTalk replies show task results without internal metadata.
Original task messages show generic processing and completed/failed markers.
These three Host controls run in message order without model calls or reactions.
They do not forward native interactive commands.
Replies that exceed the text limit are explicitly excerpted; full output stays local.
`

type botConfigFile struct {
	ClientID         string `json:"client_id"`
	RobotCode        string `json:"robot_code"`
	ClientSecretFile string `json:"client_secret_file"`
	CorpID           string `json:"corp_id"`
	OwnerUserID      string `json:"owner_user_id"`
	WorkspaceConfig  string `json:"workspace_config,omitempty"`
}

func loadBotConfig(path string) (dingtalk.Config, error) {
	var result dingtalk.Config
	if path == "" {
		return result, errors.New("provide --config with the owner-DM bot configuration")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("DingTalk configuration must be a readable regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return result, errors.New("cannot open DingTalk configuration file")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("DingTalk configuration must be a readable regular file")
	}
	rawConfig, err := io.ReadAll(io.LimitReader(f, 32*1024+1))
	if err != nil || len(rawConfig) > 32*1024 {
		return result, errors.New("DingTalk configuration could not be read within its limit")
	}
	rawConfig, err = componentconfig.Settings(path, rawConfig, "bot")
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	var c botConfigFile
	if err := decoder.Decode(&c); err != nil {
		return result, errors.New("DingTalk configuration must be JSON with the documented fields")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, errors.New("DingTalk configuration has trailing or oversized data")
	}
	if filepath.Base(path) == "channels.dingtalk.json" {
		c.ClientSecretFile = componentconfig.Resolve(path, c.ClientSecretFile)
	}
	if !filepath.IsAbs(c.ClientSecretFile) {
		return result, errors.New("client_secret_file must be an absolute path")
	}
	if strings.TrimSpace(c.RobotCode) == "" || len(c.RobotCode) > 512 {
		return result, errors.New("robot_code is required and must not exceed 512 bytes")
	}
	info, err = os.Stat(c.ClientSecretFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return result, errors.New("DingTalk credential file must be a regular file accessible only to its owner (0600 or 0400)")
	}
	secret, err := os.Open(c.ClientSecretFile)
	if err != nil {
		return result, errors.New("cannot open DingTalk credential file")
	}
	defer secret.Close()
	info, err = secret.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return result, errors.New("DingTalk credential file must be a regular file accessible only to its owner (0600 or 0400)")
	}
	raw, err := io.ReadAll(io.LimitReader(secret, 4097))
	if err != nil || len(raw) > 4096 {
		return result, errors.New("DingTalk credential file could not be read within its limit")
	}
	if c.WorkspaceConfig != "" && !filepath.IsAbs(c.WorkspaceConfig) {
		return result, errors.New("workspace_config must be an absolute path")
	}
	result = dingtalk.Config{ClientID: c.ClientID, RobotCode: c.RobotCode, ClientSecret: strings.TrimSpace(string(raw)), CorpID: c.CorpID, OwnerUserID: c.OwnerUserID, WorkspaceConfig: c.WorkspaceConfig}
	if err := dingtalk.ValidateConfig(result); err != nil {
		return dingtalk.Config{}, err
	}
	return result, nil
}

func composeHost(store *state.Store, o options, client *jev.Client, sink event.Sink) *host.Host {
	sink = runtimeReceiptSink(store.WithPartition(o.agentID, o.audienceID), o, sink)
	if o.scope == "" {
		o.scope = o.workspace
	}
	runner := claude.New(o.binary)
	h := &host.Host{Store: store.WithPartition(o.agentID, o.audienceID), Runner: runner, Decider: memory.New(client), Extractor: experience.NewExtractor(runner, o.model), Judge: experience.NewJudge(client), Config: host.Config{AgentID: o.agentID, AudienceID: o.audienceID, NativeProfile: o.nativeProfile, PermissionMode: o.permissionMode, HostInstructions: o.hostInstructions, Projects: o.projects, AdditionalDirectories: o.additionalDirectories, Scope: o.scope, ProjectID: o.projectID, ProjectName: o.projectName, Workspace: o.workspace, RuntimeFingerprint: o.runtimeFingerprint, Model: o.model, DecisionModel: client.Model(), DecisionProvider: client.Provider(), ToolsDisabled: o.noTools, AllowedTools: o.allowedTools, MCPServers: o.mcpServers, KnowledgeHints: o.knowledgeHints, ValidateKnowledge: knowledgeValidator(o)}, Sink: sink}
	h.Config.ContextFiles, h.Config.UserProfile, h.Config.WorkspaceCache = o.contextFiles, o.userProfile, o.workspaceCache
	h.Objects = objectmemory.New(runner, o.model)
	h.Config.MemorySource, h.Config.MemoryFingerprint = o.memorySource, o.memoryFingerprint
	h.Config.ExpectedSkillDigest, h.Config.SkillsPreselected = o.skillFingerprint, o.skillsPreselected
	h.Config.ProcedureRecallHints = append([]state.ProcedureRecallHint(nil), o.procedureRecallHints...)
	if o.historyEnabled && !o.noTools {
		h.Config.HistoryHome, h.Config.HistoryExecutable = o.home, o.historyExecutable
	}
	if o.contextExecutable != "" && !o.noTools {
		h.Config.ContextHome, h.Config.ContextExecutable, h.Config.ContextConfig = o.home, o.contextExecutable, o.discoveryConfig
	}
	if o.debugNonOwner {
		configureNonOwnerDebugHost(h)
	}
	return h
}

func runDingTalk(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, dingtalkUsage)
		return err
	}
	command := args[0]
	if command != "serve" && command != "check" && command != "status" {
		return errors.New("dingtalk requires serve, check or status")
	}
	o, positional, err := parse("dingtalk "+command, args[1:], errout)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(positional) != 0 || o.oneShot || o.resume != "" || o.runFilter != "" || o.note != "" || o.reason != "" || o.expiresAt != "" {
		return errors.New("dingtalk commands accept channel configuration and execution options only")
	}
	if command != "status" {
		if err := enableChannelHistory(&o); err != nil {
			return err
		}
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
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(messages)
	}
	cfg, err := loadBotConfig(o.dingtalkConfig)
	if err != nil {
		return err
	}
	agents, err := loadAgents(o.agentsConfig)
	if err != nil {
		return err
	}
	agents.ownerID = cfg.OwnerUserID
	if err := agents.users.validateOwner("dingtalk", cfg.ClientID, cfg.CorpID, cfg.OwnerUserID); err != nil {
		return err
	}
	cfg.AllowPrivateUsers = agents.users != nil
	if _, err := exec.LookPath(o.binary); err != nil {
		return errors.New("configured Claude executable is unavailable")
	}
	client, err := newJevClient(o.provider, o.jevEndpoint, o.jevModel)
	if err != nil {
		return err
	}
	if command == "check" {
		if o.discoveryConfig != "" {
			if err := checkDiscoveryConfig(ctx, o.discoveryConfig, o.home); err != nil {
				return err
			}
		}
		if err := checkAgents(ctx, agents, o); err != nil {
			return err
		}
		inspectProject := o.projectID != ""
		if o.knowledgeConfig != "" {
			if _, err := os.Stat(filepath.Join(o.home, "state.db")); err == nil {
				inspectProject = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return errors.New("cannot inspect existing project state for knowledge selection")
			}
		}
		if inspectProject {
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
		return json.NewEncoder(out).Encode(map[string]any{"status": "local_configuration_valid", "transport": "dingtalk_stream", "scope": "owner_direct_and_enterprise_group_mentions", "agents": len(agents.Agents), "model": o.model, "jev_provider": client.Provider(), "jev_model": client.Model(), "platform_verified": false, "native_auth_verified": false})
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
	if err := store.Recover(ctx); err != nil {
		return err
	}
	if err := store.RecoverChannels(ctx); err != nil {
		return err
	}
	var renderMu sync.Mutex
	renderer := terminal.Renderer{Out: out, JSON: o.format == "json"}
	sink := func(e event.Event) error {
		renderMu.Lock()
		defer renderMu.Unlock()
		return renderer.Render(e)
	}
	var channelMu sync.Mutex
	stopMemory, err := startManagedMemoryControl(ctx, store, o, client)
	if err != nil {
		return fmt.Errorf("start memory workbench control: %w", err)
	}
	defer stopMemory()
	var seq int64
	start := time.Now()
	emitChannel := func(kind, message, status string, data map[string]any) error {
		channelMu.Lock()
		defer channelMu.Unlock()
		seq++
		return sink(event.Event{Version: event.Version, Seq: seq, At: time.Now().UTC(), Type: kind, Phase: "channel", Component: "channel.dingtalk", Message: message, Status: status, Data: data, ElapsedMS: time.Since(start).Milliseconds()})
	}
	observe := func(kind, message string) error { return emitChannel(kind, message, "", nil) }
	profile := channel.Key("dingtalk-owner-v1", cfg.ClientID, cfg.CorpID, cfg.OwnerUserID, o.workspace, o.model, o.binary, fmt.Sprint(o.noTools), strings.Join(o.allowedTools, "\x00"), client.Provider(), client.Model())
	service := &channel.Service{Downloader: &dingtalk.MediaDownloader{Config: cfg}, MediaRoot: filepath.Join(o.home, "attachments"), Store: store, Host: composeHost(store, o, client, sink), Router: routing.New(client), Replier: dingtalk.Replier{}, Reactor: &dingtalk.Reactor{Config: cfg, RobotCode: cfg.RobotCode}, ClientID: cfg.ClientID, Scope: o.scope, Profile: profile, Timeout: o.timeout, MaxConcurrentConversations: o.maxConcurrentConversations, Observe: observe}
	o.runtimeWorkspaces = make(map[string]string)
	configureChannelRuntimeProjects(service, store, o, "dingtalk")
	service.OwnerID = cfg.OwnerUserID
	service.SelectAgent = func(ctx context.Context, m dingtalk.Message) (channel.AgentSelection, error) {
		return agents.selectAgent(cfg.ClientID, m)
	}
	service.ResolveAgent = func(ctx context.Context, m state.ChannelMessage) (*channel.Service, error) {
		return resolveAgentChannel(ctx, service, agents, o, client, sink, m)
	}
	service.ObserveReaction = func(kind string, reaction state.ChannelReaction, duration time.Duration) error {
		return emitChannel(kind, fmt.Sprintf("message=%s stage=%s label=%s status=%s", reaction.ChannelMessageID, reaction.Stage, reaction.Label, reaction.Status), reaction.Status, map[string]any{
			"reaction_id": reaction.ID, "channel_message_id": reaction.ChannelMessageID,
			"stage": reaction.Stage, "label": reaction.Label, "error": reaction.Error,
			"duration_ms":   duration.Milliseconds(),
			"source_run_id": reaction.SourceRunID, "source_event_seq": reaction.SourceEventSeq,
		})
	}
	service.ResolveHost = func(ctx context.Context, projectID string) (channel.Host, assembly.Runtime, error) {
		selected := o
		selected.projectRoutingComplete = true
		selected.projectID, selected.projectName, selected.scope, selected.primaryProject = "", "", "", ""
		selected.projectIDs = nil
		selected.workspace = o.defaultWorkspace
		if projectID != "" {
			selected.projectID = projectID
			selected.projectIDs = []string{projectID}
		}
		if err := resolveOptions(ctx, store, &selected, true); err != nil {
			return nil, assembly.Runtime{}, err
		}
		runtime := runtimeOptions(selected)
		return composeHost(store, selected, client, sink), runtime, nil
	}
	service.ResolveProjects = func(ctx context.Context, ids []string, primaryID string) (channel.Host, assembly.Runtime, error) {
		selected := o
		selected.projectRoutingComplete = true
		selected.projectIDs = append([]string(nil), ids...)
		selected.primaryProject = primaryID
		selected.projectID, selected.projectName, selected.scope = "", "", ""
		selected.workspace = o.defaultWorkspace
		if len(ids) == 1 {
			selected.projectID = ids[0]
		}
		if err := resolveOptions(ctx, store, &selected, true); err != nil {
			return nil, assembly.Runtime{}, err
		}
		return composeHost(store, selected, client, sink), runtimeOptions(selected), nil
	}
	if err := service.Validate(); err != nil {
		return err
	}
	var collector *discovery.Collector
	if o.discoveryConfig != "" {
		config, err := discovery.LoadConfig(o.discoveryConfig)
		if err != nil {
			return err
		}
		collector, err = composeDiscovery(ctx, store, config, o.binary)
		if err != nil {
			return err
		}
	}
	return serveChannelWithDiscovery(ctx, dingtalk.Receiver{Config: cfg}, service, observe, collector, sink)
}

// Discovery shares the Host writer but has its own lifecycle and failure state.
// It never turns observed chat messages into channel requests or replies.
func serveChannelWithDiscovery(ctx context.Context, receiver channelReceiver, service *channel.Service, observe func(string, string) error, collector *discovery.Collector, sink event.Sink) error {
	if collector == nil {
		return serveChannel(ctx, receiver, service, observe)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := collector.Loop(ctx, func(report discovery.Report, collectErr error) {
			data := discoveryCycleStatus(report, collectErr)
			_ = sink(event.Event{Version: event.Version, At: time.Now().UTC(), Type: "discovery.cycle", Phase: "discovery", Component: "system.discovery", Status: data["status"].(string), Message: "system conversation discovery cycle", Data: data})
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = sink(event.Event{Version: event.Version, At: time.Now().UTC(), Type: "discovery.stopped", Phase: "discovery", Component: "system.discovery", Status: "failed", Message: "system discovery stopped; channel processing remains available"})
		}
		done <- err
	}()
	err := serveChannel(ctx, receiver, service, observe)
	cancel()
	discoveryErr := <-done
	if errors.Is(discoveryErr, context.Canceled) {
		discoveryErr = nil
	}
	return errors.Join(err, discoveryErr)
}

type channelReceiver interface {
	Run(context.Context, func(context.Context, dingtalk.Message) error, func(string, string) error) error
}

// Do not start queued native work until the configured receiver is connected.
// Both loops are joined before releasing the home lock and closing the store.
func serveChannel(ctx context.Context, receiver channelReceiver, service *channel.Service, observe func(string, string) error) error {
	return serveChannelRuntime(ctx, "DingTalk", func(ctx context.Context, observe func(string, string) error) error {
		return receiver.Run(ctx, service.Receive, observe)
	}, service.Work, observe)
}

// The receiver acknowledges durable input independently of native execution.
// Readiness gates recovery, and both loops finish before closing Host state.
func serveChannelRuntime(ctx context.Context, label string, receive func(context.Context, func(string, string) error) error, work func(context.Context) error, observe func(string, string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	var once sync.Once
	received := make(chan error, 1)
	go func() {
		received <- receive(ctx, func(kind, message string) error {
			if err := observe("channel."+kind, message); err != nil {
				return err
			}
			if kind == "connected" {
				once.Do(func() { close(ready) })
			}
			return nil
		})
	}()
	select {
	case err := <-received:
		if err == nil {
			err = fmt.Errorf("%s receiver stopped without a terminal outcome", label)
		}
		return errors.Join(ctx.Err(), err)
	case <-ctx.Done():
		cancel()
		return errors.Join(ctx.Err(), <-received)
	case <-ready:
	}
	worked := make(chan error, 1)
	go func() { worked <- work(ctx) }()
	select {
	case err := <-received:
		cancel()
		if err == nil {
			err = fmt.Errorf("%s receiver stopped without a terminal outcome", label)
		}
		return errors.Join(err, <-worked)
	case err := <-worked:
		cancel()
		if err == nil {
			err = fmt.Errorf("%s worker stopped without a terminal outcome", label)
		}
		return errors.Join(err, <-received)
	case <-ctx.Done():
		cancel()
		return errors.Join(ctx.Err(), <-received, <-worked)
	}
}
