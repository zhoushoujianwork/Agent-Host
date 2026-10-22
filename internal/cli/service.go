package cli

import (
	"agent-host/internal/harness/acp"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"agent-host/internal/harness/claude"
)

const serviceUsage = `Managed channel runtime (DingTalk by default).

  agent-host service check --config-dir ABSOLUTE_ROOT/config
  agent-host service run --config-dir ABSOLUTE_ROOT/config --viewer-source ABSOLUTE_PATH [--no-browser]
  agent-host service check --root ABSOLUTE_PATH
  agent-host service check --channels-dir ABSOLUTE_PATH
  agent-host service run --root ABSOLUTE_PATH [--no-browser]
  agent-host service run --channels-dir ABSOLUTE_PATH --viewer-source ABSOLUTE_PATH [--no-browser]
  agent-host service workbench --config-dir ABSOLUTE_ROOT/config --viewer-source ABSOLUTE_PATH [--no-browser]

make run checks host.json, channels.NAME.json and datasource.plugins.NAME.json
from ROOT/config, gracefully restarts the same instance, then runs in the foreground.
Version 1 and explicit enabled are required; disabled components do not connect.
DingTalk settings.bot and settings.dws are independently optional.
Channel data lives under ROOT/channels/NAME and plugin state under ROOT/datasource/plugins/NAME.
Changes require restarting. The explicit legacy commands below remain supported.

The Go CLI scans channel registrations, prepares viewers,
and supervises every enabled runtime. Each registration has enabled and runtime_root.
Runtime paths may be relative to the registration directory. No channel directory
preserves the legacy --root setup; an existing directory with no enabled channels fails.
--viewer-source enables runtime locking and supervised startup for a single --root too.
Configuration comes from ROOT/config/component-service.json, component-environment.json and
channel-dingtalk.json, channel-feishu.json for channel feishu, or channel-feishu-meeting.json for
channel feishu-meeting, plus the optional private agents_config file. Native Claude
profiles are isolated by agent and audience under ROOT/native.
Optional execution_config selects a shared ACP profile; Agent overrides apply
to every channel. Auxiliary tool-free extraction keeps its separate Claude runner.
check validates local files and configuration without connecting, executing
Claude, or creating host state. It does not verify platform or model credentials.
run starts the installed read-only workbench and opens the default browser.
Use --viewer-port to change the fixed default port 8765.
Use --no-browser to keep the workbench available without opening a browser.
workbench starts only the CLI observer, without channel or model credentials.
It observes ROOT/cli-state; use chat --home ABSOLUTE_ROOT/cli-state for the same runtime.
`

type serviceConfig struct {
	SkillsRoot                 string   `json:"skills_root,omitempty"`
	ExecutionConfig            string   `json:"execution_config,omitempty"`
	WorkspaceCache             string   `json:"-"`
	KnowledgeConfigs           []string `json:"-"`
	KnowledgeChannel           string   `json:"-"`
	DisableKnowledge           bool     `json:"-"`
	DWSOnly                    bool     `json:"-"`
	DisableDWS                 bool     `json:"-"`
	DWSConfig                  string   `json:"-"`
	MemoryScope                string   `json:"memory_scope,omitempty"`
	channelConfigPath          string
	EmailConfig                string   `json:"email_config,omitempty"`
	Channel                    string   `json:"channel,omitempty"`
	MaxConcurrentConversations int      `json:"max_concurrent_conversations,omitempty"`
	AgentsConfig               string   `json:"agents_config,omitempty"`
	KnowledgeConfig            string   `json:"knowledge_config,omitempty"`
	DiscoveryConfig            string   `json:"discovery_config,omitempty"`
	Claude                     string   `json:"claude"`
	Model                      string   `json:"model"`
	JevProvider                string   `json:"jev_provider"`
	JevEndpoint                string   `json:"jev_endpoint,omitempty"`
	JevModel                   string   `json:"jev_model"`
	Timeout                    string   `json:"timeout"`
	NoTools                    bool     `json:"no_tools"`
	AllowedTools               []string `json:"allowed_tools"`
}

type serviceLayout struct {
	Root, Config, State, Workspace, Native string
}

// The managed process gets provider configuration from its own private file.
// Preserve ordinary execution environment such as PATH and certificate paths.
var serviceEnvironmentKeys = []string{
	"TYPESAFE_API_KEY", "JEV_API_KEY", "ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
	"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK", "CLAUDE_CODE_MAX_OUTPUT_TOKENS",
}

var serviceClearedEnvironmentKeys = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL",
	"ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_SUBAGENT_MODEL",
}

var serviceEnvironmentMu sync.Mutex

func runService(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, serviceUsage)
		return err
	}
	command := args[0]
	if command != "check" && command != "run" && command != "workbench" {
		return errors.New("service requires check, run or workbench")
	}
	fs := flag.NewFlagSet("service "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var root string
	fs.StringVar(&root, "root", "", "Absolute managed runtime directory")
	var channelsDirectory string
	var configDirectory string
	fs.StringVar(&configDirectory, "config-dir", "", "Unified ROOT/config directory")
	fs.StringVar(&channelsDirectory, "channels-dir", "", "Private directory of enabled channel configurations")
	var noBrowser bool
	var viewerPython string
	var viewerSource string
	var viewerPort int
	var noWorkbench bool
	var restart bool
	if command == "run" || command == "workbench" {
		fs.BoolVar(&restart, "restart", false, "Gracefully restart the same managed runtime if already running")
		fs.IntVar(&viewerPort, "viewer-port", 8765, "Fixed loopback port for the unified workbench")
		fs.BoolVar(&noWorkbench, "no-workbench", false, "Internal worker mode: use the supervisor workbench")
		fs.BoolVar(&noBrowser, "no-browser", false, "Do not open the default browser")
		fs.StringVar(&viewerPython, "viewer-python", "python3", "Python 3.10+ executable for the workbench")
		fs.StringVar(&viewerSource, "viewer-source", "", "Tracked workbench assets; enables managed startup and runtime locking")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("service commands do not accept positional arguments")
	}
	if command != "check" && (viewerPort < 1 || viewerPort > 65535) {
		return errors.New("viewer-port must be between 1 and 65535")
	}
	if restart && (noWorkbench || configDirectory == "" && channelsDirectory == "" && viewerSource == "") {
		return errors.New("restart requires managed startup with config-dir, channels-dir or viewer-source")
	}
	if command == "workbench" {
		if configDirectory == "" || root != "" || channelsDirectory != "" || restart || noWorkbench {
			return errors.New("service workbench requires config-dir and does not accept root, channels-dir, restart or no-workbench")
		}
		return runCLIWorkbench(ctx, configDirectory, viewerSource, viewerPython, viewerPort, noBrowser, errout)
	}
	if configDirectory != "" {
		if channelsDirectory != "" || root != "" {
			return errors.New("config-dir cannot be combined with root or channels-dir")
		}
		return runConfiguredChannels(ctx, command, configDirectory, "", viewerSource, viewerPython, viewerPort, noBrowser, restart, out, errout)
	}
	if channelsDirectory != "" || viewerSource != "" {
		return runConfiguredChannels(ctx, command, channelsDirectory, root, viewerSource, viewerPython, viewerPort, noBrowser, restart, out, errout)
	}
	layout, config, environment, err := loadService(root)
	if err != nil {
		return err
	}
	if command == "run" && !noWorkbench {
		stop, err := startServiceWorkbench(ctx, layout, viewerPython, viewerPort, !noBrowser, errout, openWorkbenchURL)
		if err != nil {
			fmt.Fprintf(errout, "Workbench unavailable: %v; channel startup will continue.\n", err)
		} else {
			defer stop()
		}
	}
	return withServiceEnvironment(layout.Native, environment, func() error {
		if config.DWSOnly {
			return runDWSChannel(ctx, command, layout, config, out, errout)
		}
		channelArgs := config.arguments(layout, "serve")
		run := runDingTalk
		if config.Channel == "feishu-meeting" || config.Channel == "feishu" {
			run = runFeishuMeeting
		}
		if command == "run" {
			return run(ctx, channelArgs, out, errout)
		}
		channelArgs[0] = "check"
		var checked bytes.Buffer
		if err := run(ctx, channelArgs, &checked, errout); err != nil {
			return err
		}
		var report map[string]any
		if err := json.Unmarshal(checked.Bytes(), &report); err != nil {
			return errors.New("managed local check returned an invalid report")
		}
		report["runtime_root"] = layout.Root
		report["config_directory"] = layout.Config
		report["state_directory"] = layout.State
		report["workspace"] = layout.Workspace
		report["native_config_directory"] = layout.Native
		return json.NewEncoder(out).Encode(report)
	})
}

func loadService(root string) (serviceLayout, serviceConfig, map[string]string, error) {
	if directory := unifiedDirectory(root); directory != "" {
		if _, err := os.Lstat(filepath.Join(directory, "host.json")); err == nil {
			return loadUnifiedService(root)
		}
	}
	var layout serviceLayout
	var config serviceConfig
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return layout, config, nil, errors.New("service --root must be a clean absolute path")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return layout, config, nil, errors.New("service root must exist without symbolic links in its path")
	}
	layout = serviceLayout{Root: root, Config: filepath.Join(root, "config"), State: filepath.Join(root, "state"), Workspace: filepath.Join(root, "workspace"), Native: filepath.Join(root, "native")}
	for label, path := range map[string]string{"root": layout.Root, "config": layout.Config, "state": layout.State, "workspace": layout.Workspace, "native": layout.Native} {
		if _, err := privateServicePath(path, true); err != nil {
			return layout, config, nil, fmt.Errorf("service %s directory: %w", label, err)
		}
	}
	servicePath, err := serviceConfigurationPath(layout.Config, "component-service.json", "service.json")
	if err != nil {
		return layout, config, nil, err
	}
	if err := readServiceJSON(servicePath, &config); err != nil {
		return layout, config, nil, fmt.Errorf("service configuration: %w", err)
	}
	if err := config.validate(); err != nil {
		return layout, config, nil, err
	}
	var environment map[string]string
	environmentPath, err := serviceConfigurationPath(layout.Config, "component-environment.json", "environment.json")
	if err != nil {
		return layout, config, nil, err
	}
	if err := readServiceJSON(environmentPath, &environment); err != nil {
		return layout, config, nil, fmt.Errorf("service environment: %w", err)
	}
	for key, value := range environment {
		allowed := false
		for _, known := range serviceEnvironmentKeys {
			allowed = allowed || key == known
		}
		if !allowed || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") || value != strings.TrimSpace(value) {
			return layout, config, nil, errors.New("service environment must contain only documented provider or native stream keys with nonempty single-line string values")
		}
	}
	if _, err := claude.NativeStreamEnvironment(environment); err != nil {
		return layout, config, nil, fmt.Errorf("service native stream configuration: %w", err)
	}
	if config.Channel == "feishu-meeting" || config.Channel == "feishu" {
		filename := "feishu-meeting.json"
		if config.Channel == "feishu" {
			filename = "feishu.json"
		}
		path, err := serviceConfigurationPath(layout.Config, "channel-"+filename, filename)
		if err != nil {
			return layout, config, nil, err
		}
		if _, err := loadFeishuMeetingConfig(path); err != nil {
			return layout, config, nil, err
		}
		config.channelConfigPath = path
		return layout, config, environment, nil
	}
	var bot botConfigFile
	botPath, err := serviceConfigurationPath(layout.Config, "channel-dingtalk.json", "dingtalk.json")
	if err != nil {
		return layout, config, nil, err
	}
	if err := readServiceJSON(botPath, &bot); err != nil {
		return layout, config, nil, fmt.Errorf("service DingTalk configuration: %w", err)
	}
	config.channelConfigPath = botPath
	if !filepath.IsAbs(bot.ClientSecretFile) {
		return layout, config, nil, errors.New("service DingTalk credential path must be absolute")
	}
	if _, err := privateServicePath(bot.ClientSecretFile, false); err != nil {
		return layout, config, nil, fmt.Errorf("service DingTalk credential: %w", err)
	}
	return layout, config, environment, nil
}

func (c serviceConfig) validate() error {
	if c.SkillsRoot != "" && (!filepath.IsAbs(c.SkillsRoot) || filepath.Clean(c.SkillsRoot) != c.SkillsRoot || strings.ContainsAny(c.SkillsRoot, "\x00\r\n")) {
		return errors.New("service skills_root must be a clean absolute path")
	}
	if c.MemoryScope != "" && (strings.TrimSpace(c.MemoryScope) != c.MemoryScope || len(c.MemoryScope) > 4096 || strings.ContainsAny(c.MemoryScope, "\x00\r\n")) {
		return errors.New("service memory_scope must be a nonempty single-line identity")
	}
	if c.Channel != "" && c.Channel != "dingtalk" && c.Channel != "feishu-meeting" && c.Channel != "feishu" {
		return errors.New("service channel must be dingtalk, feishu or feishu-meeting")
	}
	if (c.Channel == "feishu-meeting" || c.Channel == "feishu") && c.DiscoveryConfig != "" {
		return errors.New("Feishu meeting service does not collect DingTalk discovery conversations")
	}
	if c.AgentsConfig != "" && (!filepath.IsAbs(c.AgentsConfig) || filepath.Clean(c.AgentsConfig) != c.AgentsConfig || strings.ContainsAny(c.AgentsConfig, "\x00\r\n")) {
		return errors.New("service agents_config must be a clean absolute path")
	}
	if c.EmailConfig != "" && (!filepath.IsAbs(c.EmailConfig) || filepath.Clean(c.EmailConfig) != c.EmailConfig || strings.ContainsAny(c.EmailConfig, "\x00\r\n")) {
		return errors.New("service email_config must be a clean absolute path")
	}
	if c.KnowledgeConfig != "" && (!filepath.IsAbs(c.KnowledgeConfig) || filepath.Clean(c.KnowledgeConfig) != c.KnowledgeConfig || strings.ContainsAny(c.KnowledgeConfig, "\x00\r\n")) {
		return errors.New("service knowledge_config must be a clean absolute path")
	}
	if c.DiscoveryConfig != "" && (!filepath.IsAbs(c.DiscoveryConfig) || filepath.Clean(c.DiscoveryConfig) != c.DiscoveryConfig || strings.ContainsAny(c.DiscoveryConfig, "\x00\r\n")) {
		return errors.New("service discovery_config must be a clean absolute path")
	}
	if !filepath.IsAbs(c.Claude) || strings.ContainsAny(c.Claude, "\x00\r\n") {
		return errors.New("service claude must be an absolute executable path")
	}
	if c.ExecutionConfig != "" {
		if _, _, err := acp.Load(c.ExecutionConfig); err != nil {
			return err
		}
	}
	for _, model := range []string{c.Model, c.JevModel} {
		if strings.TrimSpace(model) == "" || model != strings.TrimSpace(model) || len(model) > 512 || strings.ContainsAny(model, "\x00\r\n") || strings.HasPrefix(model, "-") {
			return errors.New("service model and jev_model must be explicit valid model names")
		}
	}
	if err := validateJevProvider(c.JevProvider, c.JevEndpoint); err != nil {
		return fmt.Errorf("service %w", err)
	}
	timeout, err := time.ParseDuration(c.Timeout)
	if err != nil || timeout <= 0 {
		return errors.New("service timeout must be an explicit positive duration")
	}
	if c.MaxConcurrentConversations < 0 || c.MaxConcurrentConversations > 32 {
		return errors.New("service max_concurrent_conversations must be between 1 and 32, or omitted for 4")
	}
	if c.NoTools && len(c.AllowedTools) > 0 {
		return errors.New("service no_tools cannot be combined with allowed_tools")
	}
	if len(c.AllowedTools) > 128 {
		return errors.New("service allowed_tools exceeds 128 rules")
	}
	for _, rule := range c.AllowedTools {
		if strings.TrimSpace(rule) == "" || rule != strings.TrimSpace(rule) || len(rule) > 1024 || strings.ContainsAny(rule, "\x00\r\n,") || strings.HasPrefix(rule, "-") {
			return errors.New("service allowed_tools must contain nonempty individual native permission rules")
		}
	}
	return nil
}

func (c serviceConfig) arguments(layout serviceLayout, command string) []string {
	filename := "dingtalk.json"
	if c.Channel == "feishu-meeting" {
		filename = "feishu-meeting.json"
	} else if c.Channel == "feishu" {
		filename = "feishu.json"
	}
	path := c.channelConfigPath
	if path == "" {
		path = filepath.Join(layout.Config, "channel-"+filename)
	}
	args := []string{command, "--config", path, "--home", layout.State,
		"--workspace", layout.Workspace, "--claude", c.Claude, "--model", c.Model,
		"--jev-provider", c.JevProvider, "--jev-model", c.JevModel, "--timeout", c.Timeout, "--format", "json"}
	concurrency := c.MaxConcurrentConversations
	if concurrency == 0 {
		concurrency = 4
	}
	args = append(args, "--max-concurrent-conversations", fmt.Sprint(concurrency))
	if c.JevEndpoint != "" {
		args = append(args, "--jev-endpoint", c.JevEndpoint)
	}
	args = append(args, "--native-profile", layout.Native)
	if c.SkillsRoot != "" {
		args = append(args, "--skills-root", c.SkillsRoot)
	}
	if c.ExecutionConfig != "" {
		args = append(args, "--execution-config", c.ExecutionConfig)
	}
	if c.MemoryScope != "" {
		args = append(args, "--memory-scope", c.MemoryScope)
	}
	if c.WorkspaceCache != "" {
		args = append(args, "--workspace-cache", c.WorkspaceCache)
	}
	if c.DisableKnowledge {
		args = append(args, "--disable-knowledge-plugin")
	}
	if c.DisableDWS {
		args = append(args, "--disable-dingtalk-workspace")
	}
	if c.DWSConfig != "" {
		args = append(args, "--dingtalk-workspace-config", c.DWSConfig)
	}
	if c.AgentsConfig != "" {
		args = append(args, "--agents-config", c.AgentsConfig)
	}
	if c.NoTools {
		args = append(args, "--no-tools")
	}
	if c.EmailConfig != "" {
		args = append(args, "--email-config", c.EmailConfig)
	}
	if c.KnowledgeConfig != "" {
		args = append(args, "--knowledge-config", c.KnowledgeConfig)
	}
	for _, path := range c.KnowledgeConfigs {
		args = append(args, "--knowledge-config", path)
	}
	if c.KnowledgeChannel != "" {
		args = append(args, "--knowledge-channel", c.KnowledgeChannel)
	}
	if c.DiscoveryConfig != "" {
		args = append(args, "--discovery-config", c.DiscoveryConfig)
	}
	for _, rule := range c.AllowedTools {
		args = append(args, "--allowed-tools", rule)
	}
	return args
}

// Keep old runtimes readable while making new configuration names unambiguous.
// Never silently choose between two files representing the same component.
func serviceConfigurationPath(directory, canonical, legacy string) (string, error) {
	current := filepath.Join(directory, canonical)
	old := filepath.Join(directory, legacy)
	_, currentErr := os.Lstat(current)
	_, oldErr := os.Lstat(old)
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return "", currentErr
	}
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return "", oldErr
	}
	if currentErr == nil && oldErr == nil {
		return "", fmt.Errorf("conflicting configuration files: %s and %s", canonical, legacy)
	}
	if currentErr == nil {
		return current, nil
	}
	if oldErr == nil {
		return old, nil
	}
	return current, nil
}

func privateServicePath(path string, directory bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, errors.New("must be an existing non-symlink directory or regular file of the expected kind")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("must belong to the current user and deny group/other access")
	}
	return info, nil
}

func readServiceJSON(path string, value any) error {
	before, err := privateServicePath(path, false)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open private configuration file")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return errors.New("configuration file changed while opening")
	}
	owner, ok := after.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || after.Mode().Perm()&0077 != 0 {
		return errors.New("configuration file protection changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 32*1024+1))
	if err != nil || len(raw) > 32*1024 {
		return errors.New("configuration file could not be read within 32 KiB")
	}
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return errors.New("configuration has invalid or duplicate fields")
	}
	// Reject duplicate top-level keys as well as trailing values. Decode errors
	// deliberately exclude raw input, which may contain credentials.
	keys := json.NewDecoder(bytes.NewReader(raw))
	first, err := keys.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("configuration must be a JSON object")
	}
	seen := make(map[string]bool)
	for keys.More() {
		token, err := keys.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[strings.ToLower(key)] {
			return errors.New("configuration has invalid or duplicate fields")
		}
		seen[strings.ToLower(key)] = true
		var field json.RawMessage
		if err := keys.Decode(&field); err != nil {
			return errors.New("configuration contains invalid JSON")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("configuration must use the documented JSON fields and types")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("configuration has trailing data")
	}
	return nil
}

// Check nested instance definitions too; duplicate policy fields are ambiguous.
func uniqueJSONKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return errors.New("duplicate JSON field")
			}
			seen[strings.ToLower(name)] = true
			if err := uniqueJSONKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := uniqueJSONKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

func withServiceEnvironment(native string, configured map[string]string, run func() error) (runErr error) {
	serviceEnvironmentMu.Lock()
	defer serviceEnvironmentMu.Unlock()
	type previousValue struct {
		value   string
		present bool
	}
	previous := make(map[string]previousValue)
	keys := append(append([]string{}, serviceEnvironmentKeys...), serviceClearedEnvironmentKeys...)
	keys = append(keys, "CLAUDE_CONFIG_DIR")
	for _, key := range keys {
		value, present := os.LookupEnv(key)
		previous[key] = previousValue{value, present}
	}
	defer func() {
		for key, old := range previous {
			var err error
			if old.present {
				err = os.Setenv(key, old.value)
			} else {
				err = os.Unsetenv(key)
			}
			if err != nil {
				runErr = errors.Join(runErr, errors.New("cannot restore process environment"))
			}
		}
	}()
	for _, key := range keys {
		if err := os.Unsetenv(key); err != nil {
			return errors.New("cannot clear inherited provider environment")
		}
	}
	for key, value := range configured {
		if err := os.Setenv(key, value); err != nil {
			return errors.New("cannot apply managed provider environment")
		}
	}
	if err := os.Setenv("CLAUDE_CONFIG_DIR", native); err != nil {
		return errors.New("cannot isolate native Claude configuration")
	}
	return run()
}
