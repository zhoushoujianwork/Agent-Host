package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var projectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// CanonicalWorkspace validates an existing directory and resolves its symlinks.
func CanonicalWorkspace(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", errors.New("project workspace is required")
	}
	path, err := filepath.Abs(workspace)
	if err != nil {
		return "", wrap("resolve project workspace", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", wrap("resolve project workspace", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", wrap("inspect project workspace", err)
	}
	if !info.IsDir() {
		return "", errors.New("project workspace must be a directory")
	}
	return filepath.Clean(path), nil
}

func validateProjectID(id string) error {
	if !projectIDPattern.MatchString(id) || id == "default" {
		return errors.New("project ID must be 1–64 lowercase ASCII letters, digits, underscores or hyphens, start with a letter or digit, and cannot be default")
	}
	return nil
}

const projectColumns = "id,name,workspace,scope,created_at,updated_at,description,aliases"

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var createdAt, updatedAt string
	var aliases []byte
	err := row.Scan(&p.ID, &p.Name, &p.Workspace, &p.Scope, &createdAt, &updatedAt, &p.Description, &aliases)
	if err == nil {
		err = json.Unmarshal(aliases, &p.Aliases)
		if len(p.Aliases) == 0 {
			p.Aliases = nil
		}
	}
	if err == nil {
		p.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	}
	if err == nil {
		p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	}
	return p, wrap("read project", err)
}

func readProject(ctx context.Context, q rowQuerier, id string) (Project, error) {
	return scanProject(q.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
}

func (s *Store) Project(ctx context.Context, id string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, "SELECT "+s.projectColumns()+" FROM projects WHERE id=?", id))
}

func (s *Store) projectColumns() string {
	if s.legacyProjectMetadata {
		return "id,name,workspace,scope,created_at,updated_at,'' AS description,'[]' AS aliases"
	}
	return projectColumns
}

func (s *Store) ProjectByWorkspace(ctx context.Context, workspace string) (Project, error) {
	path, err := CanonicalWorkspace(workspace)
	if err != nil {
		return Project{}, err
	}
	return scanProject(s.db.QueryRowContext(ctx, "SELECT "+s.projectColumns()+" FROM projects WHERE workspace=?", path))
}

func (s *Store) ProjectByScope(ctx context.Context, scope string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, "SELECT "+s.projectColumns()+" FROM projects WHERE scope=?", scope))
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+s.projectColumns()+" FROM projects ORDER BY id")
	if err != nil {
		return nil, wrap("list projects", err)
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, wrap("list projects", rows.Err())
}

// MirrorRuntimeProject installs the shared runtime identity in this entry's
// compatibility catalog without moving historical memory or changing owners.
func (s *Store) MirrorRuntimeProject(ctx context.Context, p Project) error {
	if err := validateProjectID(p.ID); err != nil {
		return err
	}
	path, err := CanonicalWorkspace(p.Workspace)
	if err != nil {
		return err
	}
	if p.Scope == "" {
		return errors.New("runtime project scope is required")
	}
	p.Workspace = path
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = time.Now().UTC()
	aliases, err := json.Marshal(p.Aliases)
	if err != nil {
		return err
	}
	if len(p.Aliases) == 0 {
		aliases = []byte("[]")
	}
	return wrap("mirror runtime project", s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=? OR workspace=? OR scope=?", p.ID, path, p.Scope)
		if err != nil {
			return err
		}
		for rows.Next() {
			old, err := scanProject(rows)
			if err != nil {
				rows.Close()
				return err
			}
			if old.ID != p.ID || old.Workspace != p.Workspace || old.Scope != p.Scope {
				rows.Close()
				return fmt.Errorf("runtime project conflicts with local project %q", old.ID)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO projects(id,name,workspace,scope,created_at,updated_at,description,aliases) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,aliases=excluded.aliases,updated_at=excluded.updated_at`, p.ID, p.Name, path, p.Scope, p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano), p.Description, aliases)
		return err
	}))
}

// RegisterProject adopts an exact historical directory scope without modifying
// any run, memory revision, candidate or evidence payload.
func (s *Store) RegisterProject(ctx context.Context, id, name, workspace string) (Project, error) {
	var p Project
	if err := validateProjectID(id); err != nil {
		return p, err
	}
	path, err := CanonicalWorkspace(workspace)
	if err != nil {
		return p, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	now := time.Now().UTC()
	p = Project{ID: id, Name: name, Workspace: path, Scope: "project:" + id, CreatedAt: now, UpdatedAt: now}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var reserved string
		err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? OR workspace=? OR scope=?", id, path, path).Scan(&reserved)
		if err == nil {
			return fmt.Errorf("project ID, workspace or historical scope already belongs to project %q", reserved)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var legacy bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE scope=? UNION ALL SELECT 1 FROM memories WHERE scope=? UNION ALL SELECT 1 FROM candidates WHERE scope=? UNION ALL SELECT 1 FROM sessions WHERE json_extract(payload,'$.scope')=?)`, path, path, path, path).Scan(&legacy)
		if err != nil {
			return err
		}
		if legacy {
			p.Scope = path
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO projects(id,name,workspace,scope,created_at,updated_at) VALUES(?,?,?,?,?,?)", p.ID, p.Name, p.Workspace, p.Scope, p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))
		return err
	})
	return p, wrap("register project", err)
}

// UpdateProject changes the current checkout while retaining its memory scope.
func (s *Store) UpdateProject(ctx context.Context, id, name, workspace string) (Project, error) {
	var p Project
	path, err := CanonicalWorkspace(workspace)
	if err != nil {
		return p, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = readProject(ctx, tx, id)
		if err != nil {
			return err
		}
		var reserved string
		err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id<>? AND (workspace=? OR scope=?)", id, path, path).Scan(&reserved)
		if err == nil {
			return fmt.Errorf("workspace or historical scope already belongs to project %q", reserved)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p.Workspace = path
		if strings.TrimSpace(name) != "" {
			p.Name = strings.TrimSpace(name)
		}
		p.UpdatedAt = time.Now().UTC()
		_, err = tx.ExecContext(ctx, "UPDATE projects SET name=?,workspace=?,updated_at=? WHERE id=?", p.Name, p.Workspace, p.UpdatedAt.Format(time.RFC3339Nano), p.ID)
		return err
	})
	return p, wrap("update project", err)
}

// ChannelProject reads the durable explicit selection for a verified conversation.
// A missing row is distinct from an explicit empty selection of the default.
func (s *Store) ChannelProject(ctx context.Context, binding string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT project_id FROM channel_projects WHERE binding=?", binding).Scan(&id)
	return id, wrap("read channel project", err)
}

func (s *Store) SetChannelProject(ctx context.Context, binding, projectID string) error {
	ids := []string{}
	if projectID != "" {
		ids = append(ids, projectID)
	}
	return s.SetChannelSelection(ctx, binding, ChannelSelection{Mode: "manual", ProjectIDs: ids, PrimaryID: projectID})
}

// UpdateProjectMetadata changes routing descriptions without moving a checkout
// or altering its stable project and memory identities.
func (s *Store) UpdateProjectMetadata(ctx context.Context, id, name, description string, aliases []string) (Project, error) {
	var project Project
	if len(name) > 256 || len(description) > 4096 || len(aliases) > 32 {
		return project, errors.New("project metadata exceeds its limits")
	}
	seen := map[string]bool{}
	cleaned := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || len(alias) > 256 || seen[alias] {
			return project, errors.New("project aliases must be nonempty, unique and at most 256 bytes")
		}
		seen[alias] = true
		cleaned = append(cleaned, alias)
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return project, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		project, err = readProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(name) != "" {
			project.Name = strings.TrimSpace(name)
		}
		project.Description, project.Aliases, project.UpdatedAt = description, append([]string(nil), cleaned...), time.Now().UTC()
		_, err = tx.ExecContext(ctx, "UPDATE projects SET name=?,description=?,aliases=?,updated_at=? WHERE id=?", project.Name, description, encoded, project.UpdatedAt.Format(time.RFC3339Nano), id)
		return err
	})
	return project, wrap("update project metadata", err)
}
