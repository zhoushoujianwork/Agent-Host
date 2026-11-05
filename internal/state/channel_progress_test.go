package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func progressFixture(t *testing.T) (*Store, ChannelMessage, Run, ChannelProgressBinding) {
	t.Helper()
	s, _ := testStore(t)
	m, r := phaseRun(t, s, "progress")
	if err := s.EnableChannelProgress(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	b, err := s.ChannelProgressBinding(t.Context(), m.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, m, r, b
}
func TestProgressUnicodeBoundaries(t *testing.T) {
	for _, tt := range []struct {
		text string
		ok   bool
	}{{"  hi\n", true}, {strings.Repeat("界", 200), true}, {strings.Repeat("😀", 200), true}, {strings.Repeat("界", 201), false}, {" \t\n", false}, {string([]byte{0xff}), false}, {strings.Repeat("e\u0301", 101), false}} {
		got, err := ValidateProgressText(tt.text)
		if (err == nil) != tt.ok || err == nil && got != strings.TrimSpace(tt.text) {
			t.Fatalf("%q: %q %v", tt.text, got, err)
		}
	}
}
func TestProgressConcurrentReservationsIdempotencyCooldownQuota(t *testing.T) {
	s, _, _, b := progressFixture(t)
	// Independent subprocess-like connections exercise SQLite writer atomicity.
	other, err := OpenForChannelProgress(s.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	ids := make(chan int64, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 0 {
				db = other
			}
			p, e := db.ReserveChannelProgress(context.Background(), b, "first", "same")
			ids <- p.ID
			errs <- e
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id int64
	for got := range ids {
		if id != 0 && id != got {
			t.Fatal("duplicate attempt", id, got)
		}
		id = got
	}
	if _, err = s.ReserveChannelProgress(t.Context(), b, "too soon", "new"); err == nil {
		t.Fatal("cooldown ignored")
	}
	for i := 1; i < 5; i++ {
		if _, err = s.db.Exec("UPDATE channel_progress SET created_at=? WHERE run_id=?", time.Now().Add(-16*time.Second).UTC().Format(reactionTimeFormat), b.RunID); err != nil {
			t.Fatal(err)
		}
		p, err := s.ReserveChannelProgress(t.Context(), b, "next", "")
		if err != nil {
			t.Fatal(err)
		}
		claimed, _, err := s.ClaimNextChannelProgress(t.Context(), "profile")
		if err != nil {
			t.Fatal(err)
		}
		status := "failed"
		if i%2 == 0 {
			status = "unknown"
		}
		if err = s.FinishChannelProgress(t.Context(), claimed.ID, status); err != nil {
			t.Fatal(err)
		}
		_ = p
	}
	if _, err = s.db.Exec("UPDATE channel_progress SET created_at=?", time.Now().Add(-16*time.Second).UTC().Format(reactionTimeFormat)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveChannelProgress(t.Context(), b, "sixth", ""); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal("quota ignored", err)
	}
	if p, err := s.ReserveChannelProgress(t.Context(), b, "different text", "same"); err != nil || p.ID != id || p.Text != "first" {
		t.Fatal("idempotency lost at limit", p, err)
	}
}
func TestProgressRevalidatesReservationAndDispatch(t *testing.T) {
	for _, change := range []string{"revoke", "completed", "receipt", "session", "source", "runtime", "partition", "scope", "stale-binding"} {
		t.Run(change, func(t *testing.T) {
			s, m, r, b := progressFixture(t)
			p, err := s.ReserveChannelProgress(t.Context(), b, "reserved", "key")
			if err != nil {
				t.Fatal(err)
			}
			err = s.transaction(t.Context(), func(tx *sql.Tx) error {
				switch change {
				case "revoke":
					_, e := tx.Exec("UPDATE channel_progress_runs SET enabled=0")
					return e
				case "completed":
					r.ExecutionStatus = "completed"
					return writeRun(t.Context(), tx, r)
				case "receipt":
					m, err = readChannelMessage(t.Context(), tx, m.ID)
					if err != nil {
						return err
					}
					m.Status = "failed"
					return writeChannelMessage(t.Context(), tx, m)
				case "session":
					session, e := readSession(t.Context(), tx, r.SessionID)
					if e != nil {
						return e
					}
					session.NativeSessionID = "different"
					raw, e := json.Marshal(session)
					if e != nil {
						return e
					}
					_, e = tx.Exec("UPDATE sessions SET payload=? WHERE id=?", raw, session.ID)
					return e
				case "source":
					r.Source.SenderID = "forged"
				case "runtime":
					r.RuntimeFingerprint = "changed"
				case "partition":
					r.AudienceID = "other"
				case "scope":
					r.Scope = "other"
				case "stale-binding":
					_, e := tx.Exec("DELETE FROM channel_sessions WHERE binding=?", m.Binding)
					return e
				}
				raw, e := json.Marshal(r)
				if e != nil {
					return e
				}
				_, e = tx.Exec("UPDATE runs SET execution_status=?,payload=? WHERE id=?", r.ExecutionStatus, raw, r.ID)
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ReserveChannelProgress(t.Context(), b, "repeat", "key"); !errors.Is(err, ErrProgressUnavailable) {
				t.Fatal("stale reservation accepted", err)
			}
			got, _, err := s.ClaimNextChannelProgress(t.Context(), m.Profile)
			if err != nil || got.ID != p.ID || got.Status != "failed" || got.DispatchStartedAt != "" {
				t.Fatal("stale dispatch authorized", got, err)
			}
		})
	}
}
func TestProgressRecoveryAndFinalIndependence(t *testing.T) {
	s, m, r, b := progressFixture(t)
	p, err := s.ReserveChannelProgress(t.Context(), b, "reserved", "key")
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := s.ClaimNextChannelProgress(t.Context(), m.Profile)
	if err != nil || claimed.DispatchStartedAt == "" {
		t.Fatal(claimed, err)
	}
	if _, _, err = s.ClaimNextChannelProgress(t.Context(), m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("dispatch replayed", err)
	}
	if err = s.transaction(t.Context(), func(tx *sql.Tx) error { return recoverChannelProgress(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "unknown" {
		t.Fatal(got, err)
	}
	receipt, err := s.ChannelMessage(t.Context(), m.ID)
	if err != nil || receipt.ReplyStatus != "pending" || receipt.ReplyText != "" || receipt.Status != "claimed" {
		t.Fatal(receipt, err)
	}
	saved, err := s.Run(t.Context(), r.ID)
	if err != nil || saved.ExecutionStatus != "running" || saved.MemoryStatus != "pending_execution" || saved.AcceptanceStatus != "unverified" {
		t.Fatal(saved, err)
	}
	reactionReply(t, s, m, "completed", "accepted")
}
func TestProgressAuxiliaryOpener(t *testing.T) {
	home := t.TempDir()
	if s, err := OpenForChannelProgress(home); err == nil {
		s.Close()
		t.Fatal("created missing database")
	}
	s, _, _, _ := progressFixture(t)
	other, err := OpenForChannelProgress(s.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.files != nil || other.eventDB != nil {
		t.Fatal("initialized auxiliary state")
	}
	var syncMode int
	if err = other.db.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil || syncMode != 2 {
		t.Fatal(syncMode, err)
	}
	// Current schema only: an old database stays old and receives no new tables.
	path := filepath.Join(home, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA user_version=17"); err != nil {
		t.Fatal(err)
	}
	if p, err := OpenForChannelProgress(home); err == nil {
		p.Close()
		t.Fatal("migrated old state")
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatal(version, err)
	}
}

func TestProgressMigrationFrom17AndRecoveryUnclaimed(t *testing.T) {
	s, home := testStore(t)
	if _, err := s.db.Exec("DROP TABLE channel_progress; DROP TABLE channel_progress_runs; PRAGMA user_version=17;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version, count int
	if err = reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 18 {
		t.Fatal(version, err)
	}
	if err = reopened.db.QueryRow("SELECT COUNT(*) FROM channel_progress").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	m, r := phaseRun(t, reopened, "unclaimed")
	if err = reopened.EnableChannelProgress(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	b, err := reopened.ChannelProgressBinding(t.Context(), m.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := reopened.ReserveChannelProgress(t.Context(), b, "never dispatched", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.transaction(t.Context(), func(tx *sql.Tx) error { return recoverChannelProgress(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.ChannelProgress(t.Context(), p.ID)
	if err != nil || got.Status != "failed" || got.DispatchStartedAt != "" {
		t.Fatal(got, err)
	}
	if _, _, err = reopened.ClaimNextChannelProgress(t.Context(), m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("recovery replayed pending", err)
	}
}
func TestProgressConcurrentNewKeysAndDispatchClaim(t *testing.T) {
	s, m, _, b := progressFixture(t)
	other, err := OpenForChannelProgress(s.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	success := make(chan ChannelProgress, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 0 {
				db = other
			}
			p, e := db.ReserveChannelProgress(context.Background(), b, "work", strings.Repeat("k", i+1))
			if e == nil {
				success <- p
			}
		}(i)
	}
	wg.Wait()
	close(success)
	if len(success) != 1 {
		t.Fatal("concurrent callers bypassed cooldown", len(success))
	}
	claimed := make(chan ChannelProgress, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 0 {
				db = other
			}
			p, _, e := db.ClaimNextChannelProgress(context.Background(), m.Profile)
			if e == nil {
				claimed <- p
			} else if !errors.Is(e, sql.ErrNoRows) {
				t.Errorf("claim: %v", e)
			}
		}(i)
	}
	wg.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatal("concurrent dispatch duplicated", len(claimed))
	}
}

func TestProgressBindingSurvivesAuthorizedGeneratedNativeIdentity(t *testing.T) {
	s, m, r, _ := progressFixture(t)
	pending := OffloadPendingPrefix + r.SessionID
	if err := s.transaction(t.Context(), func(tx *sql.Tx) error {
		session, err := readSession(t.Context(), tx, r.SessionID)
		if err != nil {
			return err
		}
		session.NativeSessionID = pending
		raw, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE sessions SET payload=? WHERE id=?", raw, session.ID); err != nil {
			return err
		}
		r.NativeSessionID = pending
		r.Agent = "offload"
		return writeRun(t.Context(), tx, r)
	}); err != nil {
		t.Fatal(err)
	}
	b, err := s.ChannelProgressBinding(t.Context(), m.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ReserveChannelProgress(t.Context(), b, "before native init", "key")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindHostNativeSession(t.Context(), r.ID, pending, "generated-native"); err != nil {
		t.Fatal(err)
	}
	after, err := s.ChannelProgressBinding(t.Context(), m.ID, r.ID)
	if err != nil || after != b {
		t.Fatal("native init invalidated Host run authority", after, b, err)
	}
	got, _, err := s.ClaimNextChannelProgress(t.Context(), m.Profile)
	if err != nil || got.ID != p.ID || got.DispatchStartedAt == "" {
		t.Fatal(got, err)
	}
}
