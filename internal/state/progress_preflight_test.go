package state

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProgressMigrationAllowsReadOnlyMemoryPreflight(t *testing.T) {
	s, home, _, _ := fileStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA user_version=17"); err != nil {
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
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatalf("preflight changed schema: %d %v", version, err)
	}
}
