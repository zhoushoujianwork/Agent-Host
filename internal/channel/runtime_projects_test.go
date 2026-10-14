package channel

import (
	"context"
	"errors"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/host"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func TestRuntimeProjectLookupEnrollsOnlySelectedRequest(t *testing.T) {
	f := newServiceFixture(t)
	defaultWorkspace, err := state.CanonicalWorkspace(f.host.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	f.host.Config.Scope = defaultWorkspace
	workspace := t.TempDir()
	candidate := state.Project{ID: "pop", Name: "pop", Workspace: workspace, Scope: workspace}
	enrollments := 0
	f.service.PrepareProjects = func(_ context.Context, _ state.ChannelMessage, known []state.Project) ([]state.Project, error) {
		if len(known) == 0 {
			return []state.Project{candidate}, nil
		}
		return known, nil
	}
	f.service.EnrollProjects = func(ctx context.Context, m state.ChannelMessage, projects []state.Project) error {
		if m.MessageID != "inspect" || len(projects) != 1 || projects[0].ID != candidate.ID {
			t.Fatalf("enrolled unrelated or incorrect request: %+v %+v", m, projects)
		}
		enrollments++
		_, err := f.service.Store.RegisterProject(ctx, candidate.ID, candidate.Name, candidate.Workspace)
		return err
	}
	f.service.Router = serviceRouter(func(_ context.Context, request routing.Request) (routing.Decision, error) {
		if len(request.Projects) != 1 || request.Projects[0].ID != candidate.ID {
			t.Fatalf("local candidate did not reach routing: %+v", request)
		}
		if request.Text == "Inspect project pop authentication." {
			return routing.Decision{Kind: "project", ProjectIDs: []string{candidate.ID}, PrimaryID: candidate.ID}, nil
		}
		return routing.Decision{Kind: "default"}, nil
	})
	f.service.ResolveHost = func(ctx context.Context, id string) (Host, assembly.Runtime, error) {
		project := state.Project{Workspace: f.host.Config.Scope, Scope: f.host.Config.Scope}
		if id != "" {
			var err error
			project, err = f.service.Store.Project(ctx, id)
			if err != nil {
				return nil, assembly.Runtime{}, err
			}
		}
		runtime := assembly.Runtime{ProjectID: project.ID, ProjectName: project.Name, Workspace: project.Workspace, Scope: project.Scope, Fingerprint: "runtime-lookup-test"}
		config := f.host.Config
		config.ProjectID, config.ProjectName, config.Workspace, config.Scope = project.ID, project.Name, project.Workspace, project.Scope
		config.RuntimeFingerprint = runtime.Fingerprint
		return &host.Host{Store: f.service.Store, Runner: f.runner, Decider: f.host.Decider, Config: config}, runtime, nil
	}
	general := f.process(t, f.message(t, "greeting", "Hello", "text", "dm"))
	if general.ProjectID != "" || enrollments != 0 {
		t.Fatal("greeting enrolled or selected a project", general, enrollments)
	}
	projects, err := f.service.Store.Projects(t.Context())
	if err != nil || len(projects) != 0 {
		t.Fatal("lookup registered candidates before selection", projects, err)
	}
	inspect := f.process(t, f.message(t, "inspect", "Inspect project pop authentication.", "text", "dm"))
	if inspect.ProjectID != candidate.ID || inspect.RunID == "" || enrollments != 1 {
		t.Fatal("local request did not enroll and execute", inspect, enrollments)
	}
	requests := f.runner.snapshot()
	canonical, err := state.CanonicalWorkspace(workspace)
	if err != nil || len(requests) != 2 || requests[1].Workspace != canonical {
		var workspaces []string
		for _, request := range requests {
			workspaces = append(workspaces, request.Workspace)
		}
		t.Fatal("native invocation did not use the selected local checkout", workspaces, inspect.Status, inspect.Error, err)
	}
}

func TestRuntimeProjectEnrollmentRejectsInvalidDecision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision routing.Decision
	}{
		{"unknown", routing.Decision{Kind: "project", ProjectIDs: []string{"other"}, PrimaryID: "other"}},
		{"duplicate", routing.Decision{Kind: "project", ProjectIDs: []string{"pop", "pop"}}},
		{"foreign-primary", routing.Decision{Kind: "project", ProjectIDs: []string{"pop"}, PrimaryID: "other"}},
		{"invented-continuation", routing.Decision{Kind: "project", ProjectIDs: []string{"pop"}, ContinuationMessageID: "invented"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			f.service.PrepareProjects = func(context.Context, state.ChannelMessage, []state.Project) ([]state.Project, error) {
				return []state.Project{{ID: "pop", Name: "pop", Workspace: t.TempDir()}}, nil
			}
			f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) { return tc.decision, nil })
			f.service.EnrollProjects = func(context.Context, state.ChannelMessage, []state.Project) error {
				t.Fatal("invalid selection reached runtime enrollment")
				return nil
			}
			if _, _, _, err := f.service.routeTask(t.Context(), state.ChannelMessage{Text: "Inspect project pop", Binding: "fixture"}); err == nil {
				t.Fatal("invalid runtime selection was accepted")
			}
		})
	}
}

func TestRuntimeProjectPreparationFailureIsVisible(t *testing.T) {
	f := newServiceFixture(t)
	want := errors.New("local project lookup failed")
	f.service.PrepareProjects = func(context.Context, state.ChannelMessage, []state.Project) ([]state.Project, error) {
		return nil, want
	}
	if _, _, _, err := f.service.routeTask(t.Context(), state.ChannelMessage{Text: "Inspect project pop", Binding: "fixture"}); !errors.Is(err, want) {
		t.Fatal("lookup failure was hidden", err)
	}
}
