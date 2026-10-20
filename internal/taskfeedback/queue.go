package taskfeedback

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Item struct {
	ID                string    `json:"id"`
	RunID             string    `json:"run_id"`
	Home              string    `json:"home"`
	AgentID           string    `json:"agent_id"`
	AudienceID        string    `json:"audience_id"`
	Plan              Plan      `json:"plan"`
	Status            string    `json:"status"`
	Text              string    `json:"text,omitempty"`
	Receipt           string    `json:"receipt,omitempty"`
	Error             string    `json:"error,omitempty"`
	ExecutionStatus   string    `json:"execution_status,omitempty"`
	SourceObservation string    `json:"source_observation,omitempty"`
	Attempts          int       `json:"attempts"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Queue struct{ db *sql.DB }

func Open(directory string) (*Queue, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("feedback queue requires an absolute directory")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("feedback queue must be a private regular directory")
	}
	path := filepath.Join(directory, "state.db")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("feedback database must be a private regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	v := u.Query()
	v.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = v.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS feedback(id TEXT PRIMARY KEY,status TEXT NOT NULL,payload BLOB NOT NULL); CREATE INDEX IF NOT EXISTS feedback_status ON feedback(status); CREATE TABLE IF NOT EXISTS attempts(id INTEGER PRIMARY KEY AUTOINCREMENT,feedback_id TEXT NOT NULL,payload BLOB NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Queue{db}, nil
}

func (q *Queue) Close() error { return q.db.Close() }

func OpenForInspection(directory string) (*Queue, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("feedback inspection requires an absolute directory")
	}
	for _, path := range []string{directory, filepath.Join(directory, "state.db")} {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("feedback inspection requires private regular paths")
		}
		if path == directory && !info.IsDir() || path != directory && !info.Mode().IsRegular() {
			return nil, errors.New("feedback inspection path has an invalid file type")
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, "state.db")}
	v := u.Query()
	v.Set("mode", "ro")
	v.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = v.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Queue{db}, nil
}

// ResolveRecipient is an explicit local operator correction. The original Run
// plan remains evidence; this amendment never dispatches business work again.
func (q *Queue) ResolveRecipient(ctx context.Context, item Item, plan Plan) error {
	if item.Status != "needs_recipient" || plan.Mode != "notify" && plan.Mode != "origin" || plan.Target == nil {
		return errors.New("feedback recipient resolution requires an unresolved item and verified destination")
	}
	old := item.Plan
	previousTime := item.UpdatedAt.Format(time.RFC3339Nano)
	item.Plan = plan
	item.Status = "awaiting_result"
	item.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, "UPDATE feedback SET status=?,payload=? WHERE id=? AND status='needs_recipient' AND json_extract(CAST(payload AS TEXT),'$.updated_at')=?", item.Status, body, item.ID, previousTime)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("feedback recipient was already resolved")
	}
	audit, _ := json.Marshal(map[string]any{"kind": "recipient_resolved", "previous_plan": old, "plan": plan, "at": item.UpdatedAt})
	if _, err = tx.ExecContext(ctx, "INSERT INTO attempts(feedback_id,payload) VALUES(?,?)", item.ID, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *Queue) Attempts(ctx context.Context, id string) ([]json.RawMessage, error) {
	rows, err := q.db.QueryContext(ctx, "SELECT payload FROM attempts WHERE feedback_id=? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

func (q *Queue) Bind(ctx context.Context, item Item) error {
	item.Status = "awaiting_result"
	if item.Plan.Mode == "needs_recipient" {
		item.Status = "needs_recipient"
	}
	item.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, "INSERT INTO feedback(id,status,payload) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", item.ID, item.Status, body)
	return err
}

func (q *Queue) List(ctx context.Context) ([]Item, error) {
	return q.items(ctx, "")
}

func (q *Queue) Pending(ctx context.Context) ([]Item, error) {
	return q.items(ctx, "WHERE status IN ('awaiting_result','failed')")
}

func (q *Queue) items(ctx context.Context, where string) ([]Item, error) {
	rows, err := q.db.QueryContext(ctx, "SELECT payload FROM feedback "+where+" ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var raw []byte
		var item Item
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Claim atomically fixes the outbound body before a single external attempt.
func (q *Queue) Claim(ctx context.Context, item *Item, text string) (bool, error) {
	if item.Status != "awaiting_result" && item.Status != "failed" {
		return false, errors.New("only pending or confirmed-failed notifications may be claimed")
	}
	old := item.Status
	previousTime := item.UpdatedAt.Format(time.RFC3339Nano)
	item.Status = "sending"
	item.Text = text
	item.Attempts++
	item.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(item)
	if err != nil {
		return false, err
	}
	r, err := q.db.ExecContext(ctx, "UPDATE feedback SET status=?,payload=? WHERE id=? AND status=? AND json_extract(CAST(payload AS TEXT),'$.updated_at')=?", item.Status, body, item.ID, old, previousTime)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (q *Queue) Finish(ctx context.Context, item Item, status, receipt, reason string) error {
	if item.Status != "awaiting_result" && item.Status != "sending" && item.Status != "failed" {
		return errors.New("notification is already terminal or needs recipient resolution")
	}
	if status != "accepted" && status != "failed" && status != "unknown" && status != "needs_recipient" {
		return errors.New("invalid feedback outcome")
	}
	old := item.Status
	previousTime := item.UpdatedAt.Format(time.RFC3339Nano)
	item.Status, item.Receipt, item.Error = status, receipt, reason
	item.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, "UPDATE feedback SET status=?,payload=? WHERE id=? AND status=? AND json_extract(CAST(payload AS TEXT),'$.updated_at')=?", status, body, item.ID, old, previousTime)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("feedback state changed before receipt persistence")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO attempts(feedback_id,payload) VALUES(?,?)", item.ID, body); err != nil {
		return err
	}
	return tx.Commit()
}

// Recover is scoped to one adapter identity; another live worker is untouched.
func (q *Queue) Recover(ctx context.Context, account Account) error {
	items, err := q.List(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Status == "sending" && Matches(item.Plan.Target, account) {
			if err = q.Finish(ctx, item, "unknown", "", "worker restarted during notification; reconcile before resend"); err != nil {
				return err
			}
		}
	}
	return nil
}

func Matches(target *Account, account Account) bool {
	return target != nil && target.Channel == account.Channel && target.AccountID == account.AccountID && target.TenantID == account.TenantID
}
