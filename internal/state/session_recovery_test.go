package state

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestChannelSessionRecoveryIsAtomicAndRequiresTerminalFailure(t *testing.T) {
	for _, status := range []string{"failed", "canceled", "unknown", "running", "completed"} {
		t.Run(status, func(t *testing.T) {
			s, _ := testStore(t)
			r := addRun(t, s, "original", "/project", "Original request", false)
			if status != "running" {
				if err := s.CompleteRun(testContext, r.ID, status, "original evidence", "model", ""); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Run(testContext, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.BindChannelSession(testContext, "binding", r.SessionID); err != nil {
				t.Fatal(err)
			}
			replacement := Session{ID: "fresh", Scope: "/project", Model: "test", NativeSessionID: "new-native"}
			if err := s.CreateSession(testContext, replacement); err != nil {
				t.Fatal(err)
			}
			var cause *SessionResumeError
			if err := s.SessionCanResume(testContext, r.SessionID); !errors.As(err, &cause) || cause.RunID != r.ID || cause.CanStartFresh() != (status != "running") {
				t.Fatal("resume classification", cause, err)
			}
			if err := s.RecoverChannelSession(testContext, "binding", "stale-binding", replacement.ID, r.ID); err == nil {
				t.Fatal("stale binding recovered")
			}
			if err := s.RecoverChannelSession(testContext, "binding", r.SessionID, replacement.ID, "stale-run"); err == nil {
				t.Fatal("stale run recovered")
			}
			err = s.RecoverChannelSession(testContext, "binding", r.SessionID, replacement.ID, r.ID)
			if (err != nil) != (status == "running") {
				t.Fatal("unexpected recovery outcome", err)
			}
			bound, err := s.ChannelSession(testContext, "binding")
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.Session(testContext, replacement.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "running" {
				if bound != r.SessionID || saved.PreviousSessionID != "" {
					t.Fatal("active run binding or provenance changed", bound, saved)
				}
			} else {
				if bound != replacement.ID || saved.PreviousSessionID != r.SessionID || saved.RecoveryRunID != r.ID {
					t.Fatal("recovery provenance missing", bound, saved)
				}
				if err := s.RecoverChannelSession(testContext, "binding", r.SessionID, replacement.ID, r.ID); err == nil {
					t.Fatal("repeated recovery succeeded")
				}
			}
			after, err := s.Run(testContext, r.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("recovery rewrote old execution or memory evidence", err)
			}
		})
	}
}

func TestCorruptRunCannotTriggerSessionRecovery(t *testing.T) {
	for _, corruption := range []string{"invalid-json", "inconsistent-state"} {
		t.Run(corruption, func(t *testing.T) {
			s, _ := testStore(t)
			r := addRun(t, s, "original", "/project", "Original request", false)
			if err := s.CompleteRun(testContext, r.ID, "failed", "original evidence", "model", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.BindChannelSession(testContext, "binding", r.SessionID); err != nil {
				t.Fatal(err)
			}
			replacement := Session{ID: "fresh", Scope: "/project", NativeSessionID: "new-native"}
			if err := s.CreateSession(testContext, replacement); err != nil {
				t.Fatal(err)
			}
			payload := []byte("invalid json")
			if corruption == "inconsistent-state" {
				var err error
				payload, err = json.Marshal(r) // Still running while indexed state is failed.
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.ExecContext(testContext, "UPDATE runs SET payload=? WHERE id=?", payload, r.ID); err != nil {
				t.Fatal(err)
			}
			var cause *SessionResumeError
			if err := s.SessionCanResume(testContext, r.SessionID); err == nil || errors.As(err, &cause) {
				t.Fatal("corrupt evidence treated as a recoverable failure", err)
			}
			if err := s.RecoverChannelSession(testContext, "binding", r.SessionID, replacement.ID, r.ID); err == nil {
				t.Fatal("corrupt session recovered")
			}
			bound, err := s.ChannelSession(testContext, "binding")
			if err != nil || bound != r.SessionID {
				t.Fatal("corrupt session binding changed", bound, err)
			}
		})
	}
}
