package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"agent-host/internal/assembly"
	"agent-host/internal/componentconfig"
	"agent-host/internal/history"
	"agent-host/internal/knowledge"
	"agent-host/internal/projectruntime"
	"agent-host/internal/state"
)

func containsProject(ids []string, id string) bool { return slices.Contains(ids, id) }

func resolveProjectSet(ctx context.Context, store *state.Store, o *options, execute bool) error {
	o.projects = nil
	seen := map[string]bool{}
	for _, id := range o.projectIDs {
		if seen[id] {
			continue
		}
		p, err := store.Project(ctx, id)
		if err != nil {
			return err
		}
		if workspace := o.runtimeWorkspaces[p.ID]; workspace != "" {
			p.Workspace = workspace
		}
		o.projects = append(o.projects, state.ProjectRef{ID: p.ID, Name: p.Name, Workspace: p.Workspace, Scope: p.Scope})
		seen[id] = true
	}
	if len(o.projects) < 2 {
		return errors.New("joint execution requires distinct projects")
	}
	slices.SortFunc(o.projects, func(a, b state.ProjectRef) int { return strings.Compare(a.ID, b.ID) })
	workspace := o.defaultWorkspace
	if workspace == "" {
		workspace = o.workspace
	}
	scope := o.defaultScope
	if scope == "" {
		scope = workspace
	}
	if o.primaryProject == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		workspace = filepath.Join(home, "workspace")
		if execute {
			if err := os.MkdirAll(workspace, 0700); err != nil {
				return err
			}
			workspace, err = filepath.EvalSymlinks(workspace)
			if err != nil {
				return err
			}
		}
	}
	o.projectID, o.projectName, o.scope, o.workspace = o.primaryProject, "Multiple projects", scope, workspace
	for _, p := range o.projects {
		if p.ID == o.primaryProject {
			o.projectName, o.scope, o.workspace = p.Name, p.Scope, p.Workspace
		}
	}
	if !execute {
		return nil
	}
	selected := make([]state.Project, 0, len(o.projects))
	for _, ref := range o.projects {
		p, err := store.Project(ctx, ref.ID)
		if err != nil {
			return err
		}
		p.Workspace = ref.Workspace
		selected = append(selected, p)
	}
	if !o.projectRoutingComplete {
		if err := enrollRuntimeProjects(ctx, store, o, selected, projectruntime.Origin{Entry: "cli", AgentID: o.agentID, AudienceID: o.audienceID}); err != nil {
			return err
		}
	}
	binary, err := exec.LookPath(o.binary)
	if err != nil {
		return errors.New("configured Claude executable is unavailable")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	o.binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	if err := resolveKnowledge(o); err != nil {
		return err
	}
	if err := refreshFileRuntime(ctx, store, o); err != nil {
		return err
	}
	r, err := assembly.AssembleProjects(o.projects, o.primaryProject, scope, workspace, assemblyConfig(*o))
	if err != nil {
		return err
	}
	o.projectID, o.projectName, o.scope, o.workspace = r.ProjectID, r.ProjectName, r.Scope, r.Workspace
	o.projects, o.additionalDirectories, o.runtimeFingerprint = r.Projects, r.AdditionalDirectories, r.Fingerprint
	return nil
}

func runMatchesSelection(run state.Run, o options) bool {
	if o.projectID != "" {
		for _, p := range run.Projects {
			if p.ID == o.projectID {
				return true
			}
		}
		return run.ProjectID == o.projectID || len(run.Projects) == 0 && run.Scope == o.scope
	}
	return run.Scope == o.scope
}

func runtimeOptions(o options) assembly.Runtime {
	return assembly.Runtime{AgentID: o.agentID, AudienceID: o.audienceID, NativeProfile: o.nativeProfile, PermissionMode: o.permissionMode, HostInstructions: o.hostInstructions, Projects: o.projects, AdditionalDirectories: o.additionalDirectories, ProjectID: o.projectID, ProjectName: o.projectName, Scope: o.scope, Workspace: o.workspace, Fingerprint: o.runtimeFingerprint}
}

func assemblyConfig(o options) assembly.Configuration {
	policy := ""
	if o.historyEnabled && !o.noTools {
		policy = history.PolicyVersion
	}
	return assembly.Configuration{AgentID: o.agentID, AudienceID: o.audienceID, HostInstructions: o.hostInstructions, UserProfile: o.userProfile, MemoryFingerprint: fileRuntimeCompatibility(o), PermissionMode: o.permissionMode, Binary: o.binary, Model: o.model, NativeProfile: o.nativeProfile, ToolsDisabled: o.noTools, AllowedTools: o.allowedTools, KnowledgeFingerprint: knowledge.Fingerprint(o.knowledgeSources), ConversationHistoryPolicy: policy, ContextPolicy: o.contextPolicy, EmailFingerprint: o.emailFingerprint, DingTalkFingerprint: o.dingtalkWorkspaceFingerprint}
}

func selectKnowledgeForOptions(o options) ([]knowledge.ResolvedSource, error) {
	if globalKnowledgePlugins(o) {
		return knowledge.SelectMany(knowledgePaths(o), nil, nil, "", "")
	}
	ids := make([]string, 0, len(o.projects))
	for _, p := range o.projects {
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 && o.projectID != "" {
		ids = append(ids, o.projectID)
	}
	audience := o.audienceID
	if audience == "" {
		audience = "owner"
	}
	sources, err := knowledge.Catalog(knowledgePaths(o), o.knowledgeChannel)
	if err != nil {
		return nil, err
	}
	names, err := knowledge.ResolveNames(sources, o.knowledgeNames)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range knowledgePaths(o) {
		pluginID := componentconfig.DataSourceID(path)
		if o.pluginNames == nil || slices.Contains(o.pluginNames, "knowledge") || slices.Contains(o.pluginNames, pluginID) {
			paths = append(paths, path)
		}
	}
	if names != nil {
		available, err := knowledge.Catalog(paths, o.knowledgeChannel)
		if err != nil {
			return nil, err
		}
		filtered := make([]string, 0, len(names))
		for _, name := range names {
			for _, source := range available {
				if source.Name == name {
					filtered = append(filtered, name)
				}
			}
		}
		names = filtered
	}
	return knowledge.SelectMany(paths, ids, names, audience, o.knowledgeChannel)
}

func knowledgePaths(o options) []string {
	if o.knowledgeConfigs != nil {
		return o.knowledgeConfigs
	}
	if o.knowledgeConfig != "" {
		return []string{o.knowledgeConfig}
	}
	return nil
}

func knowledgePluginsEnabled(names []string) bool {
	if names == nil || slices.Contains(names, "knowledge") {
		return true
	}
	for _, id := range names {
		if provider, known := componentconfig.DataSources[id]; known && provider != "" {
			return true
		}
	}
	return false
}

// Unified datasource plugins are enabled globally at startup. Legacy direct
// knowledge configurations retain their existing selectors.
func globalKnowledgePlugins(o options) bool {
	paths := knowledgePaths(o)
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if componentconfig.DataSourceID(path) == "" {
			return false
		}
	}
	return true
}
