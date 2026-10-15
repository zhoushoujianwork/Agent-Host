package host

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func TestPrivateUsersKeepMemoryAndCorrectionsSeparate(t *testing.T) {
	for _, files := range []bool{false, true} {
		name := "sqlite"
		if files {
			name = "files"
		}
		t.Run(name, func(t *testing.T) {
			h, runner := testHost(t)
			if files {
				root := t.TempDir()
				if _, err := h.Store.MigrateFileMemory(t.Context(), filepath.Join(root, "memory"), filepath.Join(root, "cache"), "test"); err != nil {
					t.Fatal(err)
				}
			}
			const statement = "I prefer concise English answers."
			h.Config.AudienceID = "user:alice"
			h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) { return saveStatement(), nil })
			first := runOK(t, h, newSession(t, h), statement)
			if first.MemoryStatus != "saved" {
				t.Fatal(first.MemoryStatus)
			}
			h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
				return memory.Decision{Kind: "none", Model: "fixture"}, nil
			})
			for _, audience := range []string{"user:bob", "owner", "group-example"} {
				h.Config.AudienceID = audience
				r := runOK(t, h, newSession(t, h), "How should you reply?")
				if len(r.Context) != 0 || strings.Contains(runner.requests[len(runner.requests)-1].Prompt, statement) {
					t.Fatal("private memory leaked", audience, r.Context)
				}
			}
			h.Config.AudienceID = "user:alice"
			fresh := runOK(t, h, newSession(t, h), "How should you reply?")
			if len(fresh.Context) != 1 || fresh.Context[0].SourceRunID != first.ID {
				t.Fatal("own memory lost", fresh.Context)
			}
			// Explicit profile correction uses the existing memory invalidate action.
			// It preserves the old record as evidence, excluding it from future recall.
			if err := h.Store.WithPartition("default", "user:alice").InvalidateMemory(t.Context(), fresh.Context[0].ID, "User corrected their response preference"); err != nil {
				t.Fatal(err)
			}
			h.Config.UserProfile = "Use detailed Chinese replies."
			corrected := runOK(t, h, newSession(t, h), "How should you reply?")
			request := runner.requests[len(runner.requests)-1]
			if len(corrected.Context) != 0 || strings.Contains(request.Prompt, statement) || !strings.Contains(request.Prompt, h.Config.UserProfile) {
				t.Fatal("old preference returned after correction", corrected.Context)
			}
		})
	}
}
