package state

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRoomMigrationAllowsReadOnlyPreflightWithoutCreatingRoomTables(t *testing.T) {
	s, home, _, _ := fileStore(t)
	prior := addRun(t, s, "prior", "scope", "Existing evidence", false)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err = legacy.Exec("DROP TABLE room_messages; DROP TABLE rooms; PRAGMA user_version=15;"); err != nil {
		t.Fatal(err)
	}
	preflight, err := OpenForMemoryInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = preflight.VerifyFileMemory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = preflight.Close(); err != nil {
		t.Fatal(err)
	}
	var version, count int
	if err = legacy.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 15 {
		t.Fatal("preflight changed schema", version, err)
	}
	if err = legacy.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('rooms','room_messages')").Scan(&count); err != nil || count != 0 {
		t.Fatal("preflight created room tables", count, err)
	}
	upgraded, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if saved, err := upgraded.Run(t.Context(), prior.ID); err != nil || saved.Input != prior.Input {
		t.Fatal("migration lost existing evidence", saved, err)
	}
	if err = legacy.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("owner did not migrate", version, err)
	}
}
