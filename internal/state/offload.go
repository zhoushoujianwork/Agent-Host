package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// OffloadFingerprintPrefix marks sessions and runs created by the Offload
	// channel adapter. No other engine may rebind them.
	OffloadFingerprintPrefix = "offload:v1:"
	// OffloadPendingPrefix prefixes the Host-generated placeholder used until
	// Offload reports its own session ID. A placeholder is never an Offload ID.
	OffloadPendingPrefix = "offload-pending:"
	maxOffloadIDBytes    = 256
)

// IsOffloadPending reports whether id is an unbound Offload placeholder.
func IsOffloadPending(id string) bool {
	return strings.HasPrefix(id, OffloadPendingPrefix) && len(id) > len(OffloadPendingPrefix)
}

func validOffloadNativeID(id string) bool {
	if id == "" || len(id) > maxOffloadIDBytes || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// BindOffloadSession atomically replaces the initial placeholder native ID of a
// running Offload run and its session with the ID Offload reported. Binding the
// same actual ID again is idempotent. It never rebinds an actual ID, a terminal
// run, a session shared with earlier runs, or anything owned by another engine
// or partition.
func (s *Store) BindOffloadSession(ctx context.Context, runID, expectedNativeID, nativeID string) error {
	if runID == "" {
		return errors.New("offload binding requires a run ID")
	}
	if !validOffloadNativeID(nativeID) || IsOffloadPending(nativeID) {
		return errors.New("offload native session ID is invalid")
	}
	if !validOffloadNativeID(expectedNativeID) {
		return errors.New("expected offload native session ID is invalid")
	}
	if !IsOffloadPending(expectedNativeID) && expectedNativeID != nativeID {
		return errors.New("offload session is already bound to a different native session")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "running" {
			return errors.New("offload binding requires a running run")
		}
		session, err := s.readSession(ctx, tx, run.SessionID)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(session.RuntimeFingerprint, OffloadFingerprintPrefix) || run.RuntimeFingerprint != session.RuntimeFingerprint {
			return errors.New("session does not belong to an Offload runtime")
		}
		if session.Room != nil || run.Scope != session.Scope || !samePartition(session.AgentID, session.AudienceID, run.AgentID, run.AudienceID) {
			return errors.New("offload binding does not match its session")
		}
		if session.NativeSessionID != expectedNativeID || run.NativeSessionID != expectedNativeID {
			return errors.New("offload binding does not match the expected native session")
		}
		if expectedNativeID == nativeID {
			return nil
		}
		var others int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE session_id=? AND id<>?", session.ID, run.ID).Scan(&others); err != nil {
			return err
		}
		if others != 0 {
			return errors.New("only the first run of an Offload session can bind its native session")
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id<>? AND json_extract(CAST(payload AS TEXT),'$.native_session_id')=?", session.ID, nativeID).Scan(&others); err != nil {
			return err
		}
		if others != 0 {
			return errors.New("offload native session is already bound to another session")
		}
		session.NativeSessionID, run.NativeSessionID = nativeID, nativeID
		payload, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET payload=? WHERE id=?", payload, session.ID); err != nil {
			return wrap("bind offload session", err)
		}
		return writeRun(ctx, tx, run)
	})
}
