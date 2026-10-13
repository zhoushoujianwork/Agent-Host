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
	"agent-host/internal/history"
	"agent-host/internal/knowledge"
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
	return assembly.Configuration{AgentID: o.agentID, AudienceID: o.audienceID, HostInstructions: o.hostInstructions, PermissionMode: o.permissionMode, Binary: o.binary, Model: o.model, NativeProfile: o.nativeProfile, ToolsDisabled: o.noTools, AllowedTools: o.allowedTools, KnowledgeFingerprint: knowledge.Fingerprint(o.knowledgeSources), ConversationHistoryPolicy: policy, ContextPolicy: o.contextPolicy, EmailFingerprint: o.emailFingerprint}
}

func selectKnowledgeForOptions(o options) ([]knowledge.ResolvedSource, error) {
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
	return knowledge.SelectAgentAudienceProjects(o.knowledgeConfig, ids, o.knowledgeNames, audience)
}
