package dream

import (
	"agent-host/internal/state"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cleanupFixture(t *testing.T) (*state.Store, string, HOTBundle, time.Time) {
	t.Helper()
	s, e := state.Open(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	b := HOTBundle{Version: "dream-hot-v1", Partition: state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}, Documents: map[string]string{"USER.md": "current", "MEMORY.md": "memory", "TASKS.md": "tasks"}}
	b.Digest = hotBundleDigest(b)
	return s.WithPartition(b.Partition.AgentID, b.Partition.AudienceID), root, b, time.Now().UTC()
}
func oldSnapshot(t *testing.T, root string, current HOTBundle, now time.Time, text string) (HOTBundle, string) {
	t.Helper()
	b := current
	b.Documents = map[string]string{"USER.md": text, "MEMORY.md": "old", "TASKS.md": "old"}
	b.Digest = hotBundleDigest(b)
	path, e := PublishHOT(root, b)
	if e != nil {
		t.Fatal(e)
	}
	old := now.Add(-40 * 24 * time.Hour)
	if e = os.Chtimes(path, old, old); e != nil {
		t.Fatal(e)
	}
	return b, path
}

func TestCleanupHOTRetainsCurrentYoungAndModified(t *testing.T) {
	store, root, current, now := cleanupFixture(t)
	_, old := oldSnapshot(t, root, current, now, "old")
	_, modified := oldSnapshot(t, root, current, now, "modified")
	if e := os.WriteFile(filepath.Join(modified, "USER.md"), []byte("external edit"), 0600); e != nil {
		t.Fatal(e)
	}
	_, young := oldSnapshot(t, root, current, now, "young")
	if e := os.Chtimes(young, now, now); e != nil {
		t.Fatal(e)
	}
	currentPath, e := PublishHOT(root, current)
	if e != nil {
		t.Fatal(e)
	}
	aged := now.Add(-40 * 24 * time.Hour)
	if e = os.Chtimes(currentPath, aged, aged); e != nil {
		t.Fatal(e)
	}
	result, e := CleanupHOT(context.Background(), store, root, current, now, 30*24*time.Hour, 20)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Completed) != 1 || result.Skipped[modified] == "" {
		t.Fatalf("unexpected cleanup: %+v", result)
	}
	if _, e = os.Stat(old); !os.IsNotExist(e) {
		t.Fatal("expired snapshot retained", e)
	}
	for _, path := range []string{currentPath, young, modified} {
		if _, e = os.Stat(path); e != nil {
			t.Fatal("protected snapshot removed", path, e)
		}
	}
	records, e := store.DreamCleanupRecords(context.Background(), current.Partition, "completed", 20)
	if e != nil || len(records) != 1 {
		t.Fatal(records, e)
	}
	result, e = CleanupHOT(context.Background(), store, root, current, now, 30*24*time.Hour, 20)
	if e != nil || len(result.Completed) != 0 {
		t.Fatal("cleanup repeated", result, e)
	}
}

func TestCleanupHOTResumesPartialQuarantineFromDurableIntent(t *testing.T) {
	store, root, current, now := cleanupFixture(t)
	old, path := oldSnapshot(t, root, current, now, "interrupted")
	info, e := os.Lstat(path)
	if e != nil {
		t.Fatal(e)
	}
	stat, _ := infoSys(info)
	manifest, _ := json.MarshalIndent(old, "", "  ")
	manifest = append(manifest, '\n')
	r := state.DreamCleanup{ID: "resume-cleanup", Partition: old.Partition, Path: path, Digest: old.Digest, Manifest: manifest, Inode: stat.Ino, Device: uint64(stat.Dev), Status: "planned", PlannedAt: now}
	if e = store.SaveDreamCleanup(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	quarantine := filepath.Join(filepath.Dir(path), ".retire-"+old.Digest)
	if e = os.Rename(path, quarantine); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(quarantine, "manifest.json")); e != nil {
		t.Fatal(e)
	}
	result, e := CleanupHOT(context.Background(), store, root, current, now, 30*24*time.Hour, 20)
	if e != nil || len(result.Completed) != 1 {
		t.Fatal(result, e)
	}
	if _, e = os.Stat(quarantine); !os.IsNotExist(e) {
		t.Fatal("partial cleanup did not finish", e)
	}
}

func TestCleanupHOTRejectsSymlinkHardlinkAndUnknownFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			store, root, current, now := cleanupFixture(t)
			_, path := oldSnapshot(t, root, current, now, kind)
			outside := filepath.Join(t.TempDir(), "outside")
			if e := os.WriteFile(outside, []byte(kind), 0600); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "unknown":
				if e := os.WriteFile(filepath.Join(path, "extra"), []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			default:
				if e := os.Remove(filepath.Join(path, "USER.md")); e != nil {
					t.Fatal(e)
				}
				var e error
				if kind == "symlink" {
					e = os.Symlink(outside, filepath.Join(path, "USER.md"))
				} else {
					e = os.Link(outside, filepath.Join(path, "USER.md"))
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			old := now.Add(-40 * 24 * time.Hour)
			if e := os.Chtimes(path, old, old); e != nil {
				t.Fatal(e)
			}
			result, e := CleanupHOT(context.Background(), store, root, current, now, 30*24*time.Hour, 20)
			if e != nil || len(result.Completed) != 0 || result.Skipped[path] == "" {
				t.Fatal(result, e)
			}
			if _, e = os.Stat(outside); e != nil {
				t.Fatal("external file affected", e)
			}
		})
	}
}

func TestCleanupCancellationLeavesSnapshot(t *testing.T) {
	store, root, current, now := cleanupFixture(t)
	_, path := oldSnapshot(t, root, current, now, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := CleanupHOT(ctx, store, root, current, now, 30*24*time.Hour, 20); e == nil {
		t.Fatal("cancellation ignored")
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("cancelled cleanup removed snapshot", e)
	}
}

func TestCleanupIntentNeverDeletesReplacementDirectory(t *testing.T) {
	store, root, current, now := cleanupFixture(t)
	old, path := oldSnapshot(t, root, current, now, "replace")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := infoSys(info)
	manifest, _ := json.Marshal(old)
	r := state.DreamCleanup{ID: "replacement-intent", Partition: old.Partition, Path: path, Digest: old.Digest, Manifest: manifest, Inode: stat.Ino, Device: uint64(stat.Dev), Status: "planned", PlannedAt: now}
	if err = store.SaveDreamCleanup(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, path+"-preserved"); err != nil {
		t.Fatal(err)
	}
	_, replacement := oldSnapshot(t, root, current, now, "replace")
	_, eligible := oldSnapshot(t, root, current, now, "still-eligible")
	result, err := CleanupHOT(context.Background(), store, root, current, now, 30*24*time.Hour, 20)
	if err != nil || result.Skipped[path] == "" || len(result.Completed) != 1 {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(replacement); err != nil {
		t.Fatal("replacement deleted", err)
	}
	if _, err = os.Stat(eligible); !os.IsNotExist(err) {
		t.Fatal("blocked intent starved eligible cleanup", err)
	}
}
