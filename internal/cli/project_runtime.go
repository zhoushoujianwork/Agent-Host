package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/projectruntime"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func runtimeCatalog(o *options) *projectruntime.Catalog {
	if o.projectCatalog != nil {
		return o.projectCatalog
	}
	return projectruntime.New(projectruntime.RootForState(o.home))
}

// Discovery only adds metadata candidates. The existing intent router decides
// whether the text requests actual project work before any enrollment occurs.
func runtimeCandidates(ctx context.Context, store *state.Store, o *options, text string) ([]state.Project, error) {
	known, err := store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	shared, err := runtimeCatalog(o).Projects(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]state.Project{}
	for _, p := range append(known, shared...) {
		if o.projectAllowlist != nil && !slices.Contains(o.projectAllowlist, p.ID) {
			continue
		}
		if old, ok := byID[p.ID]; ok && (old.Scope != p.Scope || old.Workspace != p.Workspace) {
			return nil, fmt.Errorf("shared project %q conflicts with entry-local identity", p.ID)
		}
		byID[p.ID] = p
	}
	known = nil
	for _, p := range byID {
		known = append(known, p)
	}
	// A configured allowlist is a boundary: no surrounding checkout discovery.
	if o.projectAllowlist != nil {
		return known, nil
	}
	workspace := o.defaultWorkspace
	if workspace == "" {
		workspace = o.workspace
	}
	discovered, err := runtimeCatalog(o).Candidates(ctx, workspace, known)
	if err != nil {
		return nil, fmt.Errorf("discover local projects: %w", err)
	}
	referenced := routing.ReferencedProjects(text, discovered)
	// Text references, the continuation task and the contextual checkout are
	// never dropped: a decision cannot be reached without them. Everything
	// else enrolled over time only fills the remaining semantic-routing budget.
	required := map[string]bool{}
	for _, p := range referenced {
		byID[p.ID] = p
		required[p.ID] = true
	}
	for _, recent := range o.projectRecent {
		for _, id := range recent.ProjectIDs {
			required[id] = true
		}
	}
	// Supply the current checkout for contextual requests ("this project").
	canonical, err := state.CanonicalWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	contextual := strings.Contains(strings.ToLower(text), "this project") || strings.Contains(strings.ToLower(text), "current project") || strings.Contains(text, "这个项目") || strings.Contains(text, "当前项目") || strings.Contains(text, "这个工程") || strings.Contains(text, "当前工程")
	for _, p := range discovered {
		if !contextual {
			break
		}
		if canonical == p.Workspace || strings.HasPrefix(canonical, p.Workspace+string(filepath.Separator)) {
			p.Description = strings.TrimSpace(p.Description + " Current execution checkout.")
			byID[p.ID] = p
			required[p.ID] = true
		}
	}
	result := []state.Project{}
	for _, p := range byID {
		result = append(result, p)
	}
	slices.SortFunc(result, func(a, b state.Project) int { return strings.Compare(a.ID, b.ID) })
	return capProjects(result, required)
}

// capProjects bounds the candidate set at routing.MaxProjects without
// silently dropping anything the current request actually needs: references,
// the continuation task and the contextual checkout always survive. Any
// surplus is trimmed from the rest, in deterministic alphabetical order, so
// growth in enrolled-but-unrelated projects never blocks an unrelated request.
func capProjects(projects []state.Project, required map[string]bool) ([]state.Project, error) {
	if len(projects) <= routing.MaxProjects {
		return projects, nil
	}
	kept := make([]state.Project, 0, routing.MaxProjects)
	var fillable []state.Project
	for _, p := range projects {
		if required[p.ID] {
			kept = append(kept, p)
		} else {
			fillable = append(fillable, p)
		}
	}
	if len(kept) > routing.MaxProjects {
		return nil, fmt.Errorf("project routing supports at most %d projects; this request alone needs %d", routing.MaxProjects, len(kept))
	}
	for _, p := range fillable {
		if len(kept) >= routing.MaxProjects {
			break
		}
		kept = append(kept, p)
	}
	slices.SortFunc(kept, func(a, b state.Project) int { return strings.Compare(a.ID, b.ID) })
	return kept, nil
}

func enrollRuntimeProjects(ctx context.Context, store *state.Store, o *options, projects []state.Project, origin projectruntime.Origin) error {
	c := runtimeCatalog(o)
	if origin.StateHome == "" && o.home != "" {
		home, err := filepath.Abs(o.home)
		if err != nil {
			return err
		}
		if canonical, err := filepath.EvalSymlinks(home); err == nil {
			home = canonical
		}
		origin.StateHome = home
	}
	for _, p := range projects {
		if o.projectAllowlist != nil && !slices.Contains(o.projectAllowlist, p.ID) {
			return errors.New("project is not configured for this agent")
		}
		// Explicit legacy non-Git workspaces retain their existing behavior.
		if existing, err := store.Project(ctx, p.ID); err == nil && !isGitCheckout(ctx, p.Workspace) {
			if existing.Workspace != p.Workspace || existing.Scope != p.Scope {
				return errors.New("legacy project selection changed identity")
			}
			continue
		}
		enrolled, err := c.Enroll(ctx, p, origin)
		if err != nil {
			return fmt.Errorf("enroll runtime project: %w", err)
		}
		if err := store.MirrorRuntimeProject(ctx, enrolled); err != nil {
			return err
		}
		if o.runtimeWorkspaces == nil {
			o.runtimeWorkspaces = map[string]string{}
		}
		o.runtimeWorkspaces[p.ID] = p.Workspace
	}
	return nil
}

// Only metadata is consulted, using the catalog's bounded Git probe.
func isGitCheckout(ctx context.Context, workspace string) bool {
	if ctx.Err() != nil {
		return false
	}
	for path := filepath.Clean(workspace); ; path = filepath.Dir(path) {
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			return true
		}
		if filepath.Dir(path) == path {
			return false
		}
	}
}

func autoResolveProject(ctx context.Context, store *state.Store, o *options) error {
	if o.projectAutomatic && o.routedQuery == o.retrievalQuery {
		return nil
	}
	if o.defaultWorkspace == "" {
		o.defaultWorkspace = o.workspace
	}
	o.workspace = o.defaultWorkspace
	o.projectID, o.projectName, o.primaryProject = "", "", ""
	o.projectIDs, o.projects, o.additionalDirectories = nil, nil, nil
	o.runtimeWorkspaces = map[string]string{}
	projects, err := runtimeCandidates(ctx, store, o, o.retrievalQuery)
	if err != nil {
		return err
	}
	o.projectAutomatic = true
	if len(projects) == 0 {
		o.routedQuery = o.retrievalQuery
		return nil
	}
	request := routing.Request{Text: o.retrievalQuery, Projects: projects, Recent: o.projectRecent}
	decider := o.projectRouter
	decision := routing.Decision{}
	matched := false
	if decider == nil {
		decision, matched, err = routing.ExactDecision(request)
		if err != nil {
			return err
		}
	}
	if decider == nil && !matched {
		client, err := newJevClient(o.provider, o.jevEndpoint, o.jevModel)
		if err != nil {
			return err
		}
		decider = routing.New(client)
	}
	if !matched {
		decision, err = decider.Decide(ctx, request)
	}
	if err != nil {
		return fmt.Errorf("route project request: %w", err)
	}
	if decision.ContinuationRunID != "" || decision.ContinuationMessageID != "" {
		if len(o.projectRecent) == 0 || decision.ContinuationRunID != o.projectRecent[0].RunID || decision.ContinuationMessageID != o.projectRecent[0].MessageID || !slices.Equal(decision.ProjectIDs, o.projectRecent[0].ProjectIDs) || decision.PrimaryID != o.projectRecent[0].PrimaryID {
			return errors.New("routing continuation does not match the latest actual task")
		}
	}
	if decision.Kind == "clarify" {
		return errors.New("project target is ambiguous; provide the local checkout name or path")
	}
	if decision.Kind == "default" {
		o.routedQuery = o.retrievalQuery
		return nil
	}
	if decision.Kind != "project" || len(decision.ProjectIDs) == 0 {
		return errors.New("project router returned an invalid selection")
	}
	selected := []state.Project{}
	seen := map[string]bool{}
	for _, id := range decision.ProjectIDs {
		if seen[id] {
			return errors.New("project router returned duplicate identities")
		}
		seen[id] = true
		found := false
		for _, p := range projects {
			if p.ID == id {
				if decision.ContinuationRunID != "" {
					if previous := o.previousWorkspaces[id]; previous != "" {
						p.Workspace = previous
					}
				}
				selected = append(selected, p)
				found = true
				break
			}
		}
		if !found {
			return errors.New("project router selected an unknown identity")
		}
	}
	if decision.PrimaryID != "" && !seen[decision.PrimaryID] {
		return errors.New("project router selected an unknown primary")
	}
	if err := enrollRuntimeProjects(ctx, store, o, selected, projectruntime.Origin{Entry: "cli", AgentID: o.agentID, AudienceID: o.audienceID}); err != nil {
		return err
	}
	o.projectIDs = append([]string(nil), decision.ProjectIDs...)
	o.primaryProject = decision.PrimaryID
	if len(o.projectIDs) == 1 {
		o.projectID = o.projectIDs[0]
	}
	o.routedQuery = o.retrievalQuery
	return nil
}

func rememberRoutingRun(o *options, run state.Run) {
	ids := make([]string, 0, len(run.Projects))
	o.previousWorkspaces = map[string]string{}
	for _, p := range run.Projects {
		ids = append(ids, p.ID)
		o.previousWorkspaces[p.ID] = p.Workspace
	}
	if len(ids) == 0 && run.ProjectID != "" {
		ids = append(ids, run.ProjectID)
		o.previousWorkspaces[run.ProjectID] = run.WorkingDirectory()
	}
	o.projectRecent = []routing.TaskContext{{RunID: run.ID, Text: run.Input, ProjectIDs: ids, PrimaryID: run.ProjectID}}
}

func runtimeReceiptSink(store *state.Store, o options, next event.Sink) event.Sink {
	return func(e event.Event) error {
		if (e.Type == "run.started" || e.Type == "run.finished") && (o.projectID != "" || len(o.projects) > 0) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := runtimeCatalog(&o)
			projects, err := c.Projects(ctx)
			if err != nil {
				return err
			}
			ids := map[string]bool{}
			for _, p := range projects {
				ids[p.ID] = true
			}
			managed := o.projectID == "" || ids[o.projectID]
			for _, p := range o.projects {
				managed = managed && ids[p.ID]
			}
			if managed {
				run, err := store.Run(ctx, e.RunID)
				if err != nil {
					return err
				}
				if err := c.RecordRun(ctx, o.home, run); err != nil {
					return fmt.Errorf("save shared project execution receipt: %w", err)
				}
			}
		}
		if next != nil {
			return next(e)
		}
		return nil
	}
}
