package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestActivityInspectionRejectsMissingAndOldState(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenForActivityInspection(missing); err == nil {
		t.Fatal("missing explicit activity root was accepted")
	}
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
	if _, err = db.Exec("PRAGMA user_version=17"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenForActivityInspection(home); err == nil {
		t.Fatal("old explicit activity root was accepted")
	}
}
