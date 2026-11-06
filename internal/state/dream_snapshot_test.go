package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func publishDreamSnapshotMemory(t *testing.T, store *Store, memory Memory) {
	t.Helper()
	if err := store.memoryTransaction(context.Background(), func(tx *sql.Tx) error {
		return persistMemory(context.Background(), tx, memory, true)
	}); err != nil {
		t.Fatal(err)
	}
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return []string{"<missing>"}
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		entry := fmt.Sprintf("%s:%s:%o", rel, info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			entry += fmt.Sprintf(":%x", sha256.Sum256(raw))
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestReadDreamMemoriesSnapshotFileMemoryIsZeroWriteAndExact(t *testing.T) {
	store, home, memoryRoot, cacheRoot := fileStore(t)
	home = store.Home()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	p := DreamPartition{PersonID: "raw-person", AgentID: "agent", AudienceID: "user:canonical", Scope: "scope-a"}
	base := Memory{AgentID: p.AgentID, AudienceID: p.AudienceID, SenderID: p.PersonID, Scope: p.Scope, Kind: "preference", Status: "active", Revision: 1, SourceType: "dream_reviewed", CreatedAt: now, ExpiresAt: ptrTime(now.Add(time.Hour))}
	for _, memory := range []Memory{
		mergeMemory(base, "valid", "use concise replies"),
		mergeMemory(base, "expired", "expired", func(m *Memory) { m.ExpiresAt = ptrTime(now.Add(-time.Second)) }),
		mergeMemory(base, "inactive", "inactive", func(m *Memory) { m.Status = "invalidated" }),
		mergeMemory(base, "unreviewed", "unreviewed", func(m *Memory) { m.SourceType = "user_statement" }),
		mergeMemory(base, "other-person", "other", func(m *Memory) { m.SenderID = "other" }),
		mergeMemory(base, "other-scope", "other scope", func(m *Memory) { m.Scope = "scope-b" }),
	} {
		publishDreamSnapshotMemory(t, store, memory)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cacheRoot); err != nil {
		t.Fatal(err)
	}
	beforeSource, beforeCache, beforeHome := snapshotTree(t, memoryRoot), snapshotTree(t, cacheRoot), snapshotTree(t, home)
	memories, err := ReadDreamMemoriesSnapshot(context.Background(), home, p, now, 10)
	if err != nil || len(memories) != 1 || memories[0].ID != "valid" {
		t.Fatalf("snapshot memories: %+v %v", memories, err)
	}
	if fmt.Sprint(beforeSource) != fmt.Sprint(snapshotTree(t, memoryRoot)) || fmt.Sprint(beforeCache) != fmt.Sprint(snapshotTree(t, cacheRoot)) || fmt.Sprint(beforeHome) != fmt.Sprint(snapshotTree(t, home)) {
		t.Fatal("read-only Dream snapshot changed source, cache, or state home")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ReadDreamMemoriesSnapshot(canceled, home, p, now, 10); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled file-memory snapshot did not stop", err)
	}
	if fmt.Sprint(beforeSource) != fmt.Sprint(snapshotTree(t, memoryRoot)) || fmt.Sprint(beforeCache) != fmt.Sprint(snapshotTree(t, cacheRoot)) || fmt.Sprint(beforeHome) != fmt.Sprint(snapshotTree(t, home)) {
		t.Fatal("canceled Dream snapshot changed source, cache, or state home")
	}
}

func TestReadDreamMemoriesSnapshotDomainMetadataLimits(t *testing.T) {
	store, home, memoryRoot, cacheRoot := fileStore(t)
	home = store.Home()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	p := DreamPartition{PersonID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope"}
	memory := Memory{ID: "memory", AgentID: p.AgentID, AudienceID: p.AudienceID, SenderID: p.PersonID, Scope: p.Scope, Kind: "preference", Text: "text", Status: "active", Revision: 1, SourceType: "dream_reviewed", CreatedAt: now}
	publishDreamSnapshotMemory(t, store, memory)
	domain := store.files.manifest.Domains[0]
	manifest := store.files.manifest
	manifestPath := filepath.Join(store.files.sourceDir, "current.json")
	scopePath := filepath.Join(memoryRoot, "domains", domain.ID, "scope.json")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(scopePath)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, bytes.Repeat([]byte(" "), dreamSnapshotDomainFileLimit-len(raw)+1)...)
	if err = os.WriteFile(scopePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	beforeSource, beforeCache := snapshotTree(t, memoryRoot), snapshotTree(t, cacheRoot)
	if _, err = ReadDreamMemoriesSnapshot(context.Background(), home, p, now, 10); err == nil {
		t.Fatal("oversized domain metadata was accepted")
	}
	if fmt.Sprint(beforeSource) != fmt.Sprint(snapshotTree(t, memoryRoot)) || fmt.Sprint(beforeCache) != fmt.Sprint(snapshotTree(t, cacheRoot)) {
		t.Fatal("rejected domain metadata read changed source or cache")
	}
	manifest.Domains = make([]fileDomain, dreamSnapshotDomainLimit+1)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, manifestRaw, 0600); err != nil {
		t.Fatal(err)
	}
	beforeSource = snapshotTree(t, memoryRoot)
	if _, err = ReadDreamMemoriesSnapshot(context.Background(), home, p, now, 10); err == nil || !strings.Contains(err.Error(), "domain or entry limit") {
		t.Fatal("unbounded domain manifest was accepted", err)
	}
	if fmt.Sprint(beforeSource) != fmt.Sprint(snapshotTree(t, memoryRoot)) {
		t.Fatal("rejected domain manifest changed source")
	}
	total := int64(dreamSnapshotTotalLimit - 1)
	if err = addDreamSnapshotBytes(&total, 1); err != nil {
		t.Fatal("exact domain budget was rejected", err)
	}
	if err = addDreamSnapshotBytes(&total, 1); err == nil {
		t.Fatal("domain metadata could exceed total snapshot budget")
	}
}

func TestReadDreamMemoriesSnapshotSQLiteAndCorruption(t *testing.T) {
	store, home := testStore(t)
	home = store.Home()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	p := DreamPartition{PersonID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope"}
	memory := Memory{ID: "sqlite", AgentID: p.AgentID, AudienceID: p.AudienceID, SenderID: p.PersonID, Scope: p.Scope, Kind: "active_task", Text: "finish review", Status: "active", Revision: 1, SourceType: "dream_reviewed", CreatedAt: now, ExpiresAt: ptrTime(now.Add(time.Hour))}
	if err := store.transaction(context.Background(), func(tx *sql.Tx) error { return persistMemory(context.Background(), tx, memory, true) }); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	databaseBefore, err := os.ReadFile(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadDreamMemoriesSnapshot(context.Background(), home, p, now, 10)
	databaseAfter, readErr := os.ReadFile(filepath.Join(home, "state.db"))
	if err != nil || readErr != nil || len(got) != 1 || got[0].ID != memory.ID || string(databaseBefore) != string(databaseAfter) {
		t.Fatalf("SQLite snapshot changed state or missed memory: %+v %v", got, err)
	}

	fileStore, fileHome, _, _ := fileStore(t)
	fileHome = fileStore.Home()
	if err = os.Chmod(fileHome, 0700); err != nil {
		t.Fatal(err)
	}
	publishDreamSnapshotMemory(t, fileStore, memory)
	manifest := fileStore.files.manifest
	entry := manifest.Entries[0]
	record := filepath.Join(fileStore.files.revisionPath(entry, entry.Current), "record.json")
	if err = fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "record.json")
	raw, _ := os.ReadFile(record)
	if err = os.WriteFile(outside, raw, 0600); err == nil {
		err = os.Remove(record)
	}
	if err == nil {
		err = os.Link(outside, record)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReadDreamMemoriesSnapshot(context.Background(), fileHome, p, now, 10); err == nil {
		t.Fatal("hardlinked current revision was accepted")
	}
}

func TestReadDreamMemoriesSnapshotRejectsUnsafeSQLite(t *testing.T) {
	for _, mutation := range []string{"symlink", "hardlink", "permissions", "payload"} {
		t.Run(mutation, func(t *testing.T) {
			store, _ := testStore(t)
			home := store.Home()
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			p := DreamPartition{PersonID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope"}
			memory := Memory{ID: "sqlite", AgentID: p.AgentID, AudienceID: p.AudienceID, SenderID: p.PersonID, Scope: p.Scope, Kind: "preference", Text: "brief", Status: "active", Revision: 1, SourceType: "dream_reviewed"}
			if err := store.transaction(context.Background(), func(tx *sql.Tx) error { return persistMemory(context.Background(), tx, memory, true) }); err != nil {
				t.Fatal(err)
			}
			if mutation == "payload" {
				memory.SenderID = "another-person"
				raw, err := json.Marshal(memory)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.db.Exec("UPDATE memories SET payload=? WHERE id=?", raw, memory.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(home, "state.db")
			var err error
			switch mutation {
			case "symlink":
				outside := filepath.Join(t.TempDir(), "database")
				if err = os.Rename(database, outside); err == nil {
					err = os.Symlink(outside, database)
				}
			case "hardlink":
				err = os.Link(database, filepath.Join(t.TempDir(), "database"))
			case "permissions":
				err = os.Chmod(database, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ReadDreamMemoriesSnapshot(context.Background(), home, p, time.Now().UTC(), 10); err == nil {
				t.Fatal("unsafe SQLite source was accepted")
			}
		})
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func mergeMemory(base Memory, id, text string, changes ...func(*Memory)) Memory {
	base.ID, base.Text = id, text
	for _, change := range changes {
		change(&base)
	}
	return base
}
