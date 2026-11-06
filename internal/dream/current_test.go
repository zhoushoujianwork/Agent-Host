package dream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/state"
)

func currentFixture(t *testing.T) (string, HOTSource, HOTBundle) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, home} {
		if err = os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	b := HOTBundle{Version: "dream-hot-v1", Partition: state.DreamPartition{PersonID: "person", AgentID: "agent", AudienceID: "owner", Scope: "scope"}, Documents: map[string]string{"USER.md": "user", "MEMORY.md": "memory", "TASKS.md": "tasks"}}
	b.Digest = hotBundleDigest(b)
	return root, HOTSource{StateHome: home, ConfigID: "config-id"}, b
}

func publishCurrentVersion(t *testing.T, root string, source HOTSource, base HOTBundle, text string) HOTBundle {
	t.Helper()
	b := base
	b.Documents = map[string]string{"USER.md": text, "MEMORY.md": "memory", "TASKS.md": "tasks"}
	b.Digest = hotBundleDigest(b)
	if _, err := PublishCurrent(root, source, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCleanupCurrentProtectsCurrentAndYoungAndRetiresOld(t *testing.T) {
	root, source, base := currentFixture(t)
	old := publishCurrentVersion(t, root, source, base, "old")
	young := publishCurrentVersion(t, root, source, base, "young")
	current := publishCurrentVersion(t, root, source, base, "current")
	id := targetCoordinationKey(base.Partition)
	versions := filepath.Join(root, id, "versions")
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(versions, old.Digest), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupCurrent(context.Background(), root, source, base.Partition, time.Now(), 24*time.Hour, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Completed) != 1 {
		t.Fatalf("expected one retired version: %#v", result)
	}
	if _, err = os.Lstat(filepath.Join(versions, old.Digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old version remains", err)
	}
	for _, digest := range []string{young.Digest, current.Digest} {
		if _, err = os.Lstat(filepath.Join(versions, digest)); err != nil {
			t.Fatal("protected version missing", digest, err)
		}
	}
	metadata, err := loadCurrentPartition(filepath.Join(root, id), id, source, base.Partition)
	if err != nil || metadata.Manifests[old.Digest] != "" || metadata.Manifests[current.Digest] == "" {
		t.Fatal("manifest index mismatch", metadata, err)
	}
}

func TestCleanupCurrentSkipsModifiedAndHardlinkedVersions(t *testing.T) {
	for _, hardlink := range []bool{false, true} {
		t.Run(fmt.Sprint("hardlink=", hardlink), func(t *testing.T) {
			root, source, base := currentFixture(t)
			old := publishCurrentVersion(t, root, source, base, "old")
			_ = publishCurrentVersion(t, root, source, base, "current")
			path := filepath.Join(root, targetCoordinationKey(base.Partition), "versions", old.Digest)
			oldTime := time.Now().Add(-48 * time.Hour)
			_ = os.Chtimes(path, oldTime, oldTime)
			file := filepath.Join(path, "USER.md")
			if hardlink {
				body, _ := os.ReadFile(file)
				outside := filepath.Join(t.TempDir(), "outside")
				_ = os.WriteFile(outside, body, 0600)
				_ = os.Remove(file)
				if err := os.Link(outside, file); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, oldTime, oldTime); err != nil {
				t.Fatal(err)
			}
			result, err := CleanupCurrent(context.Background(), root, source, base.Partition, time.Now(), 24*time.Hour, 200)
			if err != nil || len(result.Completed) != 0 || result.Skipped[old.Digest] == "" {
				t.Fatal("unsafe version was not skipped", result, err)
			}
			if _, err = os.Lstat(path); err != nil {
				t.Fatal("unsafe version was deleted", err)
			}
		})
	}
}

func TestCleanupCurrentResumesPartialQuarantine(t *testing.T) {
	root, source, base := currentFixture(t)
	old := publishCurrentVersion(t, root, source, base, "old")
	_ = publishCurrentVersion(t, root, source, base, "current")
	id := targetCoordinationKey(base.Partition)
	partitionRoot := filepath.Join(root, id)
	path := filepath.Join(partitionRoot, "versions", old.Digest)
	oldTime := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(path, oldTime, oldTime)
	metadata, err := loadCurrentPartition(partitionRoot, id, source, base.Partition)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := buildCleanupIntent(path, root, source, base.Partition, old.Digest, metadata.Manifests[old.Digest], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	intentRoot, err := cleanupIntentRoot(root, base.Partition, true)
	if err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(partitionRoot, ".retired-"+intent.ID)
	if err = os.Rename(path, quarantine); err != nil {
		t.Fatal(err)
	}
	if err = writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(quarantine, "USER.md")); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupCurrent(context.Background(), root, source, base.Partition, time.Now(), 24*time.Hour, 200)
	if err != nil || len(result.Completed) != 1 {
		t.Fatal("partial quarantine not recovered", result, err)
	}
	if _, err = os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quarantine remains", err)
	}
}

func TestCleanupCurrentWaitsForPythonSharedReader(t *testing.T) {
	root, source, base := currentFixture(t)
	_ = publishCurrentVersion(t, root, source, base, "current")
	lock := filepath.Join(root, targetCoordinationKey(base.Partition), ".publish.lock")
	command := exec.Command("python3", "-c", `import fcntl,os,sys,time
fd=os.open(sys.argv[1],os.O_RDONLY|getattr(os,"O_NOFOLLOW",0))
fcntl.flock(fd,fcntl.LOCK_SH)
print("locked",flush=True)
time.sleep(2)`, lock)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if line, readErr := bufio.NewReader(stdout).ReadString('\n'); readErr != nil || line != "locked\n" {
		t.Fatal("python reader did not lock", line, readErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = CleanupCurrent(ctx, root, source, base.Partition, time.Now(), 24*time.Hour, 200); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("exclusive cleanup ignored Python shared reader", err)
	}
}

func TestCleanupCurrentSkipsReplacedInode(t *testing.T) {
	root, source, base := currentFixture(t)
	old := publishCurrentVersion(t, root, source, base, "old")
	_ = publishCurrentVersion(t, root, source, base, "current")
	id := targetCoordinationKey(base.Partition)
	partitionRoot, versions := filepath.Join(root, id), filepath.Join(root, id, "versions")
	path := filepath.Join(versions, old.Digest)
	metadata, _ := loadCurrentPartition(partitionRoot, id, source, base.Partition)
	intent, err := buildCleanupIntent(path, root, source, base.Partition, old.Digest, metadata.Manifests[old.Digest], time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	intentRoot, _ := cleanupIntentRoot(root, base.Partition, true)
	if err = writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "old")
	if err = os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.MarshalIndent(old, "", "  ")
	manifest = append(manifest, '\n')
	if err = publishCurrentSnapshot(versions, path, old, manifest); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupCurrent(context.Background(), root, source, base.Partition, time.Now(), 24*time.Hour, 200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); err != nil {
		t.Fatal("replacement inode was deleted", err)
	}
	if len(result.Completed) != 0 {
		t.Fatal("replacement was reported deleted", result)
	}
}

func TestPublishCurrentDoesNotLetOldIntentDeleteRepublishedDigest(t *testing.T) {
	root, source, base := currentFixture(t)
	old := publishCurrentVersion(t, root, source, base, "old")
	_ = publishCurrentVersion(t, root, source, base, "current")
	id := targetCoordinationKey(base.Partition)
	partitionRoot, versions := filepath.Join(root, id), filepath.Join(root, id, "versions")
	path := filepath.Join(versions, old.Digest)
	metadata, _ := loadCurrentPartition(partitionRoot, id, source, base.Partition)
	intent, err := buildCleanupIntent(path, root, source, base.Partition, old.Digest, metadata.Manifests[old.Digest], time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	intentRoot, _ := cleanupIntentRoot(root, base.Partition, true)
	quarantine := filepath.Join(partitionRoot, ".retired-"+intent.ID)
	if err = os.Rename(path, quarantine); err != nil {
		t.Fatal(err)
	}
	when := time.Now()
	intent.Status = "quarantined"
	intent.QuarantinedAt = &when
	if err = writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.MarshalIndent(old, "", "  ")
	manifest = append(manifest, '\n')
	if err = publishCurrentSnapshot(versions, path, old, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, old); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); err != nil {
		t.Fatal("republished version was removed by stale intent", err)
	}
	if target := currentTarget(t, filepath.Join(partitionRoot, "current")); target != "versions/"+old.Digest {
		t.Fatal("republished digest is not current", target)
	}
}

func TestPublishCurrentProtectsDiskCurrentWhileRecoveringOldPlan(t *testing.T) {
	root, source, base := currentFixture(t)
	old := publishCurrentVersion(t, root, source, base, "old-current")
	id := targetCoordinationKey(base.Partition)
	partitionRoot, versions := filepath.Join(root, id), filepath.Join(root, id, "versions")
	metadata, _ := loadCurrentPartition(partitionRoot, id, source, base.Partition)
	intent, err := buildCleanupIntent(filepath.Join(versions, old.Digest), root, source, base.Partition, old.Digest, metadata.Manifests[old.Digest], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	intentRoot, _ := cleanupIntentRoot(root, base.Partition, true)
	if err = writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
		t.Fatal(err)
	}
	newBundle := base
	newBundle.Documents = map[string]string{"USER.md": "new", "MEMORY.md": "memory", "TASKS.md": "tasks"}
	newBundle.Digest = hotBundleDigest(newBundle)
	bad := filepath.Join(versions, newBundle.Digest)
	if err = os.Mkdir(bad, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, newBundle); err == nil {
		t.Fatal("corrupt incoming version was accepted")
	}
	if target := currentTarget(t, filepath.Join(partitionRoot, "current")); target != "versions/"+old.Digest {
		t.Fatal("failed publication lost old current", target)
	}
	if _, err = os.Lstat(filepath.Join(versions, old.Digest)); err != nil {
		t.Fatal("old current was deleted before failed publish", err)
	}
	if err = os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, newBundle); err != nil {
		t.Fatal(err)
	}
	if target := currentTarget(t, filepath.Join(partitionRoot, "current")); target != "versions/"+newBundle.Digest {
		t.Fatal("successful publication did not switch", target)
	}
	if _, err = os.Lstat(filepath.Join(versions, old.Digest)); err != nil {
		t.Fatal("old current was deleted during switch recovery", err)
	}
}

func TestPublishCurrentRejectsMisnamedCleanupIntent(t *testing.T) {
	root, source, base := currentFixture(t)
	old := publishCurrentVersion(t, root, source, base, "old")
	id := targetCoordinationKey(base.Partition)
	partitionRoot := filepath.Join(root, id)
	metadata, _ := loadCurrentPartition(partitionRoot, id, source, base.Partition)
	intent, err := buildCleanupIntent(filepath.Join(partitionRoot, "versions", old.Digest), root, source, base.Partition, old.Digest, metadata.Manifests[old.Digest], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	intentRoot, _ := cleanupIntentRoot(root, base.Partition, true)
	if err = writeAtomicJSON(intentRoot, "wrong.json", intent); err != nil {
		t.Fatal(err)
	}
	newBundle := base
	newBundle.Documents = map[string]string{"USER.md": "new", "MEMORY.md": "memory", "TASKS.md": "tasks"}
	newBundle.Digest = hotBundleDigest(newBundle)
	if _, err = PublishCurrent(root, source, newBundle); err == nil || !strings.Contains(err.Error(), "invalid HOT cleanup intent") {
		t.Fatal("misnamed intent was accepted", err)
	}
}

func currentTarget(t *testing.T, current string) string {
	t.Helper()
	target, err := os.Readlink(current)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPublishCurrentSuccessRepeatAndMetadata(t *testing.T) {
	root, source, bundle := currentFixture(t)
	current, err := PublishCurrent(root, source, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if target := currentTarget(t, current); target != "versions/"+bundle.Digest {
		t.Fatal("unexpected current target", target)
	}
	if again, err := PublishCurrent(root, source, bundle); err != nil || again != current {
		t.Fatal("repeat publication was not idempotent", again, err)
	}
	locked := false
	if err := WithCurrentPartitionLock(context.Background(), root, bundle.Partition, func(partitionRoot string) error {
		locked = filepath.Dir(current) == partitionRoot
		return nil
	}); err != nil || !locked {
		t.Fatal("current partition maintenance lock seam failed", err)
	}
	var metadata currentPartition
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), "partition.json"))
	if err != nil || json.Unmarshal(raw, &metadata) != nil || metadata.Source != source || metadata.Partition != bundle.Partition || metadata.Manifests[bundle.Digest] == "" {
		t.Fatal("publication metadata mismatch", metadata, err)
	}
}

func TestPublishCurrentSerializesConcurrentSwitches(t *testing.T) {
	root, source, first := currentFixture(t)
	second := first
	second.Documents = map[string]string{"USER.md": "second", "MEMORY.md": "memory", "TASKS.md": "tasks"}
	second.Digest = hotBundleDigest(second)
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bundle := first
			if i%2 == 1 {
				bundle = second
			}
			_, err := PublishCurrent(root, source, bundle)
			errorsSeen <- err
		}(i)
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := hashStrings(first.Partition.PersonID, first.Partition.AgentID, first.Partition.AudienceID, first.Partition.Scope)[:24]
	target := currentTarget(t, filepath.Join(root, id, "current"))
	if target != "versions/"+first.Digest && target != "versions/"+second.Digest {
		t.Fatal("concurrent switch produced unsafe target", target)
	}
}

func TestPublishCurrentFailurePreservesOldCurrent(t *testing.T) {
	root, source, first := currentFixture(t)
	current, err := PublishCurrent(root, source, first)
	if err != nil {
		t.Fatal(err)
	}
	oldTarget := currentTarget(t, current)
	second := first
	second.Documents = map[string]string{"USER.md": "second", "MEMORY.md": "memory", "TASKS.md": "tasks"}
	second.Digest = hotBundleDigest(second)
	corrupt := filepath.Join(filepath.Dir(current), "versions", second.Digest)
	if err = os.Mkdir(corrupt, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, second); err == nil {
		t.Fatal("corrupt immutable target was accepted")
	}
	if target := currentTarget(t, current); target != oldTarget {
		t.Fatal("failed publication replaced old current", target)
	}
}

func TestPublishCurrentRejectsConflictingSourceAndUnsafeCurrent(t *testing.T) {
	root, source, bundle := currentFixture(t)
	current, err := PublishCurrent(root, source, bundle)
	if err != nil {
		t.Fatal(err)
	}
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, HOTSource{StateHome: other, ConfigID: source.ConfigID}, bundle); err == nil {
		t.Fatal("different source was merged into existing partition")
	}
	if err = os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("../outside", current); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, bundle); err == nil {
		t.Fatal("unsafe current link was replaced")
	}
}

func TestPublishCurrentRejectsTamperedSnapshotAndSymlinkRoot(t *testing.T) {
	root, source, bundle := currentFixture(t)
	current, err := PublishCurrent(root, source, bundle)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(current), filepath.FromSlash(currentTarget(t, current)))
	if err = os.WriteFile(filepath.Join(target, "MEMORY.md"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, bundle); err == nil {
		t.Fatal("tampered snapshot was reused")
	}
	parent := t.TempDir()
	linked := filepath.Join(parent, "hot")
	if err = os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(linked, source, bundle); err == nil {
		t.Fatal("symlinked HOT root was accepted")
	}
}

func TestPublishCurrentRejectsHardlinkedSnapshotFile(t *testing.T) {
	root, source, bundle := currentFixture(t)
	current, err := PublishCurrent(root, source, bundle)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(current), filepath.FromSlash(currentTarget(t, current)))
	user := filepath.Join(target, "USER.md")
	outside := filepath.Join(t.TempDir(), "same-user.md")
	if err = os.WriteFile(outside, []byte(bundle.Documents["USER.md"]), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(user); err == nil {
		err = os.Link(outside, user)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PublishCurrent(root, source, bundle); err == nil {
		t.Fatal("hardlinked immutable HOT file was accepted")
	}
}

func TestAcquireCurrentLockRejectsUnsafeFiles(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"public", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".publish.lock")
			test.setup(t, path)
			if release, err := acquireCurrentLock(root); err == nil {
				release()
				t.Fatal("unsafe publication lock was accepted")
			}
		})
	}
}

func TestCurrentMetadataAndPrivateReadsAreBounded(t *testing.T) {
	root := t.TempDir()
	value := currentPartition{Version: "dream-current-v1", ID: "partition", Manifests: map[string]string{}}
	for i := 0; i < 70000; i++ {
		value.Manifests[fmt.Sprintf("%064x", i)] = fmt.Sprintf("%064x", i)
	}
	if err := writeCurrentPartition(root, value); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal("oversized partition metadata was written", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "partition.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized metadata left a publication", err)
	}
	large := filepath.Join(root, "large")
	if err := os.WriteFile(large, make([]byte, 33), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateRegular(large, 32); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal("bounded private read accepted oversized file", err)
	}
}

func TestCurrentSwitchReportsPostRenameSyncUncertainty(t *testing.T) {
	root := t.TempDir()
	oldTarget, newTarget := "versions/"+strings.Repeat("a", 64), "versions/"+strings.Repeat("b", 64)
	if err := os.Symlink(oldTarget, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	err := replaceCurrentLinkWithSync(root, newTarget, func(string) error { return errors.New("fixture sync failed") })
	if err == nil || !strings.Contains(err.Error(), "committed") || !strings.Contains(err.Error(), "uncertain") {
		t.Fatal("post-rename sync failure was not distinguished", err)
	}
	if target, readErr := os.Readlink(filepath.Join(root, "current")); readErr != nil || target != newTarget {
		t.Fatal("fixture did not prove the rename committed", target, readErr)
	}
}
