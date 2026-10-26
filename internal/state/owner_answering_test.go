package state

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOwnerAnsweringMigrationAndClaims(t *testing.T) {
	home := t.TempDir()
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("DROP TABLE owner_answering; PRAGMA user_version=16;"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatal("migration failed", version, err)
	}
	r := OwnerAnsweringRecord{Key: "question:one", Payload: json.RawMessage(`{"status":"pending"}`)}
	if err = s.CompareAndSaveOwnerAnswering(t.Context(), &r); err != nil {
		t.Fatal(err)
	}
	stale := r
	r.Payload = json.RawMessage(`{"status":"executing"}`)
	if err = s.CompareAndSaveOwnerAnswering(t.Context(), &r); err != nil {
		t.Fatal(err)
	}
	if err = s.CompareAndSaveOwnerAnswering(t.Context(), &stale); err == nil {
		t.Fatal("competing execution claimed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, err := s.OwnerAnsweringRecord(t.Context(), r.Key)
	if err != nil || string(saved.Payload) != string(r.Payload) {
		t.Fatal("claim lost across restart", saved, err)
	}
}

func TestOwnerAnsweringIntakeBatchRollsBackTogether(t *testing.T) {
	s, err := Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := OwnerAnsweringRecord{Key: "question:stable", Payload: json.RawMessage(`{"attempted":true}`)}
	if err = s.CompareAndSaveOwnerAnswering(t.Context(), &q); err != nil {
		t.Fatal(err)
	}
	stale := q
	q.Payload = json.RawMessage(`{"attempted":true,"status":"waiting_owner"}`)
	if err = s.CompareAndSaveOwnerAnswering(t.Context(), &q); err != nil {
		t.Fatal(err)
	}
	source := OwnerAnsweringRecord{Key: "source:one", Payload: json.RawMessage(`{"through":"2026-10-26T00:00:00Z","active_question":"stable"}`)}
	if err = s.CompareAndSaveOwnerAnsweringBatch(t.Context(), &source, &stale); err == nil {
		t.Fatal("stale question committed with watermark")
	}
	if _, err = s.OwnerAnsweringRecord(t.Context(), source.Key); err == nil || source.Revision != 0 {
		t.Fatal("failed batch advanced intake")
	}
	if err = s.CompareAndSaveOwnerAnsweringBatch(t.Context(), &source, &q); err != nil {
		t.Fatal(err)
	}
	if source.Revision != 1 || q.Revision != 3 {
		t.Fatal("atomic revisions not advanced")
	}
}
