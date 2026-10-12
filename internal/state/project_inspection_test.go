package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Build the actual prior schema, including its old reaction constraints, rather
// than relabeling a current database with an earlier user_version.
func projectInspectionV7Fixture(t *testing.T) (string, Project) {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, schema := range []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaReactionsV6, schemaProjectsV7} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	legacy := &Store{db: db}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, err := legacy.RegisterProject(context.Background(), "registered", "Registered workspace", workspace)
	if err != nil {
		t.Fatal(err)
	}
	return home, project
}

// Keep the previous delivered schema genuine: additive future tables must not
// accidentally be present in inspection and migration fixtures.
func priorSchemaV11Store(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, schema := range []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5,
		schemaReactionsV6, schemaProjectsV7, schemaRuntimePhasesV8, schemaRoutingV9,
		schemaPartitionsV10, schemaChannelInputV11} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db}, home
}

func TestProjectInspectionReadsSchema11WithoutMigrationOrWrites(t *testing.T) {
	legacy, home := priorSchemaV11Store(t)
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want, err := legacy.RegisterProject(testContext, "registered", "Previous delivered workspace", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	inspected, err := OpenForProjectInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	defer inspected.Close()
	got, err := inspected.ProjectByWorkspace(testContext, want.Workspace)
	if err != nil || got.ID != want.ID || got.Description != want.Description || got.Scope != want.Scope {
		t.Fatalf("previous schema project changed: %+v %v", got, err)
	}
	var version int
	if err := inspected.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 11 {
		t.Fatalf("inspection migrated previous state: version=%d err=%v", version, err)
	}
	if _, err := inspected.db.Exec("UPDATE projects SET name='unexpected-write' WHERE id=?", want.ID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("previous schema inspection allowed a database write: %v", err)
	}
	if SchemaVersion > 11 {
		if ordinary, err := Open(home, true); err == nil {
			ordinary.Close()
			t.Fatal("ordinary read-only open accepted an unmigrated previous database")
		}
	}
}

func TestProjectInspectionReadsSchema7WithoutMigrationOrWrites(t *testing.T) {
	home, want := projectInspectionV7Fixture(t)
	store, err := OpenForProjectInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for name, read := range map[string]func() (Project, error){
		"id":        func() (Project, error) { return store.Project(ctx, want.ID) },
		"workspace": func() (Project, error) { return store.ProjectByWorkspace(ctx, want.Workspace) },
		"scope":     func() (Project, error) { return store.ProjectByScope(ctx, want.Scope) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil || got.ID != want.ID || got.Workspace != want.Workspace || got.Scope != want.Scope {
				t.Fatalf("project resolution changed: %+v %v", got, err)
			}
		})
	}
	projects, err := store.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != want.ID {
		t.Fatalf("project list changed: %+v %v", projects, err)
	}
	if _, err := store.db.Exec("UPDATE projects SET name='unexpected-write' WHERE id=?", want.ID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("inspection allowed a database write: %v", err)
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("inspection migrated state: version=%d err=%v", version, err)
	}
	var reactionsSchema string
	if err := store.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='channel_reactions'").Scan(&reactionsSchema); err != nil || strings.Contains(reactionsSchema, "knowledge_read") {
		t.Fatalf("inspection changed prior reaction schema: %v", err)
	}
	if ordinary, err := Open(home, true); err == nil {
		ordinary.Close()
		t.Fatal("ordinary read-only open accepted an unmigrated database")
	}
}

func TestProjectInspectionSupportsCurrentSchemaOnlyAndExplicitPriorVersion(t *testing.T) {
	home := t.TempDir()
	current, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	inspected, err := OpenForProjectInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspected.Close(); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 6, SchemaVersion + 1} {
		db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		// Each rejected version is outside the explicit project-inspection compatibility
		// window; its contents must never be used for project inspection.
		_, err = db.Exec("PRAGMA user_version=" + strconv.Itoa(version))
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("prepare schema version %d: %v %v", version, err, closeErr)
		}
		if inspected, err := OpenForProjectInspection(home); err == nil {
			inspected.Close()
			t.Fatalf("accepted unsupported project schema %d", version)
		}
	}
}

func TestProjectInspectionDoesNotCreateMissingState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	if store, err := OpenForProjectInspection(home); err == nil {
		store.Close()
		t.Fatal("missing state unexpectedly opened")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("inspection created a state directory: %v", err)
	}
}
