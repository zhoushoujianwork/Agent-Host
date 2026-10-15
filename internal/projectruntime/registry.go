package projectruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"agent-host/internal/state"
	_ "modernc.org/sqlite"
)

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type projectRecord struct {
	Version  int           `json:"version"`
	Project  state.Project `json:"project"`
	Identity string        `json:"git_common_dir"`
	Origins  []Origin      `json:"origins"`
}

// RunReceipt contains operational metadata and an explicit source reference.
// Original messages, responses, model context and credentials stay entry-local.
type RunReceipt struct {
	Version          int       `json:"version"`
	Kind             string    `json:"kind"`
	RunID            string    `json:"run_id"`
	StateHome        string    `json:"state_home"`
	ProjectID        string    `json:"project_id"`
	ProjectIDs       []string  `json:"project_ids"`
	AgentID          string    `json:"agent_id,omitempty"`
	AudienceID       string    `json:"audience_id"`
	Entry            string    `json:"entry"`
	MessageID        string    `json:"message_id,omitempty"`
	SessionID        string    `json:"session_id"`
	Workspace        string    `json:"workspace"`
	ExecutionStatus  string    `json:"execution_status"`
	AcceptanceStatus string    `json:"acceptance_status"`
	MemoryStatus     string    `json:"memory_status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (c *Catalog) directory() string { return filepath.Join(c.Root, "projects") }

func (c *Catalog) open(ctx context.Context) (*sql.DB, error) {
	if strings.TrimSpace(c.Root) == "" {
		return nil, errors.New("project runtime root is required")
	}
	if err := privateDirectory(c.directory()); err != nil {
		return nil, err
	}
	path := filepath.Join(c.directory(), "state.db")
	if err := regularRuntimeFile(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = errors.Join(f.Chmod(0600), f.Close())
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS projects (
id TEXT PRIMARY KEY, identity TEXT NOT NULL UNIQUE, scope TEXT NOT NULL UNIQUE, payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS run_receipts (
state_home TEXT NOT NULL, run_id TEXT NOT NULL, project_id TEXT NOT NULL,
payload BLOB NOT NULL, PRIMARY KEY(state_home,run_id,project_id),
FOREIGN KEY(project_id) REFERENCES projects(id));`)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// Projects returns enrolled projects only. Candidate enumeration cannot create
// this database or make an inventory entry look like an enrolled project.
func (c *Catalog) Projects(ctx context.Context) ([]state.Project, error) {
	if strings.TrimSpace(c.Root) == "" {
		return nil, errors.New("project runtime root is required")
	}
	if _, err := os.Stat(filepath.Join(c.directory(), "state.db")); errors.Is(err, os.ErrNotExist) {
		return []state.Project{}, nil
	} else if err != nil {
		return nil, err
	}
	db, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT payload FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []state.Project{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var record projectRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		projects = append(projects, record.Project)
	}
	return projects, rows.Err()
}

// Enroll records one resolved target. Existing identity and scope are stable;
// a different checkout cannot silently reuse either identifier.
func (c *Catalog) Enroll(ctx context.Context, p state.Project, origin Origin) (state.Project, error) {
	if !safeID.MatchString(p.ID) || p.ID == "default" || p.Scope == "" {
		return state.Project{}, errors.New("project identity and scope are required and must be safe")
	}
	co, err := inspectCheckout(ctx, p.Workspace)
	if err != nil {
		return state.Project{}, err
	}
	release, err := c.lock(ctx)
	if err != nil {
		return state.Project{}, err
	}
	defer release()
	db, err := c.open(ctx)
	if err != nil {
		return state.Project{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return state.Project{}, err
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT payload FROM projects WHERE id=? OR identity=? OR scope=?", p.ID, co.identity, p.Scope).Scan(&existing)
	record := projectRecord{Version: 1, Project: p, Identity: co.identity, Origins: []Origin{}}
	if err == nil {
		if err := json.Unmarshal(existing, &record); err != nil {
			return state.Project{}, err
		}
		if record.Project.ID != p.ID || record.Identity != co.identity || record.Project.Scope != p.Scope {
			return state.Project{}, fmt.Errorf("project identity collision with %q", record.Project.ID)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return state.Project{}, err
	}
	now := time.Now().UTC()
	if record.Project.CreatedAt.IsZero() {
		record.Project.CreatedAt = now
	}
	record.Project.UpdatedAt = now
	if record.Project.Name == "" {
		record.Project.Name = filepath.Base(co.workspace)
	}
	// Retain the original checkout when the same repository is selected through
	// a worktree; its execution directory lives on the run receipt instead.
	if len(existing) == 0 {
		record.Project.Workspace = co.workspace
	}
	if origin.Entry != "" {
		found := false
		for _, o := range record.Origins {
			if o == origin {
				found = true
				break
			}
		}
		if !found {
			record.Origins = append(record.Origins, origin)
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return state.Project{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO projects(id,identity,scope,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", record.Project.ID, co.identity, record.Project.Scope, data)
	if err != nil {
		return state.Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return state.Project{}, err
	}
	if err := c.exportProject(record); err != nil {
		return record.Project, fmt.Errorf("project enrolled; exporting generated view: %w", err)
	}
	return record.Project, nil
}

// RecordRun updates shared execution receipts, preserving the separate business
// acceptance and memory statuses. All referenced projects must be enrolled.
func (c *Catalog) RecordRun(ctx context.Context, home string, run state.Run) error {
	if !safeID.MatchString(run.ID) {
		return errors.New("run ID must be safe")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	if strings.TrimSpace(home) == "" {
		return errors.New("run source state home is required")
	}
	if canonical, err := filepath.EvalSymlinks(absHome); err == nil {
		absHome = canonical
	}
	ids := []string{}
	addID := func(id string) {
		if id == "" {
			return
		}
		for _, v := range ids {
			if v == id {
				return
			}
		}
		ids = append(ids, id)
	}
	addID(run.ProjectID)
	for _, p := range run.Projects {
		addID(p.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if !safeID.MatchString(id) {
			return errors.New("project ID must be safe")
		}
	}
	receipt := RunReceipt{Version: 1, Kind: "execution_receipt", RunID: run.ID, StateHome: absHome, ProjectIDs: ids, AgentID: run.AgentID, AudienceID: run.AudienceID, Entry: "cli", SessionID: run.SessionID, Workspace: run.WorkingDirectory(), ExecutionStatus: run.ExecutionStatus, AcceptanceStatus: run.AcceptanceStatus, MemoryStatus: run.MemoryStatus, CreatedAt: run.CreatedAt, UpdatedAt: time.Now().UTC()}
	if run.Source != nil {
		receipt.Entry = run.Source.Channel
		receipt.MessageID = run.Source.MessageID
		if receipt.AgentID == "" {
			receipt.AgentID = run.Source.AgentID
		}
		if receipt.AudienceID == "" {
			receipt.AudienceID = run.Source.AudienceID
		}
	}
	if receipt.AudienceID == "" {
		receipt.AudienceID = "owner-local"
	}
	release, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)", id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("cannot record run for unenrolled project %q", id)
		}
		// The view path is keyed by native run ID. Refuse a conflicting entry
		// rather than replace another source with an invented shared identity.
		var conflicting bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_receipts WHERE run_id=? AND project_id=? AND state_home<>?)", run.ID, id, absHome).Scan(&conflicting); err != nil {
			return err
		}
		if conflicting {
			return fmt.Errorf("run ID %q belongs to a different source state home", run.ID)
		}
		receipt.ProjectID = id
		data, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_receipts(state_home,run_id,project_id,payload) VALUES(?,?,?,?) ON CONFLICT(state_home,run_id,project_id) DO UPDATE SET payload=excluded.payload", absHome, run.ID, id, data); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, id := range ids {
		receipt.ProjectID = id
		if err := c.exportRun(receipt); err != nil {
			return fmt.Errorf("run recorded; exporting generated view: %w", err)
		}
	}
	return nil
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime path %q must be a real directory", path)
	}
	return os.Chmod(path, 0700)
}

func regularRuntimeFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("runtime file %q must be a regular file", path)
	}
	return nil
}

func atomicFile(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".view-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(path, append(data, '\n'))
}

func (c *Catalog) exportProject(record projectRecord) error {
	dir := filepath.Join(c.directory(), record.Project.ID)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "project.json"), record); err != nil {
		return err
	}
	p := record.Project
	text := fmt.Sprintf("# %s\n\nLocal project: `%s`\n\nCheckout: `%s`\n\nMemory scope: `%s`\n\nThis generated view records Runtime enrollment. Execution receipts are under `tasks/`; execution completion does not establish feature acceptance or PR delivery. Original request and response evidence stays in its source entry state.\n", markdownLine(p.Name), p.ID, markdownLine(p.Workspace), markdownLine(p.Scope))
	return atomicFile(filepath.Join(dir, "PROJECT.md"), []byte(text))
}

func (c *Catalog) exportRun(receipt RunReceipt) error {
	projectDir := filepath.Join(c.directory(), receipt.ProjectID)
	for _, dir := range []string{projectDir, filepath.Join(projectDir, "tasks"), filepath.Join(projectDir, "tasks", receipt.RunID)} {
		if err := privateDirectory(dir); err != nil {
			return err
		}
	}
	dir := filepath.Join(projectDir, "tasks", receipt.RunID)
	if err := writeJSON(filepath.Join(dir, "task.json"), receipt); err != nil {
		return err
	}
	text := fmt.Sprintf("# Execution %s\n\nProject: `%s`\n\nExecution: `%s`\n\nAcceptance: `%s`\n\nMemory: `%s`\n\nSource entry: `%s`\n\nSource state: `%s/state.db`, run `%s`\n\nSource audience: `%s`\n\nSource Agent: `%s`\n\nSource message: `%s`\n\nThis owner-local operational view does not grant another audience access to the original message, response or native conversation. Execution status is separate from business feature acceptance and PR delivery.\n", receipt.RunID, receipt.ProjectID, markdownLine(receipt.ExecutionStatus), markdownLine(receipt.AcceptanceStatus), markdownLine(receipt.MemoryStatus), markdownLine(receipt.Entry), markdownLine(receipt.StateHome), receipt.RunID, markdownLine(receipt.AudienceID), markdownLine(receipt.AgentID), markdownLine(receipt.MessageID))
	return atomicFile(filepath.Join(dir, "TASK.md"), []byte(text))
}

func markdownLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "`", "'").Replace(s)
}
