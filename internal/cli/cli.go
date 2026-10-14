// Package cli composes the local host and exposes terminal commands.
package cli

import (
	"bufio"
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
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/buildinfo"
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/knowledge"
	"agent-host/internal/routing"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

const usage = `Agent-Host: native Claude chat with observable, source-linked memory.

Usage:
  agent-host chat [-m MESSAGE] [--resume SESSION] [options]
  agent-host memory search [options] QUERY
  agent-host memory show [options] MEMORY_ID
  agent-host memory candidates [--run RUN_ID] [--status pending|accepted|rejected|all] [options]
  agent-host memory candidate [options] CANDIDATE_ID
  agent-host memory accept [--note TEXT] [--expires-at RFC3339] [options] CANDIDATE_ID
  agent-host memory reject --reason TEXT [options] CANDIDATE_ID
  agent-host memory invalidate --reason TEXT [options] MEMORY_ID
  agent-host memory restore --reason TEXT [options] MEMORY_ID
  agent-host memory migrate --home PATH --memory-root PATH --memory-cache PATH --memory-source ID
  agent-host memory verify --home PATH
  agent-host memory rollback --home PATH
  agent-host run show [options] RUN_ID
  agent-host run events [options] RUN_ID
  agent-host run retry-memory [options] RUN_ID
  agent-host run retry-experience [options] RUN_ID
  agent-host feedback report --output PATH [--home PATH] [--run RUN_ID]
                             [--agent ID] [--audience ID]
  agent-host eval [--suite PATH] [--mode regression|live] [--category CATEGORY] [--case ID]
                  [--format text|json] [--output PATH] [--timeout DURATION]
                  [--jev-provider NAME] [--jev-endpoint URL] [--jev-model MODEL]
  agent-host eval --help
  agent-host dingtalk serve --config PATH [options]
  agent-host dingtalk check --config PATH [options]
  agent-host dingtalk status [options]
  agent-host dingtalk-workspace --help
  agent-host feishu serve --config PATH [options]
  agent-host feishu check --config PATH [options]
  agent-host feishu status [--home PATH]
  agent-host feishu-meeting serve --config PATH [options]
  agent-host feishu-meeting check --config PATH [options]
  agent-host feishu-meeting status [--home PATH]
  agent-host project add --id ID --workspace PATH [--name NAME] [--home PATH]
  agent-host project update --id ID --workspace PATH [--name NAME] [--home PATH]
  agent-host project list [--home PATH]
  agent-host project show --id ID [--home PATH]
  agent-host service check --root ABSOLUTE_PATH
  agent-host service run --root ABSOLUTE_PATH
  agent-host knowledge check --config ABSOLUTE_PATH
  agent-host email --help
  agent-host discovery --help
  agent-host assistance --help

Options (place before positional IDs/queries):
  --home PATH           Host state directory (default .agent-host/cli-state)
  --workspace PATH      Native cwd (default current directory); matches registered projects
  --project ID          Registered project; repeat for joint chat (exclusive with --workspace)
  --primary-project ID  Primary cwd within the selected set; otherwise joint chat uses ~/workspace
  --format text|json    Live terminal output or NDJSON events (default text)
  --model MODEL         Claude model (default AGENT_HOST_MODEL or sonnet)
  --claude PATH         Native executable (default claude)
  --no-tools            Explicit tool-free Claude execution for controlled tests
  --allowed-tools LIST  Native permission allowlist; repeat or comma-separate
  --email-config PATH  Private managed mailbox configuration
  --dingtalk-workspace-config PATH  Private built-in DingTalk workspace configuration
  --knowledge-config PATH  Private Dokki/Confluence source configuration
  --agents-config PATH  Private agent roles, context selections and group bindings
  --agent ID            Logical instance for chat and local state (default default)
  --audience ID         Conversation partition for local state (default owner)
  --native-profile PATH Native Claude configuration directory
  --jev-provider NAME   typesafe (official, default) or custom; never automatic fallback
  --jev-endpoint URL    Third-party System One gateway; required for custom
  --jev-model MODEL     Default jev-1.13.0
  --timeout DURATION    Per-turn deadline including memory (default 1h)

Credentials: TYPESAFE_API_KEY for typesafe; JEV_API_KEY for custom.
Claude uses its native authentication/configuration. Configured channel instances
use permission bypass; ordinary local chat keeps native policy. Inspect commands
do not need model credentials.
Interactive chat reads one line per turn; EOF exits. Ctrl-C cancels and exits.
Memory holds attributed user statements and explicitly reviewed tool-based experiences.
Pending, conflicted, expired, and invalidated experiences are excluded from recall.
`

type options struct {
	retrievalQuery, skillFingerprint                                                   string
	skillsPreselected                                                                  bool
	contextFiles                                                                       []assembly.ContextFile
	userProfile, workspaceCache, memoryFingerprint, memorySource                       string
	maxConcurrentConversations                                                         int
	agentsConfig, agentID, audienceID, nativeProfile, permissionMode, hostInstructions string
	knowledgeNames                                                                     []string
	knowledgeConfig                                                                    string
	knowledgeConfigs                                                                   []string
	knowledgeChannel                                                                   string
	emailConfig, emailFingerprint                                                      string
	disableDingTalkWorkspace                                                           bool
	dingtalkWorkspaceConfig, dingtalkWorkspaceFingerprint                              string
	discoveryConfig                                                                    string
	contextPolicy, contextExecutable                                                   string
	suggestionRevision                                                                 int
	reviewAt                                                                           string
	historyEnabled                                                                     bool
	historyExecutable                                                                  string
	knowledgePluginDisabled                                                            bool
	pluginNames                                                                        []string
	knowledgeSources                                                                   []knowledge.ResolvedSource
	knowledgeHints                                                                     []knowledge.Hint
	mcpServers                                                                         []harness.MCPServer
	projectID, projectName, scope, runtimeFingerprint                                  string
	projectIDs                                                                         []string
	primaryProject, defaultWorkspace, defaultScope                                     string
	projects                                                                           []state.ProjectRef
	additionalDirectories                                                              []string
	workspaceExplicit                                                                  bool
	home, workspace, format, model, binary, provider, jevModel, resume, message        string
	jevEndpoint                                                                        string
	dingtalkConfig                                                                     string
	feishuConfig                                                                       string
	allowedTools                                                                       []string
	runFilter, status, note, reason, expiresAt                                         string
	noTools                                                                            bool
	oneShot                                                                            bool
	timeout                                                                            time.Duration
}

func parse(name string, args []string, errout io.Writer) (options, []string, error) {
	var o options
	cwd, err := os.Getwd()
	if err != nil {
		return o, nil, err
	}
	model := os.Getenv("AGENT_HOST_MODEL")
	if model == "" {
		model = "sonnet"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errout)
	fs.StringVar(&o.home, "home", filepath.Join(cwd, ".agent-host", "cli-state"), "Host state directory")
	fs.StringVar(&o.workspace, "workspace", cwd, "Native working directory")
	fs.StringVar(&o.defaultScope, "memory-scope", "", "Explicit durable default memory scope")
	fs.StringVar(&o.workspaceCache, "workspace-cache", "", "Private root for rebuildable task context views")
	fs.BoolVar(&o.knowledgePluginDisabled, "disable-knowledge-plugin", false, "Skip disabled managed knowledge plugin")
	fs.BoolVar(&o.disableDingTalkWorkspace, "disable-dingtalk-workspace", false, "Disable DingTalk workspace discovery and mounting")
	fs.Func("project", "Registered project ID; repeat for joint chat", func(value string) error {
		o.projectIDs = append(o.projectIDs, value)
		o.projectID = value
		return nil
	})
	fs.StringVar(&o.primaryProject, "primary-project", "", "Primary project in the selected set")
	fs.StringVar(&o.format, "format", "text", "text or json (NDJSON)")
	fs.StringVar(&o.model, "model", model, "Claude model")
	fs.StringVar(&o.binary, "claude", "claude", "Claude executable")
	fs.StringVar(&o.emailConfig, "email-config", "", "Private managed email configuration")
	fs.StringVar(&o.dingtalkWorkspaceConfig, "dingtalk-workspace-config", "", "Private built-in DingTalk workspace configuration")
	fs.Func("knowledge-config", "Private knowledge source configuration; repeat for multiple plugins", func(path string) error {
		o.knowledgeConfig = path
		o.knowledgeConfigs = append(o.knowledgeConfigs, path)
		return nil
	})
	fs.StringVar(&o.knowledgeChannel, "knowledge-channel", "", "Managed channel grant for datasource selection")
	fs.StringVar(&o.discoveryConfig, "discovery-config", "", "Private DWS system topic discovery configuration")
	fs.StringVar(&o.agentsConfig, "agents-config", "", "Private agent instance and group binding configuration")
	fs.StringVar(&o.agentID, "agent", "default", "Logical agent instance for local state selection")
	fs.StringVar(&o.audienceID, "audience", "owner", "Conversation audience for local state selection")
	fs.StringVar(&o.nativeProfile, "native-profile", os.Getenv("CLAUDE_CONFIG_DIR"), "Native Claude configuration directory")
	fs.StringVar(&o.provider, "jev-provider", "typesafe", "typesafe (official) or custom")
	fs.StringVar(&o.jevEndpoint, "jev-endpoint", "", "Third-party Jev gateway URL for custom")
	fs.StringVar(&o.jevModel, "jev-model", "", "Jev model")
	fs.StringVar(&o.resume, "resume", "", "Host session ID")
	fs.StringVar(&o.message, "m", "", "One-shot input")
	fs.StringVar(&o.message, "message", "", "One-shot input")
	fs.BoolVar(&o.noTools, "no-tools", false, "Disable native tools")
	fs.Func("allowed-tools", "Native tool permission allowlist", func(value string) error {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				return errors.New("allowed-tools entries cannot be empty")
			}
			o.allowedTools = append(o.allowedTools, item)
		}
		return nil
	})
	fs.StringVar(&o.runFilter, "run", "", "Filter candidates by source run")
	fs.StringVar(&o.status, "status", "pending", "Candidate status or all")
	fs.StringVar(&o.note, "note", "", "Acceptance note")
	fs.StringVar(&o.reason, "reason", "", "Rejection, invalidation or restoration reason")
	fs.StringVar(&o.expiresAt, "expires-at", "", "Experience expiration (RFC3339)")
	fs.IntVar(&o.maxConcurrentConversations, "max-concurrent-conversations", 4, "Maximum simultaneous channel conversations (1-32)")
	fs.DurationVar(&o.timeout, "timeout", time.Hour, "Per-turn timeout")
	if strings.HasPrefix(name, "assistance ") {
		fs.StringVar(&o.discoveryConfig, "config", "", "Private selected-source discovery configuration")
		fs.IntVar(&o.suggestionRevision, "revision", 0, "Expected suggestion revision from the private list")
		fs.StringVar(&o.reviewAt, "until", "", "Postponement review time (RFC3339)")
	}
	if strings.HasPrefix(name, "dingtalk ") {
		fs.StringVar(&o.dingtalkConfig, "config", "", "DingTalk owner-DM JSON configuration")
	}
	if strings.HasPrefix(name, "feishu-meeting ") {
		fs.StringVar(&o.feishuConfig, "config", "", "Private Feishu meeting JSON configuration")
	}
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	projectExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "project" {
			projectExplicit = true
		}
		if f.Name == "workspace" {
			o.workspaceExplicit = true
		}
		if f.Name == "m" || f.Name == "message" {
			o.oneShot = true
		}
	})
	if projectExplicit && o.workspaceExplicit {
		return o, nil, errors.New("--project and --workspace are mutually exclusive")
	}
	if projectExplicit && o.projectID == "" {
		return o, nil, errors.New("--project requires a registered project ID")
	}
	for _, id := range o.projectIDs {
		if strings.TrimSpace(id) == "" {
			return o, nil, errors.New("--project requires a registered project ID")
		}
	}
	if len(o.projectIDs) > 1 && name != "chat" {
		return o, nil, errors.New("multiple --project options are supported by chat only")
	}
	if o.primaryProject != "" {
		if name != "chat" || !containsProject(o.projectIDs, o.primaryProject) {
			return o, nil, errors.New("--primary-project must belong to the chat project set")
		}
	}
	if o.format != "text" && o.format != "json" {
		return o, nil, errors.New("format must be text or json")
	}
	if err := validateJevProvider(o.provider, o.jevEndpoint); err != nil {
		return o, nil, err
	}
	if o.noTools && len(o.allowedTools) > 0 {
		return o, nil, errors.New("no-tools cannot be combined with allowed-tools")
	}
	if o.maxConcurrentConversations < 1 || o.maxConcurrentConversations > 32 {
		return o, nil, errors.New("max-concurrent-conversations must be between 1 and 32")
	}
	if o.timeout <= 0 {
		return o, nil, errors.New("timeout must be positive")
	}
	if !agentIdentifier.MatchString(o.agentID) || strings.TrimSpace(o.audienceID) == "" || len(o.audienceID) > 512 || strings.ContainsAny(o.audienceID, "\x00\r\n") {
		return o, nil, errors.New("agent and audience require valid nonempty identities")
	}
	if o.nativeProfile != "" {
		if o.nativeProfile, err = filepath.Abs(o.nativeProfile); err != nil {
			return o, nil, err
		}
	}
	if o.agentsConfig != "" {
		if o.agentsConfig, err = filepath.Abs(o.agentsConfig); err != nil {
			return o, nil, err
		}
	}
	if o.home, err = filepath.Abs(o.home); err != nil {
		return o, nil, err
	}
	if o.emailConfig != "" {
		if o.emailConfig, err = filepath.Abs(o.emailConfig); err != nil {
			return o, nil, err
		}
	}
	if o.knowledgeConfig != "" {
		if o.knowledgeConfig, err = filepath.Abs(o.knowledgeConfig); err != nil {
			return o, nil, err
		}
	}
	for i, path := range o.knowledgeConfigs {
		o.knowledgeConfigs[i], err = filepath.Abs(path)
		if err != nil {
			return o, nil, err
		}
	}
	if o.discoveryConfig != "" {
		if o.discoveryConfig, err = filepath.Abs(o.discoveryConfig); err != nil {
			return o, nil, err
		}
	}
	// Relative executable paths belong to the CLI invocation directory, including
	// extraction retries that deliberately execute in a separate temporary cwd.
	if strings.ContainsRune(o.binary, filepath.Separator) {
		if o.binary, err = filepath.Abs(o.binary); err != nil {
			return o, nil, err
		}
	}
	if o.workspace, err = filepath.Abs(o.workspace); err != nil {
		return o, nil, err
	}
	o.defaultWorkspace = o.workspace
	if o.projectID != "" {
		return o, fs.Args(), nil
	}
	if o.workspace, err = filepath.EvalSymlinks(o.workspace); err != nil {
		return o, nil, fmt.Errorf("workspace: %w", err)
	}
	info, err := os.Stat(o.workspace)
	if err != nil {
		return o, nil, err
	}
	if !info.IsDir() {
		return o, nil, errors.New("workspace must be a directory")
	}
	o.defaultWorkspace = o.workspace
	return o, fs.Args(), nil
}

func Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) (runErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, usage)
		return err
	}
	if args[0] == "--version" {
		info := buildinfo.Current()
		revision := info.Revision
		dirty := fmt.Sprint(info.Modified)
		if revision == "" {
			revision = "unknown"
			dirty = "unknown"
		}
		_, err := fmt.Fprintf(out, "agent-host %s (commit %s, dirty=%s, %s/%s, %s)\n", info.Version, revision, dirty, info.GOOS, info.GOARCH, info.GoVersion)
		return err
	}
	if args[0] == "feedback" {
		return runFeedback(ctx, args[1:], out, errout)
	}
	if args[0] == "project" {
		return runProject(ctx, args[1:], out, errout)
	}
	if args[0] == "eval" {
		return runEval(ctx, args[1:], out, errout)
	}
	if args[0] == "dingtalk" {
		return runDingTalk(ctx, args[1:], out, errout)
	}
	if args[0] == "dingtalk-workspace" {
		return runDingTalkWorkspace(ctx, args[1:], in, out, errout)
	}
	if args[0] == "feishu-meeting" || args[0] == "feishu" {
		return runFeishuMeeting(ctx, args[1:], out, errout)
	}
	if args[0] == "service" {
		return runService(ctx, args[1:], out, errout)
	}
	if args[0] == "email" {
		return runEmail(ctx, args[1:], in, out, errout)
	}
	if args[0] == "knowledge" {
		return runKnowledge(ctx, args[1:], in, out, errout)
	}
	if args[0] == "history-mcp" {
		return runHistoryMCP(ctx, args[1:], in, out, errout)
	}
	if args[0] == "discovery" {
		return runDiscovery(ctx, args[1:], out, errout)
	}
	if args[0] == "context-mcp" {
		return runContextMCP(ctx, args[1:], in, out, errout)
	}
	if args[0] == "assistance" {
		return runAssistance(ctx, args[1:], out, errout)
	}
	if args[0] == "memory" && len(args) > 1 && (args[1] == "migrate" || args[1] == "verify" || args[1] == "rollback") {
		return runMemoryStorage(ctx, args[1:], out, errout)
	}
	command := args[0]
	args = args[1:]
	if command == "memory" || command == "run" {
		if len(args) == 0 {
			return errors.New("missing subcommand")
		}
		command += " " + args[0]
		args = args[1:]
	}
	switch command {
	case "chat", "memory search", "memory show", "memory candidates", "memory candidate", "memory accept", "memory reject", "memory invalidate", "memory restore", "run show", "run events", "run retry-memory", "run retry-experience":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	o, pos, err := parse(command, args, errout)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if command == "chat" || command == "memory candidates" {
		if len(pos) != 0 {
			return errors.New("this command accepts options only, not positional arguments")
		}
	} else if len(pos) != 1 {
		return errors.New("provide exactly one query or ID after options")
	}
	switch command {
	case "memory search", "memory show", "memory candidate", "memory candidates", "run show", "run events":
		id := ""
		if len(pos) != 0 {
			id = pos[0]
		}
		return inspect(ctx, command, o, id, out)
	}
	if (command == "memory reject" || command == "memory invalidate" || command == "memory restore") && strings.TrimSpace(o.reason) == "" {
		return errors.New("provide --reason for rejection, invalidation or restoration")
	}
	var expires *time.Time
	if o.expiresAt != "" {
		t, err := time.Parse(time.RFC3339, o.expiresAt)
		if err != nil || !t.After(time.Now()) {
			return errors.New("expires-at must be a future RFC3339 timestamp")
		}
		expires = &t
	}
	var client *jev.Client
	if command != "memory reject" && command != "memory invalidate" && command != "memory restore" {
		client, err = newJevClient(o.provider, o.jevEndpoint, o.jevModel)
		if err != nil {
			return err
		}
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
	defer func(owner *state.Store) { runErr = errors.Join(runErr, owner.Close()) }(store)
	store = store.WithPartition(o.agentID, o.audienceID)
	if command == "chat" && o.agentsConfig != "" {
		catalog, err := loadAgents(o.agentsConfig)
		if err != nil {
			return err
		}
		if err := catalog.validateReferences(ctx, store, o); err != nil {
			return err
		}
		a, err := catalog.definition(o.agentID)
		if err != nil {
			return err
		}
		o, err = a.options(o, o.audienceID, o.audienceID != "owner", true)
		if err != nil {
			return err
		}
	}
	if command == "chat" && o.oneShot {
		o.retrievalQuery = o.message
	}
	if err := resolveOptions(ctx, store, &o, command == "chat"); err != nil {
		return err
	}
	if err := store.Recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	if command == "memory reject" || command == "memory invalidate" || command == "memory restore" {
		return localReview(ctx, store, command, o, pos[0], out)
	}
	renderer := &terminal.Renderer{Out: out, JSON: o.format == "json"}
	h := composeHost(store, o, client, renderer.Render)
	if command != "chat" {
		turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		switch command {
		case "run retry-memory":
			_, err = h.RetryMemory(turnCtx, pos[0])
		case "run retry-experience":
			_, err = h.RetryExperience(turnCtx, pos[0])
		case "memory accept":
			_, err = h.AcceptExperience(turnCtx, pos[0], o.note, expires)
		}
		return err
	}
	var s state.Session
	if store.FileMemoryEnabled() && o.resume != "" && !o.oneShot {
		// Interactive input is not known yet. Inspect the owned session now;
		// compare its exact runtime after reading the first task, before dispatch.
		s, err = store.Session(ctx, o.resume)
		if err == nil {
			err = store.SessionCanResume(ctx, s.ID)
		}
	} else {
		s, err = h.Session(ctx, o.resume)
	}
	if err != nil {
		return err
	}
	if o.format == "text" {
		if _, err := fmt.Fprintf(out, "Session %s\nWorkspace %s\nAgent local-claude · Claude %s · Jev %s/%s\n", s.ID, s.WorkingDirectory(), o.model, client.Provider(), client.Model()); err != nil {
			return err
		}
	}
	execute := func(input string) error {
		if strings.TrimSpace(input) == "" {
			return errors.New("message cannot be empty")
		}
		if len(input) > 16*1024 {
			return errors.New("message exceeds MVP limit of 16 KiB")
		}
		previous := s.RuntimeFingerprint
		o.retrievalQuery = input
		if err := resolveOptions(ctx, store, &o, true); err != nil {
			return err
		}
		h = composeHost(store, o, client, renderer.Render)
		if previous != o.runtimeFingerprint {
			var err error
			s, err = h.Session(ctx, "")
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(errout, "Runtime configuration changed; started a fresh session."); err != nil {
				return err
			}
		}
		turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		_, err := h.Run(turnCtx, s, input)
		return err
	}
	if o.oneShot {
		return execute(o.message)
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16*1024+1)
	type inputResult struct {
		text string
		err  error
		eof  bool
	}
	inputs := make(chan inputResult)
	readCtx, stopRead := context.WithCancel(ctx)
	defer stopRead()
	go func() {
		for scanner.Scan() {
			select {
			case inputs <- inputResult{text: scanner.Text()}:
			case <-readCtx.Done():
				return
			}
		}
		select {
		case inputs <- inputResult{err: scanner.Err(), eof: true}:
		case <-readCtx.Done():
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.WriteString(errout, "you> "); err != nil {
			return err
		}
		var next inputResult
		select {
		case <-ctx.Done():
			return ctx.Err()
		case next = <-inputs:
		}
		if next.eof {
			return next.err
		}
		if strings.TrimSpace(next.text) == "" {
			continue
		}
		if err := execute(next.text); err != nil {
			return err
		}
	}
}

func inspect(ctx context.Context, command string, o options, id string, out io.Writer) (inspectErr error) {
	s, err := state.Open(o.home, true)
	if err != nil {
		return err
	}
	defer func(owner *state.Store) { inspectErr = errors.Join(inspectErr, owner.Close()) }(s)
	s = s.WithPartition(o.agentID, o.audienceID)
	if err := resolveOptions(ctx, s, &o, false); err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	switch command {
	case "memory candidates":
		if o.runFilter != "" {
			r, err := s.Run(ctx, o.runFilter)
			if err != nil {
				return err
			}
			if r.Scope != o.scope {
				return errors.New("run belongs to another workspace")
			}
		}
		candidates, err := s.Candidates(ctx, o.scope, o.runFilter, o.status)
		if err != nil {
			return err
		}
		return enc.Encode(candidates)
	case "memory candidate":
		c, err := s.GetCandidate(ctx, id)
		if err != nil {
			return err
		}
		if c.Scope != o.scope {
			return errors.New("candidate belongs to another workspace")
		}
		evidence, err := s.CandidateEvidence(ctx, id)
		if err != nil {
			return err
		}
		return enc.Encode(struct {
			Candidate state.Candidate      `json:"candidate"`
			Evidence  []state.ToolEvidence `json:"evidence"`
		}{c, evidence})
	case "memory search":
		memories, err := s.Search(ctx, o.scope, id, 20)
		if err != nil {
			return err
		}
		return enc.Encode(memories)
	case "memory show":
		m, err := s.Memory(ctx, id)
		if err != nil {
			return err
		}
		if m.Scope != o.scope {
			return errors.New("memory belongs to another workspace")
		}
		return enc.Encode(m)
	case "run show", "run events":
		r, err := s.Run(ctx, id)
		if err != nil {
			return err
		}
		if !runMatchesSelection(r, o) {
			return errors.New("run belongs to another workspace")
		}
		if command == "run show" {
			return enc.Encode(r)
		}
		renderer := terminal.Renderer{Out: out, JSON: o.format == "json"}
		for _, e := range r.Events {
			if err := renderer.Render(e); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unsupported inspection command")
}

// Local review never calls a model or reruns a source task.
func localReview(ctx context.Context, s *state.Store, command string, o options, id string, out io.Writer) error {
	if command == "memory reject" {
		c, err := s.GetCandidate(ctx, id)
		if err != nil {
			return err
		}
		if c.Scope != o.scope {
			return errors.New("candidate belongs to another workspace")
		}
		if err := s.RejectCandidate(ctx, id, o.reason); err != nil {
			return err
		}
		c, err = s.GetCandidate(ctx, id)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(c)
	}
	m, err := s.Memory(ctx, id)
	if err != nil {
		return err
	}
	if m.Scope != o.scope {
		return errors.New("memory belongs to another workspace")
	}
	var changeErr error
	if command == "memory restore" {
		changeErr = s.RestoreMemory(ctx, id, o.reason)
	} else {
		changeErr = s.InvalidateMemory(ctx, id, o.reason)
	}
	if changeErr != nil {
		return changeErr
	}
	m, err = s.Memory(ctx, id)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(m)
}

// Inspection resolves identity without requiring the project's current files.
func resolveOptions(ctx context.Context, store *state.Store, o *options, execute bool) error {
	if len(o.projectIDs) > 1 {
		return resolveProjectSet(ctx, store, o, execute)
	}
	p, err := routing.Resolve(ctx, store, o.projectID, o.workspace)
	if err != nil {
		return err
	}
	if p.ID == "" && o.defaultScope != "" {
		p.Scope = o.defaultScope
	}
	o.projectID, o.projectName, o.scope, o.workspace = p.ID, p.Name, p.Scope, p.Workspace
	o.projects = nil
	if p.ID != "" {
		o.projects = []state.ProjectRef{{ID: p.ID, Name: p.Name, Scope: p.Scope, Workspace: p.Workspace}}
	}
	o.additionalDirectories = nil
	if execute {
		binary, err := exec.LookPath(o.binary)
		if err != nil {
			return errors.New("configured Claude executable is unavailable")
		}
		o.binary, err = filepath.Abs(binary)
		if err != nil {
			return err
		}
		o.binary, err = filepath.EvalSymlinks(o.binary)
		if err != nil {
			return fmt.Errorf("resolve Claude executable: %w", err)
		}
		if err := resolveKnowledge(o); err != nil {
			return err
		}
		if err := refreshFileRuntime(ctx, store, o); err != nil {
			return err
		}
		r, err := assembly.Assemble(p.ID, p.Name, p.Scope, p.Workspace, assemblyConfig(*o))
		if err != nil {
			return err
		}
		o.runtimeFingerprint = r.Fingerprint
		o.projects, o.additionalDirectories = r.Projects, r.AdditionalDirectories
	}
	return nil
}

// validateJevProvider accepts the official provider, or a custom provider with
// an explicit gateway endpoint. There is no automatic provider selection.
func validateJevProvider(provider, endpoint string) error {
	switch provider {
	case "typesafe":
		if endpoint != "" {
			return errors.New("jev-endpoint requires jev-provider custom")
		}
		return nil
	case "custom":
		if endpoint == "" {
			return errors.New("jev-provider custom requires jev-endpoint")
		}
		return jev.ValidateEndpoint(endpoint)
	}
	return errors.New("jev-provider must be typesafe or custom")
}

// newJevClient reads only the selected provider's credential variable.
func newJevClient(provider, endpoint, model string) (*jev.Client, error) {
	if provider == "custom" {
		return jev.NewCustom(os.Getenv("JEV_API_KEY"), model, endpoint)
	}
	return jev.New(os.Getenv("TYPESAFE_API_KEY"), model)
}
