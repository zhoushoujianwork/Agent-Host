package state

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func downgradeActivityStateToV19(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err = db.Exec(`
		DROP TABLE dream_cleanup;
		DROP TABLE dream_work_items;
		DROP TABLE dream_scan_cursors;
		PRAGMA user_version=19;
	`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestForegroundActivityIsUnpartitionedAndReadOnly(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	other := store.WithPartition("other-agent", "other-audience")
	session := Session{ID: "activity-session", AgentID: "other-agent", AudienceID: "other-audience", Scope: "scope", NativeSessionID: "native"}
	if err = other.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 5, 1, 2, 3, 4, time.UTC)
	run := Run{ID: "activity-run", SessionID: session.ID, AgentID: session.AgentID, AudienceID: session.AudienceID, Scope: session.Scope, Input: "foreground", NativeSessionID: session.NativeSessionID, ExecutionStatus: "running", MemoryStatus: "blocked", CreatedAt: created}
	if err = other.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	message := channelInput("activity-message")
	message.AgentID, message.AudienceID = "third-agent", "third-audience"
	message.ReceivedAt = created.Add(time.Second)
	if inserted, enqueueErr := store.EnqueueChannelMessage(context.Background(), message, testReplyURL); enqueueErr != nil || !inserted {
		t.Fatal(inserted, enqueueErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	inspection, err := OpenForActivityInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	activity, err := inspection.ForegroundActivity(context.Background())
	if err != nil || !activity.Busy || !activity.LastForeground.Equal(message.ReceivedAt) {
		t.Fatalf("activity=%+v err=%v", activity, err)
	}
	if _, err = inspection.db.Exec("UPDATE runs SET execution_status='completed'"); err == nil {
		t.Fatal("activity inspection allowed a write")
	}
	if err = inspection.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestActivityInspectionReadsSchema19WithoutMigration(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 6, 2, 3, 4, 5, time.UTC)
	session := Session{ID: "schema19-session", Scope: "scope", NativeSessionID: "native-schema19"}
	if err = store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "schema19-run", SessionID: session.ID, Scope: session.Scope, Input: "active native conversation", NativeSessionID: session.NativeSessionID, ExecutionStatus: "running", MemoryStatus: "blocked", CreatedAt: created}
	if err = store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	message := channelInput("schema19-message")
	message.ReceivedAt = created.Add(time.Second)
	if inserted, enqueueErr := store.EnqueueChannelMessage(t.Context(), message, testReplyURL); enqueueErr != nil || !inserted {
		t.Fatal(inserted, enqueueErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db := downgradeActivityStateToV19(t, home)

	if ordinary, openErr := Open(home, true); openErr == nil {
		ordinary.Close()
		t.Fatal("ordinary Host read-only open accepted schema 19")
	}
	if history, openErr := OpenForHistoryInspection(home); openErr == nil {
		history.Close()
		t.Fatal("history inspection accepted schema 19")
	}
	inspection, err := OpenForActivityInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	if inspection.FileMemoryEnabled() {
		t.Fatal("activity inspection initialized file memory")
	}
	activity, err := inspection.ForegroundActivity(t.Context())
	if err != nil || !activity.Busy || !activity.LastForeground.Equal(message.ReceivedAt) {
		t.Fatalf("schema 19 activity=%+v err=%v", activity, err)
	}
	if _, err = inspection.db.Exec("UPDATE runs SET execution_status='completed'"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("schema 19 activity inspection write result: %v", err)
	}
	if _, err = db.Exec("UPDATE runs SET execution_status='completed'; UPDATE channel_messages SET status='completed'"); err != nil {
		t.Fatal(err)
	}
	activity, err = inspection.ForegroundActivity(t.Context())
	if err != nil || activity.Busy {
		t.Fatalf("schema 19 idle activity=%+v err=%v", activity, err)
	}
	var version, dream20Tables int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('dream_cleanup','dream_work_items','dream_scan_cursors')`).Scan(&dream20Tables); err != nil {
		t.Fatal(err)
	}
	if version != 19 || dream20Tables != 0 {
		t.Fatalf("activity inspection migrated schema: version=%d dream20_tables=%d", version, dream20Tables)
	}
}

func TestActivityInspectionRejectsMissingAndUnsupportedState(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenForActivityInspection(missing); err == nil {
		t.Fatal("missing explicit activity root was accepted")
	}
	for _, version := range []int{17, SchemaVersion + 1} {
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			home := t.TempDir()
			store, err := Open(home, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "state.db")
			if _, err = os.Stat(path); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if inspection, openErr := OpenForActivityInspection(home); openErr == nil {
				inspection.Close()
				t.Fatalf("activity inspection accepted schema %d", version)
			}
		})
	}
}
