package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-host/internal/event"
	_ "modernc.org/sqlite"
)

// Store holds a single SQLite connection. The owning host must hold Acquire's
// process lock before opening a writable Store. Read-only inspection is safe
// while the host is running.
type Store struct {
	db                    *sql.DB
	legacyProjectMetadata bool
	agentID               string
	audienceID            string
	borrowed              bool
	home                  string
	readOnly              bool
	files                 *fileMemory
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), scope TEXT NOT NULL, created_at TEXT NOT NULL, execution_status TEXT NOT NULL, memory_status TEXT NOT NULL, payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS runs_session ON runs(session_id, created_at);
CREATE INDEX IF NOT EXISTS runs_pending ON runs(memory_status);
CREATE TABLE IF NOT EXISTS events(run_id TEXT NOT NULL REFERENCES runs(id), seq INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(run_id,seq));
CREATE TABLE IF NOT EXISTS memories(id TEXT PRIMARY KEY, scope TEXT NOT NULL, text TEXT NOT NULL, status TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(scope,text));
CREATE INDEX IF NOT EXISTS memories_scope ON memories(scope,status);
CREATE TABLE IF NOT EXISTS memory_revisions(memory_id TEXT NOT NULL REFERENCES memories(id), revision INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(memory_id,revision));
CREATE TABLE IF NOT EXISTS memory_evidence(memory_id TEXT NOT NULL REFERENCES memories(id), run_id TEXT NOT NULL REFERENCES runs(id), PRIMARY KEY(memory_id,run_id));
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(memory_id UNINDEXED, tokens, tokenize='unicode61');
PRAGMA user_version=1;
`

const schemaV2 = `
ALTER TABLE memories ADD COLUMN expires_at TEXT;
CREATE TABLE candidates(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), scope TEXT NOT NULL, status TEXT NOT NULL, payload BLOB NOT NULL);
CREATE INDEX candidates_scope ON candidates(scope,run_id,status);
PRAGMA user_version=2;
`

const schemaV3 = `
CREATE TABLE memory_annotations(memory_id TEXT PRIMARY KEY REFERENCES memories(id), version TEXT NOT NULL, payload BLOB NOT NULL);
CREATE TABLE memory_terms(memory_id TEXT NOT NULL REFERENCES memories(id), term TEXT NOT NULL, PRIMARY KEY(memory_id,term));
CREATE INDEX memory_terms_term ON memory_terms(term,memory_id);
PRAGMA user_version=3;
`

const schemaV4 = `
CREATE TABLE channel_messages(id TEXT PRIMARY KEY, channel TEXT NOT NULL, account_id TEXT NOT NULL, message_id TEXT NOT NULL, profile TEXT NOT NULL, status TEXT NOT NULL, received_at TEXT NOT NULL, reply_url TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(channel,account_id,message_id));
CREATE INDEX channel_messages_queue ON channel_messages(profile,status,received_at);
CREATE TABLE channel_sessions(binding TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id));
PRAGMA user_version=4;
`

const schemaV5 = `
CREATE TABLE channel_reactions(id INTEGER PRIMARY KEY AUTOINCREMENT, channel_message_id TEXT NOT NULL REFERENCES channel_messages(id), profile TEXT NOT NULL, stage TEXT NOT NULL CHECK(stage IN ('received','processing','completed','failed')), label TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('ready','sending','accepted','failed','unknown','skipped')), error TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1)), removal_status TEXT NOT NULL DEFAULT 'pending' CHECK(removal_status IN ('pending','sending','accepted','failed','unknown')), removal_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(channel_message_id,stage));
CREATE INDEX channel_reactions_queue ON channel_reactions(profile,status,id);
CREATE UNIQUE INDEX channel_reactions_terminal ON channel_reactions(channel_message_id) WHERE stage IN ('completed','failed');
PRAGMA user_version=5;
`

const schemaReactionsV6 = `
CREATE TABLE channel_reactions_v6(id INTEGER PRIMARY KEY AUTOINCREMENT, channel_message_id TEXT NOT NULL REFERENCES channel_messages(id), profile TEXT NOT NULL, stage TEXT NOT NULL CHECK(stage IN ('received','processing','recall','execute','remember','persist','evidence','extract','assess','candidates','replying','completed','failed')), label TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('ready','sending','accepted','failed','unknown','skipped')), error TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1)), removal_status TEXT NOT NULL DEFAULT 'pending' CHECK(removal_status IN ('pending','sending','accepted','failed','unknown')), removal_error TEXT NOT NULL DEFAULT '', source_run_id TEXT NOT NULL DEFAULT '', source_event_seq INTEGER NOT NULL DEFAULT 0 CHECK(source_event_seq>=0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(channel_message_id,stage), CHECK((source_run_id='' AND source_event_seq=0) OR (source_run_id<>'' AND source_event_seq>0)));
INSERT INTO channel_reactions_v6(id,channel_message_id,profile,stage,label,status,error,active,removal_status,removal_error,created_at,updated_at) SELECT id,channel_message_id,profile,stage,label,status,error,active,removal_status,removal_error,created_at,updated_at FROM channel_reactions;
UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='channel_reactions'),0)) WHERE name='channel_reactions_v6';
DROP TABLE channel_reactions;
ALTER TABLE channel_reactions_v6 RENAME TO channel_reactions;
CREATE INDEX channel_reactions_queue ON channel_reactions(profile,status,id);
CREATE UNIQUE INDEX channel_reactions_terminal ON channel_reactions(channel_message_id) WHERE stage IN ('completed','failed');
PRAGMA user_version=6;
`

const schemaProjectsV7 = `
CREATE TABLE projects(id TEXT PRIMARY KEY, name TEXT NOT NULL, workspace TEXT NOT NULL UNIQUE, scope TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE channel_projects(binding TEXT PRIMARY KEY, project_id TEXT NOT NULL);
PRAGMA user_version=7;
`

func Open(home string, readOnly bool) (*Store, error) {
	return open(home, readOnly, false)
}

// OpenForProjectInspection opens project registration data without migrating
// state. Schemas 7/8 retain the older project columns; schema 9 already has the
// current registry. Schema 10 changes memory ownership only, so those registry
// shapes remain safe during installation preflight, including schema 11 before
// the additive discovery migration. Other inspection stays
// strict and this compatibility exception never permits writes.
func OpenForProjectInspection(home string) (*Store, error) {
	return open(home, true, true)
}

func open(home string, readOnly, projectInspection bool) (*Store, error) {
	if home == "" {
		return nil, errors.New("state home is required")
	}
	if !readOnly {
		if err := os.MkdirAll(home, 0700); err != nil {
			return nil, fmt.Errorf("create state home: %w", err)
		}
	}
	path, err := filepath.Abs(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, err
	}
	if !readOnly {
		// Restrict the main file before SQLite creates WAL/SHM siblings, which
		// inherit the database mode on supported local Unix filesystems.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("prepare database file: %w", err)
		}
		if err = errors.Join(file.Chmod(0600), file.Close()); err != nil {
			return nil, err
		}
	}
	// Resolve platform aliases such as macOS /var before recording a durable
	// state-home locator. Runtime authorization remains with the host config.
	physicalHome, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(physicalHome, "state.db")
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rwc")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*Store, error) { closeErr := db.Close(); return nil, errors.Join(err, closeErr) }
	if err = db.Ping(); err != nil {
		return fail(fmt.Errorf("connect state: %w", err))
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > SchemaVersion {
		return fail(fmt.Errorf("unsupported state schema %d", version))
	}
	if readOnly {
		compatibleProjects := projectInspection && (version >= 7 && version < SchemaVersion)
		if version != SchemaVersion && !compatibleProjects {
			return fail(fmt.Errorf("state schema %d requires migration; open the host for writing first", version))
		}
	} else {
		var mode string
		if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
			return fail(err)
		}
		if mode != "wal" {
			return fail(fmt.Errorf("SQLite did not enable WAL: %s", mode))
		}
		if _, err = db.Exec("PRAGMA synchronous=FULL"); err != nil {
			return fail(err)
		}
		store := &Store{db: db}
		// Rebuilding memories changes its unique key, not evidence IDs. Foreign
		// keys must be disabled outside the transaction for SQLite table rebuilds.
		if version < 10 {
			if _, err = db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
				return fail(err)
			}
		}
		if err = store.transaction(context.Background(), func(tx *sql.Tx) error {
			if version == 0 {
				if _, err := tx.Exec(schemaV1); err != nil {
					return err
				}
			}
			if version < 2 {
				if _, err := tx.Exec(schemaV2); err != nil {
					return err
				}
			}
			if version < 3 {
				if _, err := tx.Exec(schemaV3); err != nil {
					return err
				}
				if err := backfillAnnotations(context.Background(), tx); err != nil {
					return err
				}
			}
			if version < 4 {
				if _, err := tx.Exec(schemaV4); err != nil {
					return err
				}
			}
			if version < 5 {
				if _, err := tx.Exec(schemaV5); err != nil {
					return err
				}
			}
			if version < 6 {
				if _, err := tx.Exec(schemaReactionsV6); err != nil {
					return err
				}
			}
			if version < 7 {
				if _, err := tx.Exec(schemaProjectsV7); err != nil {
					return err
				}
			}
			if version < 8 {
				if _, err := tx.Exec(schemaRuntimePhasesV8); err != nil {
					return err
				}
			}
			if version < 9 {
				if _, err := tx.Exec(schemaRoutingV9); err != nil {
					return err
				}
			}
			if version < 10 {
				if _, err := tx.Exec(schemaPartitionsV10); err != nil {
					return err
				}
				rows, err := tx.Query("PRAGMA foreign_key_check")
				if err != nil {
					return err
				}
				broken := rows.Next()
				err = errors.Join(rows.Err(), rows.Close())
				if err != nil {
					return err
				}
				if broken {
					return errors.New("partition migration has invalid evidence references")
				}
			}
			if version < 11 {
				if _, err := tx.Exec(schemaChannelInputV11); err != nil {
					return err
				}
			}
			if version < 12 {
				if _, err := tx.Exec(SchemaDiscoveryV12); err != nil {
					return err
				}
			}
			if version < 13 {
				if _, err := tx.Exec(schemaAssistanceV13); err != nil {
					return err
				}
			}
			if version < 14 {
				if _, err := tx.Exec(schemaChannelReactionV14); err != nil {
					return err
				}
			}
			return migrateCurrentAnnotations(context.Background(), tx)
		}); err != nil {
			_, restoreErr := db.Exec("PRAGMA foreign_keys=ON")
			return fail(errors.Join(fmt.Errorf("migrate state schema including FTS5: %w", err), restoreErr))
		}
		if _, err = db.Exec("PRAGMA foreign_keys=ON"); err != nil {
			return fail(err)
		}
	}
	store := &Store{db: db, home: filepath.Dir(path), readOnly: readOnly, legacyProjectMetadata: readOnly && version < 9}
	if !projectInspection {
		if err := store.openFileMemory(context.Background()); err != nil {
			return fail(err)
		}
	}
	return store, nil
}

func (s *Store) Close() error {
	if s.borrowed {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	if err := s.assignPartition(&session.AgentID, &session.AudienceID); err != nil {
		return err
	}
	if session.ID == "" || session.Scope == "" || session.NativeSessionID == "" {
		return errors.New("session ID, scope and native session ID are required")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO sessions(id,payload) VALUES(?,?)", session.ID, payload)
	return wrap("create session", err)
}

func (s *Store) Session(ctx context.Context, id string) (Session, error) {
	return s.readSession(ctx, s.db, id)
}

func readSession(ctx context.Context, q rowQuerier, id string) (Session, error) {
	var session Session
	var payload []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM sessions WHERE id=?", id).Scan(&payload)
	if err == nil {
		err = json.Unmarshal(payload, &session)
	}
	return session, wrap("read session", err)
}

func (s *Store) CreateRun(ctx context.Context, run Run) error {
	if err := s.assignPartition(&run.AgentID, &run.AudienceID); err != nil {
		return err
	}
	if run.Source != nil || run.MemoryAttribution != "" {
		return errors.New("channel provenance requires CreateChannelRun")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error { return createRun(ctx, tx, run) })
}

func createRun(ctx context.Context, tx *sql.Tx, run Run) error {
	if err := validateMemoryAttribution(ctx, tx, run); err != nil {
		return err
	}
	if run.ID == "" || run.SessionID == "" || run.Scope == "" || strings.TrimSpace(run.Input) == "" {
		return errors.New("run ID, session, scope and input are required")
	}
	session, err := readSession(ctx, tx, run.SessionID)
	if err != nil {
		return err
	}
	if !samePartition(session.AgentID, session.AudienceID, run.AgentID, run.AudienceID) || session.Scope != run.Scope || session.NativeSessionID != run.NativeSessionID {
		return errors.New("run does not match its session scope or native session")
	}
	if session.ProjectID != run.ProjectID || session.ProjectName != run.ProjectName || session.Workspace != run.Workspace || session.RuntimeFingerprint != run.RuntimeFingerprint || !sameProjectRefs(session.Projects, run.Projects) || !sameDirectories(session.AdditionalDirectories, run.AdditionalDirectories) {
		return errors.New("run does not match its session project, workspace or runtime configuration")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	run.ExecutionStatus = "running"
	run.MemoryStatus = "pending_execution"
	run.AcceptanceStatus = "unverified"
	run.Events = nil
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,session_id,scope,created_at,execution_status,memory_status,payload) VALUES(?,?,?,?,?,?,?)", run.ID, run.SessionID, run.Scope, run.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), run.ExecutionStatus, run.MemoryStatus, payload)
	return wrap("create run", err)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRun(ctx context.Context, q rowQuerier, id string) (Run, error) {
	var run Run
	var payload []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM runs WHERE id=?", id).Scan(&payload)
	if err == nil {
		err = json.Unmarshal(payload, &run)
	}
	if err == nil {
		err = validateMemoryAttribution(ctx, q, run)
	}
	return run, wrap("read run", err)
}

func writeRun(ctx context.Context, tx *sql.Tx, run Run) error {
	if err := validateMemoryAttribution(ctx, tx, run); err != nil {
		return err
	}
	run.Events = nil
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE runs SET execution_status=?,memory_status=?,payload=? WHERE id=?", run.ExecutionStatus, run.MemoryStatus, payload, run.ID)
	return wrap("update run", err)
}

func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	run, err := s.readRun(ctx, s.db, id)
	if err != nil {
		return run, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM events WHERE run_id=? ORDER BY seq", id)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		var e event.Event
		if err = rows.Scan(&payload); err != nil {
			return run, err
		}
		if err = json.Unmarshal(payload, &e); err != nil {
			return run, err
		}
		run.Events = append(run.Events, e)
	}
	return run, rows.Err()
}

func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.files != nil {
		return s.fileOperationalTransaction(ctx, fn)
	}
	return s.sqlTransaction(ctx, fn)
}

// Operational evidence remains writable when learned files need repair.
// Only methods that consume or publish knowledge require the file coordinator.
func (s *Store) memoryTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.files != nil {
		return s.fileTransaction(ctx, fn)
	}
	return s.sqlTransaction(ctx, fn)
}

func (s *Store) sqlTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(err, ctx.Err())
	}
	if err = fn(tx); err != nil {
		rollbackErr := tx.Rollback()
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		return errors.Join(err, rollbackErr, ctx.Err())
	}
	if err := tx.Commit(); err != nil {
		// database/sql can roll back a canceled transaction before Commit runs.
		// Keep both its terminal error and the cancellation that caused it.
		return errors.Join(err, ctx.Err())
	}
	return nil
}

func (s *Store) SaveContext(ctx context.Context, id string, memories []Memory) error {
	return s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "running" {
			return errors.New("cannot change context after execution")
		}
		for i, m := range memories {
			if err := s.assignPartition(&m.AgentID, &m.AudienceID); err != nil {
				return err
			}
			memories[i] = m
			allowedScope := m.Scope == run.Scope
			if len(run.Projects) > 0 {
				allowedScope = false
				for _, project := range run.Projects {
					allowedScope = allowedScope || m.Scope == project.Scope
				}
			}
			if !allowedScope || !recallEligible(m, time.Now()) || m.ID == "" || m.Revision < 1 {
				return errors.New("context contains ineligible memory")
			}
			current, err := s.readMemory(ctx, tx, m.ID)
			if err != nil {
				return err
			}
			currentPayload, err := json.Marshal(current)
			if err != nil {
				return err
			}
			snapshotPayload, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if !recallEligible(current, time.Now()) || !bytes.Equal(currentPayload, snapshotPayload) {
				return errors.New("context snapshot does not match current stored memory")
			}
		}
		run.Context = memories
		return writeRun(ctx, tx, run)
	})
}

func (s *Store) AppendEvent(ctx context.Context, e *event.Event) error {
	if e == nil || e.RunID == "" || e.Type == "" {
		return errors.New("event run ID and type are required")
	}
	next := *e
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, e.RunID)
		if err != nil {
			return err
		}
		if next.SessionID != "" && next.SessionID != run.SessionID {
			return errors.New("event session mismatch")
		}
		next.SessionID = run.SessionID
		next.Version = event.Version
		if next.At.IsZero() {
			next.At = time.Now().UTC()
		}
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE run_id=?", next.RunID).Scan(&next.Seq); err != nil {
			return err
		}
		payload, err := json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO events(run_id,seq,payload) VALUES(?,?,?)", next.RunID, next.Seq, payload)
		if err != nil {
			return err
		}
		return projectChannelPhase(ctx, tx, run, next)
	})
	if err == nil {
		*e = next
	}
	return wrap("append event", err)
}

func (s *Store) CompleteRun(ctx context.Context, id, status, output, model, errorText string) error {
	if status != "completed" && status != "failed" && status != "canceled" && status != "unknown" {
		return fmt.Errorf("invalid execution status %q", status)
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "running" {
			return errors.New("run is already terminal; replay is forbidden")
		}
		run.ExecutionStatus = status
		run.Output = output
		run.Model = model
		run.Error = errorText
		run.MemoryStatus = "skipped"
		run.ExperienceStatus = "skipped"
		if status == "completed" && run.AutomaticMemorySkipReason() == "" {
			run.MemoryStatus = "pending"
			run.ExperienceStatus = "pending"
		} else if status == "completed" {
			audit := automaticMemorySkipAudit(run, run.AutomaticMemorySkipReason())
			run.Decision, run.ExperienceDecision = audit, append(json.RawMessage(nil), audit...)
		}
		return writeRun(ctx, tx, run)
	})
}

// SkipAutomaticMemory records why this completed run cannot write learned memory.
func (s *Store) SkipAutomaticMemory(ctx context.Context, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("skipped automatic memory requires a reason")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "completed" || run.AutomaticMemorySkipReason() != reason || run.MemoryStatus != "pending" && run.MemoryStatus != "skipped" || run.ExperienceStatus != "pending" && run.ExperienceStatus != "skipped" {
			return errors.New("run is not eligible for the specified automatic memory skip")
		}
		audit := automaticMemorySkipAudit(run, reason)
		run.MemoryStatus, run.ExperienceStatus = "skipped", "skipped"
		run.MemoryError, run.ExperienceError = "", ""
		run.Decision, run.ExperienceDecision = audit, append(json.RawMessage(nil), audit...)
		return writeRun(ctx, tx, run)
	})
}

func (s *Store) MemoryFailure(ctx context.Context, id, errorText string) error {
	if strings.TrimSpace(errorText) == "" {
		return errors.New("memory failure requires error details")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "completed" || (run.MemoryStatus != "pending" && run.MemoryStatus != "failed") {
			return errors.New("run is not eligible for memory failure")
		}
		run.MemoryStatus = "failed"
		run.MemoryError = errorText
		return writeRun(ctx, tx, run)
	})
}

func (s *Store) SessionHasRun(ctx context.Context, id string) (bool, error) {
	if _, err := s.readSession(ctx, s.db, id); err != nil {
		return false, err
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE session_id=?)", id).Scan(&exists)
	return exists, err
}

// SessionResumeError distinguishes a terminal conversation failure from an
// active run, corrupt state, or storage error. Only the former permits rotation.
type SessionResumeError struct {
	RunID, ExecutionStatus, MemoryStatus string
}

func (e *SessionResumeError) Error() string {
	if e.ExecutionStatus != "completed" {
		return fmt.Sprintf("session cannot resume after %s execution; start a fresh session", e.ExecutionStatus)
	}
	return fmt.Sprintf("session memory is %s; finish retry-memory before resuming", e.MemoryStatus)
}

func (e *SessionResumeError) CanStartFresh() bool {
	switch e.ExecutionStatus {
	case "failed", "canceled", "unknown":
		return true
	case "completed":
		return e.MemoryStatus == "failed" || e.MemoryStatus == "pending"
	default:
		return false
	}
}

func (s *Store) SessionCanResume(ctx context.Context, id string) error {
	if _, err := s.readSession(ctx, s.db, id); err != nil {
		return err
	}
	return sessionCanResume(ctx, s.db, id)
}

func sessionCanResume(ctx context.Context, q rowQuerier, id string) error {
	if _, err := readSession(ctx, q, id); err != nil {
		return err
	}
	var runID, status, memoryStatus string
	var payload []byte
	err := q.QueryRowContext(ctx, "SELECT id,execution_status,memory_status,payload FROM runs WHERE session_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1", id).Scan(&runID, &status, &memoryStatus, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var run Run
	if err := json.Unmarshal(payload, &run); err != nil {
		return wrap("read resumable run", err)
	}
	if run.ID != runID || run.SessionID != id || run.ExecutionStatus != status || run.MemoryStatus != memoryStatus {
		return errors.New("resumable run metadata does not match its stored evidence")
	}
	if err := validateMemoryAttribution(ctx, q, run); err != nil {
		return err
	}
	if status != "completed" {
		return &SessionResumeError{runID, status, memoryStatus}
	}
	if memoryStatus == "skipped" {
		if run.AutomaticMemorySkipReason() != "" && run.ExperienceStatus == "skipped" {
			return nil
		}
	}
	if memoryStatus != "saved" && memoryStatus != "unchanged" {
		return &SessionResumeError{runID, status, memoryStatus}
	}
	return nil
}

func (s *Store) Recover(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT payload FROM runs WHERE execution_status='running'")
		if err != nil {
			return err
		}
		var runs []Run
		for rows.Next() {
			var payload []byte
			var run Run
			if err = rows.Scan(&payload); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(payload, &run); err != nil {
				rows.Close()
				return err
			}
			runs = append(runs, run)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, run := range runs {
			run.ExecutionStatus = "unknown"
			run.MemoryStatus = "skipped"
			run.ExperienceStatus = "skipped"
			run.Error = "host stopped before recording a terminal native outcome; execution was not replayed"
			if err = writeRun(ctx, tx, run); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) PendingMemory(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM runs WHERE execution_status='completed' AND memory_status='pending' ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var payload []byte
		var run Run
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &run); err != nil {
			return nil, err
		}
		if s.matchesPartition(run.AgentID, run.AudienceID) {
			runs = append(runs, run)
		}
	}
	return runs, rows.Err()
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
