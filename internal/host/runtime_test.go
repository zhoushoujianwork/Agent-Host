package host

import (
	"context"
	"strings"
	"testing"

	"agent-host/internal/harness"
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

func TestPersonalHOTRefreshRotatesOnlyNewSessions(t *testing.T) {
	h, runner := testHost(t)
	h.Config.RuntimeFingerprint = "base-runtime"
	h.Config.PersonalHOTPersonID = "person-1"
	digest, content := "hot-v1", "first reviewed memory"
	h.Config.PersonalHOTDocuments = func(context.Context) ([]harness.PresetDocument, string, error) {
		return []harness.PresetDocument{{Name: "MEMORY.md", Content: []byte(content)}}, digest, nil
	}

	first := newSession(t, h)
	runOK(t, h, first, "first")
	if got := string(runner.requests[0].PresetDocuments[0].Content); got != content {
		t.Fatalf("first HOT content = %q", got)
	}
	if h.Config.RuntimeFingerprint != "base-runtime" {
		t.Fatalf("shared Host fingerprint mutated: %q", h.Config.RuntimeFingerprint)
	}

	digest, content = "hot-v2", "newly accepted memory"
	if _, err := h.Session(context.Background(), first.ID); err == nil || !strings.Contains(err.Error(), "start a new session") {
		t.Fatalf("old session resumed after HOT changed: %v", err)
	}
	if _, err := h.Run(context.Background(), first, "stale"); err == nil {
		t.Fatal("old session dispatched after HOT changed")
	}
	second := newSession(t, h)
	runOK(t, h, second, "second")
	if got := string(runner.requests[1].PresetDocuments[0].Content); got != content {
		t.Fatalf("refreshed HOT content = %q", got)
	}

	digest, content = "hot-v3-expired", ""
	h.Config.PersonalHOTDocuments = func(context.Context) ([]harness.PresetDocument, string, error) {
		return nil, digest, nil
	}
	if _, err := h.Session(context.Background(), second.ID); err == nil {
		t.Fatal("session survived HOT expiry")
	}
	third := newSession(t, h)
	runOK(t, h, third, "after expiry")
	if len(runner.requests[2].PresetDocuments) != 0 {
		t.Fatalf("expired HOT documents forwarded: %+v", runner.requests[2].PresetDocuments)
	}
}
