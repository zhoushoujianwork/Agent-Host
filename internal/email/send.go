package email

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/smtp"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

func (p *imapProvider) smtpConnect(ctx context.Context) (*smtp.Client, func(), error) {
	password, e := secret(p.account)
	if e != nil {
		return nil, nil, e
	}
	dial := p.dial
	if dial == nil {
		dial = dialTLS
	}
	conn, e := dial(ctx, "smtp.163.com:465")
	if e != nil {
		return nil, nil, e
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	close := func() { stop(); conn.Close() }
	c, e := smtp.NewClient(conn, "smtp.163.com")
	if e != nil {
		close()
		return nil, nil, errors.New("SMTP greeting failed")
	}
	if e = c.Auth(smtp.PlainAuth("", p.account.Address, password, "smtp.163.com")); e != nil {
		close()
		return nil, nil, errors.New("SMTP authentication failed")
	}
	return c, close, nil
}
func (p *imapProvider) Send(ctx context.Context, raw []byte, to []string) (string, error) {
	c, close, e := p.smtpConnect(ctx)
	if e != nil {
		return "", e
	}
	defer close()
	if e = c.Mail(p.account.Address); e != nil {
		return "", errors.New("SMTP sender rejected")
	}
	for _, r := range to {
		if e = c.Rcpt(r); e != nil {
			return "", errors.New("SMTP recipient rejected")
		}
	}
	w, e := c.Data()
	if e != nil {
		return "", errors.New("SMTP DATA rejected")
	}
	if _, e = w.Write(raw); e != nil {
		return "", errors.New("SMTP write failed; submission outcome unknown")
	}
	if e = w.Close(); e != nil {
		return "", errors.New("SMTP completion failed; submission outcome unknown")
	}
	return "smtp_accepted", nil
}
func openOperations(c Config) (*sql.DB, error) {
	if e := os.MkdirAll(c.StateDirectory, 0700); e != nil {
		return nil, errors.New("cannot create private email state")
	}
	info, e := os.Lstat(c.StateDirectory)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("email state must be an owner-private directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("email state owner mismatch")
	}
	path := filepath.Join(c.StateDirectory, "operations.db")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e == nil {
		f.Close()
	} else if !os.IsExist(e) {
		return nil, errors.New("cannot create email operation store")
	}
	fi, e := os.Lstat(path)
	if e != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("email operation store must be private and regular")
	}
	owner, ok = fi.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("email operation store owner mismatch")
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, errors.New("cannot open email operation store")
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(`PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS operations(account TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, status TEXT NOT NULL, message_id TEXT NOT NULL, receipt TEXT NOT NULL DEFAULT '', updated TEXT NOT NULL, PRIMARY KEY(account,id));`); e != nil {
		db.Close()
		return nil, errors.New("cannot initialize email operation store")
	}
	return db, nil
}
func readOperation(db *sql.DB, account, id string) (map[string]any, string, error) {
	var digest, status, message, receipt, updated string
	e := db.QueryRow(`SELECT digest,status,message_id,receipt,updated FROM operations WHERE account=? AND id=?`, account, id).Scan(&digest, &status, &message, &receipt, &updated)
	if e != nil {
		return nil, "", e
	}
	return map[string]any{"account_id": account, "operation_id": id, "status": status, "message_id": message, "provider_receipt": receipt, "updated_at": updated, "recipient_delivery_verified": false}, digest, nil
}
func operationStatus(c Config, account, id string) (any, error) {
	if !identifier.MatchString(id) {
		return nil, errors.New("invalid operation_id")
	}
	db, e := openOperations(c)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	r, _, e := readOperation(db, account, id)
	if e != nil {
		return nil, errors.New("operation not found or unavailable")
	}
	return r, nil
}
func sendOnce(ctx context.Context, c Config, account Account, p Provider, a Args, reply bool) (any, error) {
	if !identifier.MatchString(a.OperationID) {
		return nil, errors.New("operation_id must be 1-64 letters, digits, underscores or hyphens")
	}
	canonical, _ := json.Marshal(struct {
		Account   Account
		Arguments Args
		Reply     bool
	}{account, a, reply})
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	db, e := openOperations(c)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	existing, prior, e := readOperation(db, account.ID, a.OperationID)
	if e == nil {
		if prior != digest {
			return nil, errors.New("operation_id already belongs to different content")
		}
		return existing, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, errors.New("cannot inspect prior send operation")
	}
	var original []byte
	if reply {
		ref, e := decodeRef(a.MessageID, account.ID)
		if e != nil {
			return nil, e
		}
		original, e = p.Raw(ctx, ref)
		if e != nil {
			return nil, e
		}
	}
	raw, to, messageID, e := composeMessage(account, a, original)
	if e != nil {
		return nil, e
	}
	// Commit intent before any network submission. A crash leaves pending, never an implicit resend.
	_, e = db.ExecContext(ctx, `INSERT INTO operations(account,id,digest,status,message_id,updated) VALUES(?,?,?,'pending',?,?)`, account.ID, a.OperationID, digest, messageID, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		existing, prior, readErr := readOperation(db, account.ID, a.OperationID)
		if readErr == nil && prior == digest {
			return existing, nil
		}
		return nil, errors.New("cannot reserve send operation")
	}
	receipt, sendErr := p.Send(ctx, raw, to)
	status := "accepted"
	if sendErr != nil {
		status = "unknown"
		receipt = ""
	}
	if _, e = db.Exec(`UPDATE operations SET status=?,receipt=?,updated=? WHERE account=? AND id=?`, status, receipt, time.Now().UTC().Format(time.RFC3339Nano), account.ID, a.OperationID); e != nil {
		return nil, errors.New("submission outcome not durably recorded; do not resend; inspect operation_status")
	}
	result, _, e := readOperation(db, account.ID, a.OperationID)
	if e != nil {
		return nil, errors.New("send receipt unavailable; do not resend")
	}
	if sendErr != nil {
		result["error"] = sendErr.Error()
		result["automatic_retry_allowed"] = false
	}
	return result, nil
}
