package dream

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
