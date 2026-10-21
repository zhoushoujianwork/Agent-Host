package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/state"
)

func memoryStorageCommand(t *testing.T, args ...string) state.FileMemoryReport {
	t.Helper()
	var output bytes.Buffer
	if err := Run(t.Context(), append([]string{"memory"}, args...), nil, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report state.FileMemoryReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestMemoryVerifyRemainsReadOnlyAcrossRoomSchemaUpgrade(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "state")
	memoryStorageCommand(t, "migrate", "--home", home, "--memory-root", filepath.Join(root, "memory"), "--memory-cache", filepath.Join(root, "cache"), "--memory-source", "room-upgrade")
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("DROP TABLE room_messages; DROP TABLE rooms; PRAGMA user_version=15;"); err != nil {
		t.Fatal(err)
	}
	report := memoryStorageCommand(t, "verify", "--home", home)
	if report.Digest == "" {
		t.Fatal("missing verified publication")
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 15 {
		t.Fatal("read-only verifier migrated an active writer's database", version, err)
	}
}

func historicalMemorySQL(t *testing.T, home, id string) (string, int, []byte) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	var payload []byte
	var revisions int
	if err := db.QueryRow("SELECT status,payload FROM memories WHERE id=?", id).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_revisions WHERE memory_id=?", id).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	return status, revisions, payload
}

func TestMemoryStorageCLIHistoricalMigrationInspectionAndLosslessRollback(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	fixture := seedReviewFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	memoryRoot, cacheRoot := filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")
	status, revisions, original := historicalMemorySQL(t, fixture.home, fixture.memoryID)
	if status != "active" || revisions != 1 {
		t.Fatal("historical fixture does not have one active reviewed revision")
	}
	args := []string{"migrate", "--home", fixture.home, "--memory-root", memoryRoot, "--memory-cache", cacheRoot, "--memory-source", "historical-cli"}
	migrated := memoryStorageCommand(t, args...)
	if migrated.Memories != 1 || migrated.Skills != 1 || migrated.Revisions != 1 || migrated.Evidence != 1 || migrated.Digest == "" {
		t.Fatalf("migration lost historical skill or evidence: %+v", migrated)
	}
	repeated := memoryStorageCommand(t, args...)
	if repeated.Digest != migrated.Digest || repeated.Generation != migrated.Generation {
		t.Fatalf("idempotent migration changed history: %+v %+v", migrated, repeated)
	}
	var output bytes.Buffer
	if err := Run(t.Context(), fixture.args("invalidate", "--reason", "historical validation disables this procedure", fixture.memoryID), nil, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	unchangedStatus, unchangedRevisions, unchanged := historicalMemorySQL(t, fixture.home, fixture.memoryID)
	if unchangedStatus != status || unchangedRevisions != revisions || !bytes.Equal(unchanged, original) {
		t.Fatal("file-backed mutation dual-wrote legacy SQL memory rows")
	}
	if err := os.RemoveAll(cacheRoot); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Run(t.Context(), fixture.args("show", fixture.memoryID), nil, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var current state.Memory
	if err := json.Unmarshal(output.Bytes(), &current); err != nil || current.Status != "invalidated" || current.Revision != 2 {
		t.Fatalf("inspector read stale SQL memory after cache rebuild: %+v %v", current, err)
	}
	verified := memoryStorageCommand(t, "verify", "--home", fixture.home)
	if verified.Memories != 1 || verified.Revisions != 2 || verified.Generation <= migrated.Generation || verified.Digest == migrated.Digest {
		t.Fatalf("verification omitted new disabled revision: %+v", verified)
	}
	restored := memoryStorageCommand(t, "rollback", "--home", fixture.home)
	if restored.Digest != verified.Digest || restored.Revisions != 2 {
		t.Fatalf("rollback lost file-only updates: %+v", restored)
	}
	if _, err := os.Stat(state.FileMemoryMarker(fixture.home)); !os.IsNotExist(err) {
		t.Fatal("rollback retained active backend marker")
	}
	rolledStatus, rolledRevisions, rolledPayload := historicalMemorySQL(t, fixture.home, fixture.memoryID)
	var rolledMemory state.Memory
	if err := json.Unmarshal(rolledPayload, &rolledMemory); err != nil || rolledStatus != "invalidated" || rolledRevisions != 2 || rolledMemory.ReviewNote != current.ReviewNote {
		t.Fatalf("rollback discarded the disabled revision: %+v %v", rolledMemory, err)
	}
	files, err := filepath.Glob(filepath.Join(memoryRoot, "domains", "*", "skills", fixture.memoryID, "revisions", "2", "SKILL.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("rollback deleted authoritative historical files: %v %v", files, err)
	}
}

func TestMemoryStorageCLIRejectsMissingOrChangedIdentityAndOverlappingRoots(t *testing.T) {
	fixture := seedReviewFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"memory", "migrate", "--home", fixture.home, "--memory-root", filepath.Join(root, "memory"), "--memory-cache", filepath.Join(root, "cache")},
		{"memory", "migrate", "--home", fixture.home, "--memory-root", filepath.Join(root, "overlap"), "--memory-cache", filepath.Join(root, "overlap", "index"), "--memory-source", "cli"},
		{"memory", "verify", "--home", fixture.home},
		{"memory", "rollback", "--home", fixture.home},
	} {
		if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted invalid storage command: %v", args)
		}
		if _, err := os.Stat(state.FileMemoryMarker(fixture.home)); !os.IsNotExist(err) {
			t.Fatal("failed validation switched backend")
		}
	}
	args := []string{"migrate", "--home", fixture.home, "--memory-root", filepath.Join(root, "memory"), "--memory-cache", filepath.Join(root, "cache"), "--memory-source", "cli"}
	memoryStorageCommand(t, args...)
	args[len(args)-1] = "other-channel"
	if err := Run(t.Context(), append([]string{"memory"}, args...), nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "different file-memory identity") {
		t.Fatalf("changed source identity silently merged memories: %v", err)
	}
}
