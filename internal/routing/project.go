package routing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"agent-host/internal/state"
)

// Resolve selects only registered projects or the explicit legacy/default cwd.
// Directory availability is checked by assembly when executing, not inspecting.
func Resolve(ctx context.Context, store *state.Store, id, workspace string) (state.Project, error) {
	if id != "" {
		p, err := store.Project(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("unknown project %q", id)
		}
		return p, err
	}
	p, err := store.ProjectByWorkspace(ctx, workspace)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	// A moved project's old path remains its knowledge namespace. It must not
	// become the default namespace of a different directory occupying that path.
	if owner, err := store.ProjectByScope(ctx, workspace); err == nil {
		return p, fmt.Errorf("workspace knowledge belongs to registered project %q; use --project", owner.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	return state.Project{Name: "Default workspace", Workspace: workspace, Scope: workspace}, nil
}
