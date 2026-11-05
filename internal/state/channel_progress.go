package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const schemaChannelProgressV18 = `
CREATE TABLE IF NOT EXISTS channel_progress_runs(run_id TEXT PRIMARY KEY REFERENCES runs(id), enabled INTEGER NOT NULL CHECK(enabled IN (0,1)));
CREATE TABLE IF NOT EXISTS channel_progress(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES channel_progress_runs(run_id), message_id TEXT NOT NULL REFERENCES channel_messages(id), profile TEXT NOT NULL, fingerprint TEXT NOT NULL, idempotency_key TEXT, text TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','accepted','failed','unknown')), error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, dispatch_started_at TEXT NOT NULL DEFAULT '', UNIQUE(run_id,idempotency_key));
CREATE INDEX IF NOT EXISTS channel_progress_queue ON channel_progress(profile,status,id);
PRAGMA user_version=18;
`

var ErrProgressUnavailable = errors.New("channel progress is unavailable for this run")

type ChannelProgressBinding struct {
	MessageID       string
	RunID           string
	Fingerprint     string
	ConversationKey string
}

type ChannelProgress struct {
	ID                int64  `json:"attempt_id"`
	RunID             string `json:"-"`
	MessageID         string `json:"-"`
	Fingerprint       string `json:"-"`
	Text              string `json:"text"`
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
	CreatedAt         string `json:"-"`
	DispatchStartedAt string `json:"-"`
}

// OpenForChannelProgress opens only an existing current operational database.
// It never migrates, recovers, initializes memory, or acquires the Host lock.
func OpenForChannelProgress(home string) (*Store, error) {
	path, err := filepath.Abs(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, err
	}
	physicalHome, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(physicalHome, "state.db")
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	for _, p := range []string{"busy_timeout(5000)", "foreign_keys(1)", "synchronous(FULL)"} {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var version int
	err = db.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil || version != SchemaVersion {
		_ = db.Close()
		return nil, errors.New("channel progress requires current existing state")
	}
	return &Store{db: db, home: filepath.Dir(path)}, nil
}

func progressDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EnableChannelProgress is called only by Host for an admitted business run.
func (s *Store) EnableChannelProgress(ctx context.Context, runID string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		r, err := readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if r.Source == nil || !s.matchesPartition(r.AgentID, r.AudienceID) {
			return ErrProgressUnavailable
		}
		if _, err = progressCurrent(ctx, tx, r.Source.ChannelMessageID, runID, false); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO channel_progress_runs(run_id,enabled) VALUES(?,1) ON CONFLICT(run_id) DO NOTHING", runID)
		return err
	})
}
func (s *Store) RevokeChannelProgress(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE channel_progress_runs SET enabled=0 WHERE run_id=?", runID)
	return err
}

func progressCurrent(ctx context.Context, tx *sql.Tx, messageID, runID string, enabled bool) (ChannelProgressBinding, error) {
	b := ChannelProgressBinding{MessageID: messageID, RunID: runID}
	m, err := readChannelMessage(ctx, tx, messageID)
	if err != nil || m.Status != "claimed" || m.RunID != runID || !historyIdentityValid(m) {
		return b, ErrProgressUnavailable
	}
	var receiptStatus, receiptProfile, receiptChannel, receiptAccount, receiptProviderID string
	if tx.QueryRowContext(ctx, "SELECT status,profile,channel,account_id,message_id FROM channel_messages WHERE id=?", messageID).Scan(&receiptStatus, &receiptProfile, &receiptChannel, &receiptAccount, &receiptProviderID) != nil || m.ID != messageID || receiptStatus != m.Status || receiptProfile != m.Profile || receiptChannel != m.Channel || receiptAccount != m.AccountID || receiptProviderID != m.MessageID {
		return b, ErrProgressUnavailable
	}
	r, err := historyRun(ctx, tx, m)
	if err != nil || r == nil || r.ExecutionStatus != "running" || r.Preparation != nil || r.CompletedAt != nil {
		return b, ErrProgressUnavailable
	}
	var rowSession, rowScope string
	if tx.QueryRowContext(ctx, "SELECT session_id,scope FROM runs WHERE id=?", runID).Scan(&rowSession, &rowScope) != nil || rowSession != r.SessionID || rowScope != r.Scope {
		return b, ErrProgressUnavailable
	}
	session, err := readSession(ctx, tx, r.SessionID)
	if err != nil || session.ID != r.SessionID || session.Scope != r.Scope || session.NativeSessionID != r.NativeSessionID || !samePartition(session.AgentID, session.AudienceID, r.AgentID, r.AudienceID) || session.RuntimeFingerprint != r.RuntimeFingerprint || session.ProjectID != r.ProjectID || session.Workspace != r.Workspace || !sameProjectRefs(session.Projects, r.Projects) || !sameDirectories(session.AdditionalDirectories, r.AdditionalDirectories) || !sameDirectories(m.AdditionalDirectories, r.AdditionalDirectories) {
		return b, ErrProgressUnavailable
	}
	binding := m.Binding
	if m.RouteBinding != "" {
		binding = m.RouteBinding
	}
	if session.Room == nil {
		var current string
		if tx.QueryRowContext(ctx, "SELECT session_id FROM channel_sessions WHERE binding=?", binding).Scan(&current) != nil || current != r.SessionID {
			return b, ErrProgressUnavailable
		}
	} else {
		var current string
		if tx.QueryRowContext(ctx, "SELECT session_id FROM room_messages WHERE room_id=? AND request_id=?", session.Room.RoomID, m.MessageID).Scan(&current) != nil || current != r.SessionID || session.Room.RequestID != m.MessageID || session.Room.SenderID != m.SenderID {
			return b, ErrProgressUnavailable
		}
	}
	if enabled {
		var active bool
		if tx.QueryRowContext(ctx, "SELECT enabled FROM channel_progress_runs WHERE run_id=?", runID).Scan(&active) != nil || !active {
			return b, ErrProgressUnavailable
		}
	}
	a, err := historyCurrent(ctx, tx, messageID)
	if err != nil {
		return b, ErrProgressUnavailable
	}
	b.ConversationKey = progressDigest([]any{"channel-progress-v1", a.historyScope, binding, m.Profile, r.RuntimeFingerprint})
	b.Fingerprint = progressDigest([]any{b.ConversationKey, messageID, runID, r.SessionID, r.Scope, r.Projects, r.AdditionalDirectories, m.ReplyTarget, m.ReplyExpiresAt})
	return b, nil
}
func (s *Store) ChannelProgressBinding(ctx context.Context, messageID, runID string) (ChannelProgressBinding, error) {
	var b ChannelProgressBinding
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		b, err = progressCurrent(ctx, tx, messageID, runID, true)
		return err
	})
	return b, err
}
func ValidateProgressText(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("progress text must be valid UTF-8")
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 200 {
		return "", errors.New("progress text must contain 1 to 200 Unicode code points")
	}
	return text, nil
}

const progressColumns = "id,run_id,message_id,fingerprint,text,status,error,created_at,dispatch_started_at"

func scanProgress(row reactionScanner) (ChannelProgress, error) {
	var p ChannelProgress
	err := row.Scan(&p.ID, &p.RunID, &p.MessageID, &p.Fingerprint, &p.Text, &p.Status, &p.Error, &p.CreatedAt, &p.DispatchStartedAt)
	return p, err
}
func (s *Store) ChannelProgress(ctx context.Context, id int64) (ChannelProgress, error) {
	return scanProgress(s.db.QueryRowContext(ctx, "SELECT "+progressColumns+" FROM channel_progress WHERE id=?", id))
}
func (s *Store) ReserveChannelProgress(ctx context.Context, b ChannelProgressBinding, text, key string) (ChannelProgress, error) {
	var p ChannelProgress
	var err error
	text, err = ValidateProgressText(text)
	if err != nil {
		return p, err
	}
	if !utf8.ValidString(key) || len(key) > 256 {
		return p, errors.New("invalid progress idempotency key")
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		current, err := progressCurrent(ctx, tx, b.MessageID, b.RunID, true)
		if err != nil || current != b {
			return ErrProgressUnavailable
		}
		if key != "" {
			p, err = scanProgress(tx.QueryRowContext(ctx, "SELECT "+progressColumns+" FROM channel_progress WHERE run_id=? AND idempotency_key=?", b.RunID, key))
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var count int
		var last sql.NullString
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*),MAX(created_at) FROM channel_progress WHERE run_id=?", b.RunID).Scan(&count, &last); err != nil {
			return err
		}
		if count >= 5 {
			return errors.New("channel progress reservation limit reached")
		}
		now := time.Now().UTC()
		if last.Valid {
			previous, e := time.Parse(reactionTimeFormat, last.String)
			if e != nil {
				return e
			}
			if now.Sub(previous) < 15*time.Second {
				return errors.New("channel progress requires 15 seconds between reservations")
			}
		}
		var optional any
		if key != "" {
			optional = key
		}
		m, err := readChannelMessage(ctx, tx, b.MessageID)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO channel_progress(run_id,message_id,profile,fingerprint,idempotency_key,text,status,created_at) VALUES(?,?,?,?,?,?,'pending',?)", b.RunID, b.MessageID, m.Profile, b.Fingerprint, optional, text, now.Format(reactionTimeFormat))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		p, err = scanProgress(tx.QueryRowContext(ctx, "SELECT "+progressColumns+" FROM channel_progress WHERE id=?", id))
		return err
	})
	return p, err
}

// ClaimNextChannelProgress atomically revalidates and records dispatch authority.
func (s *Store) ClaimNextChannelProgress(ctx context.Context, profile string) (ChannelProgress, ChannelMessage, error) {
	var p ChannelProgress
	var m ChannelMessage
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = scanProgress(tx.QueryRowContext(ctx, "SELECT "+progressColumns+" FROM channel_progress WHERE profile=? AND status='pending' AND dispatch_started_at='' ORDER BY id LIMIT 1", profile))
		if err != nil {
			return err
		}
		b, validErr := progressCurrent(ctx, tx, p.MessageID, p.RunID, true)
		if validErr != nil || b.Fingerprint != p.Fingerprint {
			_, err = tx.ExecContext(ctx, "UPDATE channel_progress SET status='failed',error='channel progress authorization expired' WHERE id=?", p.ID)
			p.Status = "failed"
			return err
		}
		m, err = readChannelMessage(ctx, tx, p.MessageID)
		if err != nil {
			return err
		}
		if m.ReplyTarget == nil && !m.ReplyExpiresAt.After(time.Now()) {
			_, err = tx.ExecContext(ctx, "UPDATE channel_progress SET status='failed',error='channel progress transport expired' WHERE id=?", p.ID)
			p.Status = "failed"
			return err
		}
		p.DispatchStartedAt = time.Now().UTC().Format(reactionTimeFormat)
		_, err = tx.ExecContext(ctx, "UPDATE channel_progress SET dispatch_started_at=? WHERE id=?", p.DispatchStartedAt, p.ID)
		return err
	})
	return p, m, err
}
func (s *Store) FinishChannelProgress(ctx context.Context, id int64, status string) error {
	if status != "accepted" && status != "failed" && status != "unknown" {
		return errors.New("invalid progress delivery outcome")
	}
	cause := ""
	if status != "accepted" {
		cause = "channel progress delivery " + status
	}
	result, err := s.db.ExecContext(ctx, "UPDATE channel_progress SET status=?,error=? WHERE id=? AND status='pending' AND dispatch_started_at<>''", status, cause, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("progress attempt is not dispatched pending")
	}
	return err
}
func recoverChannelProgress(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE channel_progress SET status='unknown',error='host stopped after dispatch authorization; progress was not replayed' WHERE status='pending' AND dispatch_started_at<>''")
	if err != nil {
		return err
	}
	// All active message/run claims are recovered before this operation.
	_, err = tx.ExecContext(ctx, "UPDATE channel_progress SET status='failed',error='channel progress authorization expired during recovery' WHERE status='pending' AND dispatch_started_at=''")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE channel_progress_runs SET enabled=0")
	return err
}
