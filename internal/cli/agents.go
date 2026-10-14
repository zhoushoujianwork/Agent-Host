package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/componentconfig"
	"agent-host/internal/decision/jev"
	"agent-host/internal/event"
	"agent-host/internal/host"
	"agent-host/internal/knowledge"
	"agent-host/internal/projectruntime"
	"agent-host/internal/state"
)

// Instance configuration is local policy, never inferred from message content.
type agentDefinition struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	InstructionsFile string   `json:"instructions_file,omitempty"`
	InstructionsDir  string   `json:"instructions_dir,omitempty"`
	UserFile         string   `json:"user_file,omitempty"`
	Workspace        string   `json:"workspace,omitempty"`
	Projects         []string `json:"projects"`
	Plugins          []string `json:"plugins,omitempty"`
	KnowledgeSources []string `json:"knowledge_sources"`
	Model            string   `json:"model,omitempty"`
	role             string
	contextFiles     []assembly.ContextFile
	userProfile      string
}

type agentBinding struct {
	ConversationID string `json:"conversation_id"`
	AgentID        string `json:"agent_id"`
}

type agentCatalog struct {
	DefaultAgent  string            `json:"default_agent"`
	Agents        []agentDefinition `json:"agents"`
	GroupBindings []agentBinding    `json:"group_bindings,omitempty"`
}

var agentIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func loadAgents(path string) (agentCatalog, error) {
	if path == "" {
		return agentCatalog{DefaultAgent: "default", Agents: []agentDefinition{{ID: "default", Name: "Default agent"}}}, nil
	}
	var c agentCatalog
	if err := readServiceJSON(path, &c); err != nil {
		return c, fmt.Errorf("agent configuration: %w", err)
	}
	if len(c.Agents) == 0 || len(c.Agents) > 128 || len(c.GroupBindings) > 4096 {
		return c, errors.New("agent configuration requires 1–128 instances and at most 4096 group bindings")
	}
	seen := map[string]bool{}
	for i := range c.Agents {
		a := &c.Agents[i]
		if !agentIdentifier.MatchString(a.ID) || seen[a.ID] {
			return c, errors.New("agent IDs must be unique letters, digits, hyphens or underscores (1–64 characters)")
		}
		seen[a.ID] = true
		if strings.TrimSpace(a.Name) == "" || len(a.Name) > 200 || strings.ContainsAny(a.Name, "\x00\r\n") {
			return c, errors.New("agent name must be a nonempty single line")
		}
		if a.Model != "" && (a.Model != strings.TrimSpace(a.Model) || strings.HasPrefix(a.Model, "-") || len(a.Model) > 512 || strings.ContainsAny(a.Model, "\x00\r\n")) {
			return c, errors.New("agent model must be a valid model name")
		}
		for _, name := range a.Plugins {
			if _, known := componentconfig.DataSources[name]; !known && name != "knowledge" {
				return c, errors.New("agent references unknown plugin")
			}
		}
		for _, values := range [][]string{a.Projects, a.KnowledgeSources} {
			unique := map[string]bool{}
			for _, value := range values {
				if value == "" || value != strings.TrimSpace(value) || unique[value] || strings.ContainsAny(value, "\x00\r\n") {
					return c, errors.New("agent project and knowledge selections must contain distinct nonempty identifiers")
				}
				unique[value] = true
			}
		}
		if a.Workspace != "" {
			if !filepath.IsAbs(a.Workspace) {
				return c, errors.New("agent workspace must be absolute")
			}
			actual, err := filepath.EvalSymlinks(a.Workspace)
			if err != nil {
				return c, fmt.Errorf("agent %s workspace is unavailable", a.ID)
			}
			info, err := os.Stat(actual)
			if err != nil || !info.IsDir() {
				return c, fmt.Errorf("agent %s workspace must be a directory", a.ID)
			}
			a.Workspace = actual
		}
		if err := a.loadContextFiles(filepath.Dir(path)); err != nil {
			return c, fmt.Errorf("agent %s: %w", a.ID, err)
		}
	}
	if !seen[c.DefaultAgent] {
		return c, errors.New("default_agent must identify a configured instance")
	}
	groups := map[string]bool{}
	for _, b := range c.GroupBindings {
		if b.ConversationID == "" || len(b.ConversationID) > 512 || strings.ContainsAny(b.ConversationID, "\x00\r\n") || groups[b.ConversationID] || !seen[b.AgentID] {
			return c, errors.New("group bindings require distinct conversation IDs and existing agents")
		}
		groups[b.ConversationID] = true
	}
	return c, nil
}

func (c agentCatalog) definition(id string) (agentDefinition, error) {
	for _, a := range c.Agents {
		if a.ID == id {
			return a, nil
		}
	}
	return agentDefinition{}, errors.New("selected agent is no longer configured")
}

func (c agentCatalog) selectAgent(account string, m dingtalk.Message) (channel.AgentSelection, error) {
	id, audience := c.DefaultAgent, "owner"
	if m.ConversationType == "2" {
		audience = channel.Key("group-audience-v1", "dingtalk", account, m.TenantID, m.ConversationID)
		for _, b := range c.GroupBindings {
			if b.ConversationID == m.ConversationID {
				id = b.AgentID
				break
			}
		}
	}
	if _, err := c.definition(id); err != nil {
		return channel.AgentSelection{}, err
	}
	return channel.AgentSelection{AgentID: id, AudienceID: audience}, nil
}

func (c agentCatalog) validateReferences(ctx context.Context, store *state.Store, base options) error {
	for _, a := range c.Agents {
		for _, id := range a.Projects {
			if store == nil {
				return errors.New("configured agent projects require an existing project registry")
			}
			p, err := store.Project(ctx, id)
			if err != nil {
				return fmt.Errorf("agent %s references an unavailable project", a.ID)
			}
			actual, err := filepath.EvalSymlinks(p.Workspace)
			info, statErr := os.Stat(p.Workspace)
			if err != nil || statErr != nil || !info.IsDir() || actual != p.Workspace {
				return fmt.Errorf("agent %s project directory is unavailable or changed", a.ID)
			}
		}
		if a.KnowledgeSources != nil && !base.knowledgePluginDisabled && knowledgePluginsEnabled(a.Plugins) {
			sources, err := knowledge.Catalog(knowledgePaths(base), base.knowledgeChannel)
			if err != nil {
				return err
			}
			if _, err := knowledge.ResolveNames(sources, a.KnowledgeSources); err != nil {
				return fmt.Errorf("agent %s: %w", a.ID, err)
			}
		}
	}
	return nil
}

func (a agentDefinition) projects(ctx context.Context, store *state.Store) ([]state.Project, error) {
	projects, err := store.Projects(ctx)
	if err != nil || a.Projects == nil {
		return projects, err
	}
	selected := make([]state.Project, 0, len(a.Projects))
	for _, p := range projects {
		if slices.Contains(a.Projects, p.ID) {
			selected = append(selected, p)
		}
	}
	return selected, nil
}

func (a agentDefinition) options(base options, audience string, group, prepare bool) (options, error) {
	o := base
	o.projectAllowlist = a.Projects
	o.agentID, o.audienceID = a.ID, audience
	o.hostInstructions = host.ChannelInstructions(a.role, group)
	o.contextFiles = append([]assembly.ContextFile(nil), a.contextFiles...)
	o.userProfile = ""
	if audience == state.OwnerAudienceID && !group && a.UserFile != "" {
		o.userProfile = a.userProfile
		o.contextFiles = append(o.contextFiles, assembly.ContextFile{Name: "USER.md", Source: a.UserFile, Content: a.userProfile})
	}
	o.permissionMode = "bypassPermissions"
	if a.Model != "" {
		o.model = a.Model
	}
	if a.Workspace != "" {
		o.workspace, o.defaultWorkspace = a.Workspace, a.Workspace
		if o.defaultScope == "" {
			o.defaultScope = a.Workspace
		}
		o.projectID, o.projectName, o.scope, o.primaryProject = "", "", "", ""
		o.projectIDs, o.projects, o.additionalDirectories = nil, nil, nil
	}
	o.knowledgeNames = a.KnowledgeSources
	o.pluginNames = a.Plugins
	profile := base.nativeProfile
	if profile == "" {
		profile = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if profile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return o, err
		}
		profile = filepath.Join(home, ".claude")
	}
	o.nativeProfile = profile
	if a.ID != "default" || audience != "owner" {
		o.nativeProfile = filepath.Join(profile, "instances", a.ID, "audiences", channel.Key(audience))
		if prepare {
			if err := prepareAgentProfile(profile, o.nativeProfile); err != nil {
				return o, err
			}
		}
	}
	return o, nil
}

// Only managed settings are shared, never conversation state or instruction files.
func prepareAgentProfile(base, destination string) error {
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	if _, err := privateServicePath(destination, true); err != nil {
		return fmt.Errorf("agent native profile: %w", err)
	}
	settings, err := os.ReadFile(filepath.Join(base, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		settings = []byte("{}\n")
	} else if err != nil {
		return errors.New("cannot read shared native settings")
	}
	path := filepath.Join(destination, "settings.json")
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(settings) {
		return nil
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("agent native settings must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, settings, 0600)
}

func checkAgents(ctx context.Context, agents agentCatalog, o options) error {
	var store *state.Store
	if _, err := os.Stat(filepath.Join(o.home, "state.db")); err == nil {
		var err error
		store, err = state.OpenForProjectInspection(o.home)
		if err != nil {
			return err
		}
		defer store.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect agent project registry")
	}
	if err := agents.validateReferences(ctx, store, o); err != nil {
		return err
	}
	for _, a := range agents.Agents {
		selected, err := a.options(o, "owner", false, false)
		if err != nil {
			return err
		}
		if err := resolveKnowledge(&selected); err != nil {
			return err
		}
		if _, err := assembly.Assemble("", "", selected.workspace, selected.workspace, assemblyConfig(selected)); err != nil {
			return err
		}
	}
	return nil
}

func resolveAgentChannel(ctx context.Context, parent *channel.Service, agents agentCatalog, base options, client *jev.Client, sink event.Sink, message state.ChannelMessage) (*channel.Service, error) {
	provider := parent.Channel
	if provider == "" {
		provider = "dingtalk"
	}
	if provider == state.ReplyTargetFeishu && message.Channel == state.ReplyTargetFeishuMeeting {
		provider = message.Channel
	}
	id, audience := message.AgentID, message.AudienceID
	if id == "" && audience == "" && message.ConversationType != "2" {
		id, audience = "default", "owner"
	}
	if provider != "dingtalk" && (message.Channel != provider || message.AccountID != parent.ClientID) || audience == "" || message.ConversationType == "2" && audience != channel.Key("group-audience-v1", provider, parent.ClientID, message.TenantID, message.ConversationID) || message.ConversationType != "2" && audience != "owner" {
		return nil, errors.New("message has an incompatible audience binding")
	}
	a, err := agents.definition(id)
	if err != nil {
		return nil, err
	}
	o, err := a.options(base, audience, message.ConversationType == "2", true)
	if err != nil {
		return nil, err
	}
	o.retrievalQuery = message.Text
	for _, supplement := range message.Supplements {
		o.retrievalQuery += "\n" + supplement.Text
	}
	if provider == state.ReplyTargetFeishuMeeting {
		o.hostInstructions = host.MeetingChannelInstructions(a.role)
	}
	store := parent.Store.WithPartition(id, audience)
	child := *parent
	child.Store, child.ResolveAgent, child.SelectAgent = store, nil, nil
	child.SelectMessageAgent = nil
	child.Projects = func(ctx context.Context) ([]state.Project, error) { return a.projects(ctx, store) }
	o.runtimeWorkspaces = make(map[string]string)
	configureChannelRuntimeProjects(&child, store, o, provider)
	resolve := func(ctx context.Context, ids []string, primary string) (channel.Host, assembly.Runtime, error) {
		for _, id := range ids {
			if a.Projects != nil && !slices.Contains(a.Projects, id) {
				return nil, assembly.Runtime{}, errors.New("project is not configured for this agent")
			}
		}
		selected := o
		selected.projectRoutingComplete = true
		selected.projectIDs = append([]string(nil), ids...)
		selected.projectID, selected.projectName, selected.scope = "", "", ""
		selected.workspace, selected.primaryProject = o.defaultWorkspace, primary
		if len(ids) == 1 {
			selected.projectID = ids[0]
		}
		if err := resolveOptions(ctx, store, &selected, true); err != nil {
			return nil, assembly.Runtime{}, err
		}
		for _, p := range selected.projects {
			if a.Projects != nil && !slices.Contains(a.Projects, p.ID) {
				return nil, assembly.Runtime{}, errors.New("workspace resolves to a project not configured for this agent")
			}
		}
		return composeHost(store, selected, client, sink), runtimeOptions(selected), nil
	}
	child.ResolveProjects = resolve
	child.ResolveHost = func(ctx context.Context, id string) (channel.Host, assembly.Runtime, error) {
		var ids []string
		if id != "" {
			ids = []string{id}
		}
		return resolve(ctx, ids, "")
	}
	child.Host = composeHost(store, o, client, sink)
	child.Scope = o.defaultScope
	return &child, nil
}

// All channel adapters use the same local lookup and runtime enrollment path.
// An explicit Agent allowlist restricts both candidates and enrollment.
func configureChannelRuntimeProjects(service *channel.Service, store *state.Store, base options, entry string) {
	service.PrepareProjects = func(ctx context.Context, m state.ChannelMessage, _ []state.Project) ([]state.Project, error) {
		selected := base
		text := m.Text
		for _, supplement := range m.Supplements {
			text += "\n" + supplement.Text
		}
		return runtimeCandidates(ctx, store, &selected, text)
	}
	service.EnrollProjects = func(ctx context.Context, m state.ChannelMessage, projects []state.Project) error {
		for _, project := range projects {
			if base.projectAllowlist != nil && !slices.Contains(base.projectAllowlist, project.ID) {
				return errors.New("project is not configured for this agent")
			}
		}
		selected := base
		return enrollRuntimeProjects(ctx, store, &selected, projects, projectruntime.Origin{Entry: entry, AgentID: m.AgentID, AudienceID: m.AudienceID, MessageID: m.MessageID})
	}
}
