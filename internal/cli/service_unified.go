package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agent-host/internal/backup"
	"agent-host/internal/componentconfig"
	"agent-host/internal/dingtalkworkspace"
	"agent-host/internal/email"
	"agent-host/internal/harness/claude"
	"agent-host/internal/knowledge"
	"agent-host/internal/mcpstdio"
	"agent-host/internal/state"
)

type executionProfile struct {
	Execution       serviceConfig `json:"execution"`
	EnvironmentFile string        `json:"environment_file"`
}

type unifiedHost struct {
	Memory         *hostMemoryConfig           `json:"memory,omitempty"`
	Version        int                         `json:"version"`
	DefaultProfile string                      `json:"default_profile"`
	Profiles       map[string]executionProfile `json:"profiles"`
}

type hostMemoryConfig struct {
	Backend string `json:"backend"`
}

func strictUnifiedJSON(raw []byte, value any) error {
	if mcpstdio.Decode(raw, value) != nil {
		return errors.New("configuration must use documented fields and types")
	}
	return nil
}

func readEnvelope(path string) (componentconfig.Envelope, error) {
	var raw json.RawMessage
	if err := readServiceJSON(path, &raw); err != nil {
		return componentconfig.Envelope{}, err
	}
	return componentconfig.Decode(raw)
}

func unifiedDirectory(root string) string {
	if filepath.Base(filepath.Dir(root)) == "channels" {
		return filepath.Join(filepath.Dir(filepath.Dir(root)), "config")
	}
	return ""
}

func discoverUnifiedChannels(directory string) ([]configuredChannel, error) {
	if !filepath.IsAbs(directory) || filepath.Base(directory) != "config" || filepath.Clean(directory) != directory {
		return nil, errors.New("config-dir must be a clean absolute ROOT/config path")
	}
	if _, err := privateServicePath(directory, true); err != nil {
		return nil, err
	}
	actual, err := filepath.EvalSymlinks(directory)
	if err != nil || actual != directory {
		return nil, errors.New("configuration directory must not contain symlinks")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var channels []configuredChannel
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name != "credentials" && name != "agents" && name != "users" && name != "skills" {
				return nil, fmt.Errorf("unknown configuration directory %s", name)
			}
			continue
		}
		if name == "host.json" || name == "agents.json" || name == "CLAUDE.md" {
			continue
		}
		if name == "backup.json" {
			if _, err := privateServicePath(filepath.Join(directory, name), false); err != nil {
				return nil, err
			}
			if _, err := backup.LoadPolicy(filepath.Dir(directory)); err != nil {
				return nil, fmt.Errorf("backup policy: %w", err)
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("unknown configuration file %s", name)
		}
		if name == "plugins.email.json" || name == "plugins.knowledge.json" {
			return nil, errors.New("legacy plugin configuration requires datasource migration")
		}
		pluginID := componentconfig.DataSourceID(name)
		_, knownPlugin := componentconfig.DataSources[pluginID]
		if name != "channels.dingtalk.json" && name != "channels.feishu.json" && !knownPlugin {
			return nil, fmt.Errorf("unknown component %s", name)
		}
		e, err := readEnvelope(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if strings.HasPrefix(name, "channels.") && *e.Enabled {
			id := strings.TrimSuffix(strings.TrimPrefix(name, "channels."), ".json")
			channels = append(channels, configuredChannel{id, filepath.Join(filepath.Dir(directory), "channels", id)})
		}
	}
	if len(channels) == 0 {
		return nil, errors.New("no enabled channel configurations")
	}
	// Load all enabled plugins even when no channel selects them, before connecting.
	for _, id := range datasourceIDs() {
		if _, err := unifiedPlugin(directory, id, ""); err != nil {
			return nil, err
		}
	}
	for _, ch := range channels {
		if _, _, _, err := loadUnifiedService(ch.Root); err != nil {
			return nil, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
	}
	return channels, nil
}

func unifiedPlugin(directory, id, channel string) (string, error) {
	_, known := componentconfig.DataSources[id]
	if !known {
		return "", errors.New("unknown datasource plugin")
	}
	path := filepath.Join(directory, "datasource.plugins."+id+".json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	e, err := readEnvelope(path)
	if err != nil {
		return "", err
	}
	if !*e.Enabled {
		return "", nil
	}
	// Enabled datasource plugins are global; legacy access fields are ignored.
	if id == "email" {
		c, _, err := email.Load(path)
		if err != nil {
			return "", err
		}
		for _, a := range c.Accounts {
			if !a.Disabled {
				if _, err := privateServicePath(a.CredentialFile, false); err != nil {
					return "", fmt.Errorf("email account %s credential: %w", a.ID, err)
				}
			}
		}
	} else {
		c, err := knowledge.Load(path)
		if err != nil {
			return "", err
		}
		for _, source := range c.Sources {
			if _, err := knowledge.ResolveConfiguredSource(source); err != nil {
				return "", err
			}
		}
	}
	return path, nil
}

func loadUnifiedService(root string) (serviceLayout, serviceConfig, map[string]string, error) {
	directory := unifiedDirectory(root)
	for _, name := range []string{"plugins.email.json", "plugins.knowledge.json"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
			return serviceLayout{}, serviceConfig{}, nil, errors.New("legacy plugin configuration requires datasource migration")
		} else if !errors.Is(err, os.ErrNotExist) {
			return serviceLayout{}, serviceConfig{}, nil, err
		}
	}
	layout := serviceLayout{Root: root, Config: directory, State: filepath.Join(root, "state"), Workspace: filepath.Join(root, "workspace"), Native: filepath.Join(root, "native")}
	var zero serviceConfig
	for _, path := range []string{root, layout.State, layout.Workspace, layout.Native} {
		if _, err := os.Lstat(path); err == nil {
			if _, err := privateServicePath(path, true); err != nil {
				return layout, zero, nil, err
			}
			actual, err := filepath.EvalSymlinks(path)
			if err != nil || actual != path {
				return layout, zero, nil, errors.New("runtime directories must not contain symlinks")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return layout, zero, nil, err
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "config")); err == nil {
		return layout, zero, nil, errors.New("unified runtime must not contain legacy configuration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return layout, zero, nil, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return layout, zero, nil, errors.New("runtime root must be a clean absolute path")
	}
	var host unifiedHost
	if err := readServiceJSON(filepath.Join(directory, "host.json"), &host); err != nil {
		return layout, zero, nil, err
	}
	if host.Version != 1 {
		return layout, zero, nil, errors.New("host requires version 1")
	}
	if host.Memory != nil && host.Memory.Backend != "sqlite" && host.Memory.Backend != "files" {
		return layout, zero, nil, errors.New("memory backend must be sqlite or files")
	}
	id := filepath.Base(root)
	path := filepath.Join(directory, "channels."+id+".json")
	e, err := readEnvelope(path)
	if err != nil {
		return layout, zero, nil, err
	}
	if !*e.Enabled {
		return layout, zero, nil, errors.New("channel is disabled")
	}
	profile := e.ExecutionProfile
	if profile == "" {
		profile = host.DefaultProfile
	}
	p, ok := host.Profiles[profile]
	if !ok {
		return layout, zero, nil, errors.New("channel references an unknown execution profile")
	}
	c := p.Execution
	if host.Memory != nil && host.Memory.Backend == "files" {
		if _, err := os.Stat(filepath.Join(layout.State, "file-memory.json")); err != nil {
			return layout, zero, nil, errors.New("file memory requires explicit memory migrate before startup")
		}
		c.WorkspaceCache = filepath.Join(filepath.Dir(directory), "cache")
		store, err := state.OpenForMemoryInspection(layout.State)
		if err != nil {
			return layout, zero, nil, err
		}
		_, verifyErr := store.VerifyFileMemory(context.Background())
		if err := errors.Join(verifyErr, store.Close()); err != nil {
			return layout, zero, nil, err
		}
	}
	if host.Memory != nil && host.Memory.Backend == "sqlite" {
		if _, err := os.Stat(state.FileMemoryMarker(layout.State)); err == nil {
			return layout, zero, nil, errors.New("sqlite backend requires explicit memory rollback before startup")
		} else if !errors.Is(err, os.ErrNotExist) {
			return layout, zero, nil, err
		}
	}
	c.Channel = id
	c.channelConfigPath = path
	// Capability configurations belong to the unified registry, not profiles.
	if c.EmailConfig != "" || c.KnowledgeConfig != "" {
		return layout, c, nil, errors.New("execution profiles must not reference plugin configuration")
	}
	c.AgentsConfig = componentconfig.Resolve(filepath.Join(directory, "host.json"), c.AgentsConfig)
	c.DiscoveryConfig = componentconfig.Resolve(filepath.Join(directory, "host.json"), c.DiscoveryConfig)
	if c.MemoryScope == "" {
		c.MemoryScope = "channel:" + id + ":default"
	}
	if err := c.validate(); err != nil {
		return layout, c, nil, err
	}
	var env map[string]string
	if err := readServiceJSON(componentconfig.Resolve(filepath.Join(directory, "host.json"), p.EnvironmentFile), &env); err != nil {
		return layout, c, nil, err
	}
	for k, v := range env {
		if !slices.Contains(serviceEnvironmentKeys, k) || strings.TrimSpace(v) == "" || v != strings.TrimSpace(v) || strings.ContainsAny(v, "\x00\r\n") {
			return layout, c, nil, errors.New("invalid provider environment")
		}
	}
	if _, err := claude.NativeStreamEnvironment(env); err != nil {
		return layout, c, nil, err
	}
	if c.EmailConfig, err = unifiedPlugin(directory, "email", id); err != nil {
		return layout, c, nil, err
	}
	for _, pluginID := range datasourceIDs() {
		if pluginID == "email" {
			continue
		}
		path, err := unifiedPlugin(directory, pluginID, id)
		if err != nil {
			return layout, c, nil, err
		}
		if path != "" {
			c.KnowledgeConfigs = append(c.KnowledgeConfigs, path)
		}
	}
	c.KnowledgeChannel = id
	c.DisableKnowledge = len(c.KnowledgeConfigs) == 0
	if id == "dingtalk" {
		var settings struct {
			Bot json.RawMessage `json:"bot,omitempty"`
			DWS json.RawMessage `json:"dws,omitempty"`
		}
		if err := strictUnifiedJSON(e.Settings, &settings); err != nil {
			return layout, c, nil, err
		}
		botEnabled, err := featureEnabled(settings.Bot)
		if err != nil {
			return layout, c, nil, err
		}
		dwsEnabled, err := featureEnabled(settings.DWS)
		if err != nil {
			return layout, c, nil, err
		}
		if !botEnabled && !dwsEnabled {
			return layout, c, nil, errors.New("DingTalk requires enabled bot or dws")
		}
		c.DWSOnly = !botEnabled
		c.DisableDWS = !dwsEnabled
		if dwsEnabled {
			if _, _, err := dingtalkworkspace.Load(path); err != nil {
				return layout, c, nil, err
			}
			c.DWSConfig = path
		}
		if botEnabled {
			if _, err := loadBotConfig(path); err != nil {
				return layout, c, nil, err
			}
		}
		if c.DiscoveryConfig != "" && !dwsEnabled {
			return layout, c, nil, errors.New("DingTalk discovery requires enabled dws")
		}
	} else if id == "feishu" {
		if _, err := loadFeishuMeetingConfig(path); err != nil {
			return layout, c, nil, err
		}
	} else {
		return layout, c, nil, errors.New("unknown channel")
	}
	return layout, c, env, nil
}

func featureEnabled(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if err := strictUnifiedJSON(raw, &fields); err != nil {
		return false, err
	}
	var value bool
	if json.Unmarshal(fields["enabled"], &value) != nil {
		return false, errors.New("channel feature requires explicit enabled")
	}
	return value, nil
}

func prepareUnifiedRuntime(root string) error {
	for _, name := range []string{"", "state", "workspace", "native", "logs"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if _, err := privateServicePath(path, true); err != nil {
			return err
		}
		actual, err := filepath.EvalSymlinks(path)
		if err != nil || actual != path {
			return errors.New("runtime directories must not contain symlinks")
		}
	}
	return nil
}

func runDWSChannel(ctx context.Context, command string, layout serviceLayout, c serviceConfig, out, errout io.Writer) error {
	if _, _, err := dingtalkworkspace.Load(c.DWSConfig); err != nil {
		return err
	}
	if c.DiscoveryConfig != "" {
		if err := checkDiscoveryConfig(ctx, c.DiscoveryConfig, layout.State); err != nil {
			return err
		}
	}
	if command == "check" {
		return json.NewEncoder(out).Encode(map[string]any{"status": "local_configuration_valid", "bot_enabled": false, "dws_enabled": true, "runtime_root": layout.Root, "platform_verified": false})
	}
	if err := json.NewEncoder(out).Encode(map[string]any{"type": "channel.ready", "channel": "dingtalk", "bot_enabled": false, "dws_enabled": true}); err != nil {
		return err
	}
	if c.DiscoveryConfig != "" {
		return runDiscovery(ctx, []string{"serve", "--config", c.DiscoveryConfig, "--home", layout.State, "--claude", c.Claude}, out, errout)
	}
	<-ctx.Done()
	return nil
}

func datasourceIDs() []string {
	ids := make([]string, 0, len(componentconfig.DataSources))
	for id := range componentconfig.DataSources {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
