package host

import (
	"context"
	"testing"

	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func TestProjectScopeSurvivesDirectoryChangeAndSessionDoesNot(t *testing.T) {
	h, runner := testHost(t)
	a, b := t.TempDir(), t.TempDir()
	h.Config.Scope, h.Config.Workspace = "project:api", a
	h.Config.ProjectID, h.Config.ProjectName, h.Config.RuntimeFingerprint = "api", "API", "configuration-v1"
	decisions := 0
	h.Decider = deciderFunc(func(_ context.Context, _ string, _ []state.Memory) (memory.Decision, error) {
		decisions++
		if decisions == 1 {
			return saveStatement(), nil
		}
		return memory.Decision{Kind: "none", Model: "fixture"}, nil
	})
	s := newSession(t, h)
	first := runOK(t, h, s, "This project prefers SQLite.")
	if first.Workspace != a || first.Scope != "project:api" || runner.requests[0].Workspace != a {
		t.Fatalf("runtime conflated: %+v %+v", first, runner.requests)
	}
	h.Config.Workspace = b
	if _, err := h.Session(context.Background(), s.ID); err == nil {
		t.Fatal("resumed across directory change")
	}
	if _, err := h.Run(context.Background(), s, "continue"); err == nil {
		t.Fatal("dispatched mismatched session")
	}
	fresh := newSession(t, h)
	second := runOK(t, h, fresh, "Which database does this project prefer?")
	if second.Workspace != b || second.Scope != first.Scope || len(second.Context) != 1 || runner.requests[1].Workspace != b || runner.requests[1].Resume {
		t.Fatalf("memory/session after move: %+v %+v", second, runner.requests)
	}
	h.Config.RuntimeFingerprint = "configuration-v2"
	if _, err := h.Session(context.Background(), fresh.ID); err == nil {
		t.Fatal("resumed across runtime policy change")
	}
}
