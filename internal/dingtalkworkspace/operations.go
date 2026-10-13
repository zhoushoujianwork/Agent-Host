package dingtalkworkspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var operationID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func operations(c Config) (*sql.DB, error) {
	if err := privateDirectory(c.StateDirectory); err != nil {
		return nil, err
	}
	path := filepath.Join(c.StateDirectory, "host-operations.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		_ = f.Close()
	} else if !os.IsExist(err) {
		return nil, errors.New("cannot create DingTalk operation store")
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("DingTalk operation store must be private")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("DingTalk operation store owner mismatch")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS operations(profile TEXT NOT NULL,id TEXT NOT NULL,digest TEXT NOT NULL,status TEXT NOT NULL,result TEXT NOT NULL,updated TEXT NOT NULL,PRIMARY KEY(profile,id));`)
	if err != nil {
		db.Close()
		return nil, errors.New("cannot initialize DingTalk operation store")
	}
	return db, nil
}

func receipt(db *sql.DB, profile, id string) (map[string]any, string, error) {
	return receiptAt(db, profile, id, 0)
}

func receiptAt(db *sql.DB, profile, id string, offset int) (map[string]any, string, error) {
	var digest, status, result, updated string
	err := db.QueryRow(`SELECT digest,status,result,updated FROM operations WHERE profile=? AND id=?`, profile, id).Scan(&digest, &status, &result, &updated)
	if err != nil {
		return nil, "", err
	}
	var payload any
	if offset < 0 || offset > len(result) || offset < len(result) && !utf8.RuneStart(result[offset]) {
		return nil, "", errors.New("invalid result byte offset")
	}
	if result != "" && len(result) <= 12000 && offset == 0 {
		if json.Unmarshal([]byte(result), &payload) != nil {
			return nil, "", errors.New("invalid saved DingTalk operation result")
		}
	}
	r := map[string]any{"profile": profile, "operation_id": id, "status": status, "result": payload, "updated_at": updated, "business_acceptance_verified": false}
	if len(result) > 12000 || offset > 0 {
		end := min(offset+8000, len(result))
		for end < len(result) && !utf8.RuneStart(result[end]) {
			end--
		}
		r["result_text"], r["offset"], r["total_bytes"] = result[offset:end], offset, len(result)
		r["partial_result"] = end < len(result)
		if end < len(result) {
			r["next_offset"] = end
		}
	}
	return r, digest, nil
}

func operationStatus(c Config, id string, offset int) (any, error) {
	if !operationID.MatchString(id) {
		return nil, errors.New("invalid operation_id")
	}
	db, err := operations(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	r, _, err := receiptAt(db, c.Profile, id, offset)
	if err != nil {
		return nil, errors.New("DingTalk operation not found")
	}
	return r, nil
}

func executeOnce(ctx context.Context, c Config, id string, request Request, run Runner) (any, error) {
	if !operationID.MatchString(id) {
		return nil, errors.New("write operations require a unique operation_id (1-64 letters, digits, underscores or hyphens)")
	}
	b, _ := json.Marshal(request)
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	db, err := operations(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO operations VALUES(?,?,?,'pending','',?) ON CONFLICT(profile,id) DO NOTHING`, c.Profile, id, digest, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, errors.New("cannot reserve DingTalk operation")
	}
	// Claim through the inserted-row count so concurrent MCP instances cannot
	// both execute the same request. Pending survives worker/Host interruption.
	var changes int
	if err = db.QueryRow(`SELECT changes()`).Scan(&changes); err != nil {
		return nil, err
	}
	if changes == 0 {
		prior, previous, e := receipt(db, c.Profile, id)
		if e != nil {
			return nil, e
		}
		if previous != digest {
			return nil, errors.New("operation_id belongs to a different DingTalk request")
		}
		return prior, nil
	}
	raw, runErr := run(ctx, request)
	status := "completed"
	var outcome struct {
		Outcome string `json:"outcome"`
		OK      *bool  `json:"ok"`
		Success *bool  `json:"success"`
	}
	if json.Unmarshal(raw, &outcome) == nil {
		if outcome.Outcome == "failure" || outcome.OK != nil && !*outcome.OK || outcome.Success != nil && !*outcome.Success {
			status = "failed"
		}
		if outcome.Outcome == "partial_failure" {
			status = "partial_failure"
		}
		if outcome.Outcome == "pending" {
			status = "provider_pending"
		}
	}
	if runErr != nil {
		var official *CommandFailure
		if !errors.As(runErr, &official) || len(raw) == 0 {
			status = "unknown"
			raw = nil
		} else if status == "completed" {
			status = "failed"
		}
	}
	if _, err = db.Exec(`UPDATE operations SET status=?,result=?,updated=? WHERE profile=? AND id=?`, status, string(raw), time.Now().UTC().Format(time.RFC3339Nano), c.Profile, id); err != nil {
		return nil, errors.New("DingTalk execution finished but saving its receipt failed; do not replay")
	}
	r, _, err := receipt(db, c.Profile, id)
	if err != nil {
		return nil, errors.New("DingTalk receipt was saved but cannot be read; inspect before retrying")
	}
	if runErr != nil {
		r["failure"] = runErr.Error()
	}
	return r, nil
}
