package host

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/experience"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func enableHostFiles(t *testing.T, h *Host) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.MigrateFileMemory(t.Context(), filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory"), "host-test"); err != nil {
		t.Fatal(err)
	}
	h.Config.WorkspaceCache = filepath.Join(root, "cache")
	h.Config.MemorySource = "host-test"
}

func TestFileHostAutomaticSkillPublicationFreshUseAndBlacklist(t *testing.T) {
	h, runner, judge := experienceHost(t)
	enableHostFiles(t, h)
	judge.compare = func(_ context.Context, _ state.Candidate, items []state.Memory) (experience.Relations, error) {
		for _, m := range items {
			if m.Kind == "procedure" {
				return experience.Relations{DuplicateIDs: []string{m.ID}}, nil
			}
		}
		return experience.Relations{}, nil
	}
	first := runOK(t, h, newSession(t, h), "Fix the fixture decrement test")
	c := onlyCandidate(t, h, first.ID)
	if c.Status != "accepted" || c.MemoryID == "" {
		t.Fatalf("not automatically published: %+v", c)
	}
	m, err := h.Store.Memory(t.Context(), c.MemoryID)
	if err != nil || !strings.Contains(m.ReviewNote, "Automatically") {
		t.Fatal(m, err)
	}
	runner.tools = false
	second := runOK(t, h, newSession(t, h), "fixture decrement test")
	req := runner.requests[len(runner.requests)-1]
	if len(second.Context) != 1 || req.SkillPluginDir == "" || req.SkillPluginDigest == "" {
		t.Fatal("skill missing from fresh task", second.Context, req.SkillPluginDir)
	}
	if strings.Contains(req.Prompt, m.Steps[0]) {
		t.Fatal("full skill procedure was duplicated in prompt")
	}
	if err := h.Store.InvalidateMemory(t.Context(), m.ID, "Wrong approach"); err != nil {
		t.Fatal(err)
	}
	runner.tools = true
	third := runOK(t, h, newSession(t, h), "Fix the fixture decrement test again")
	excluded := onlyCandidate(t, h, third.ID)
	if excluded.Status != "rejected" {
		t.Fatalf("disabled lesson returned: %+v", excluded)
	}
	if runner.requests[len(runner.requests)-1].SkillPluginDir != "" {
		t.Fatal("disabled skill mounted")
	}
	items, err := h.Store.Search(t.Context(), h.Config.Scope, "fixture decrement", 10)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}

func TestFileHostDisabledUserStatementCannotReturn(t *testing.T) {
	h, _ := testHost(t)
	enableHostFiles(t, h)
	h.Decider = deciderFunc(func(_ context.Context, input string, items []state.Memory) (memory.Decision, error) {
		d := memory.Decision{Kind: "preference", Store: true}
		for _, m := range items {
			if m.Text == input {
				d.DuplicateIDs = append(d.DuplicateIDs, m.ID)
			}
		}
		return d, nil
	})
	first := runOK(t, h, newSession(t, h), "我偏好简短的中文回复。")
	items, err := h.Store.Memories(t.Context(), h.Config.Scope)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if err := h.Store.InvalidateMemory(t.Context(), items[0].ID, "withdrawn"); err != nil {
		t.Fatal(err)
	}
	second := runOK(t, h, newSession(t, h), first.Input)
	if second.MemoryStatus != "unchanged" || !strings.Contains(string(second.Decision), "disabled_or_unresolved") {
		t.Fatal(second.MemoryStatus, string(second.Decision))
	}
	if err := h.Store.RestoreMemory(t.Context(), items[0].ID, "Explicitly adopt this preference again"); err != nil {
		t.Fatal(err)
	}
	restored, err := h.Store.Memory(t.Context(), items[0].ID)
	if err != nil || restored.Status != "active" || restored.Revision != 3 {
		t.Fatal(restored, err)
	}
}

func TestFileHostAutomaticComparisonRetryDoesNotReplayExecution(t *testing.T) {
	h, runner, judge := experienceHost(t)
	enableHostFiles(t, h)
	judge.compare = func(context.Context, state.Candidate, []state.Memory) (experience.Relations, error) {
		return experience.Relations{}, errors.New("comparison unavailable")
	}
	r, err := h.Run(t.Context(), newSession(t, h), "Fix the fixture decrement")
	if err == nil {
		t.Fatal("comparison error hidden")
	}
	if r.ExecutionStatus != "completed" {
		t.Fatal("business execution was lost", r)
	}
	count := len(runner.requests)
	judge.compare = nil
	if _, err := h.RetryExperience(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != count {
		t.Fatal("retry dispatched business execution")
	}
	if onlyCandidate(t, h, r.ID).Status != "accepted" {
		t.Fatal("pending publication not recovered")
	}
}
