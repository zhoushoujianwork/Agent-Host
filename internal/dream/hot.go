package dream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type HOTSource struct {
	StateHome string `json:"state_home"`
	ConfigID  string `json:"config_id"`
}

type currentPartition struct {
	Version   string               `json:"version"`
	ID        string               `json:"id"`
	Partition state.DreamPartition `json:"partition"`
	Source    HOTSource            `json:"source"`
	Manifests map[string]string    `json:"manifest_sha256"`
}

const currentMetadataLimit = 4 << 20

func PresetDocuments(bundle HOTBundle) []harness.PresetDocument {
	documents := []harness.PresetDocument{}
	if body := bundle.Documents["USER.md"]; body != "" {
		documents = append(documents, harness.PresetDocument{Name: "USERS.md", Content: []byte(body)})
	}
	memory := bundle.Documents["MEMORY.md"]
	if tasks := bundle.Documents["TASKS.md"]; tasks != "" {
		memory += "\n# Active tasks\n\n" + tasks
	}
	if memory != "" {
		documents = append(documents, harness.PresetDocument{Name: "MEMORY.md", Content: []byte(memory)})
	}
	return documents
}

func PublishHOT(cacheRoot string, bundle HOTBundle) (string, error) {
	if !filepath.IsAbs(cacheRoot) || filepath.Clean(cacheRoot) != cacheRoot || bundle.Digest == "" || hotBundleDigest(bundle) != bundle.Digest {
		return "", errors.New("HOT publication requires absolute cache root and digest")
	}
	canonical, err := filepath.EvalSymlinks(cacheRoot)
	if err != nil || canonical != cacheRoot {
		return "", errors.New("HOT cache root must be canonical without symlinks")
	}
	if err := validatePrivateDir(cacheRoot); err != nil {
		return "", errors.New("HOT cache root must be owner-private")
	}
	partition := hashStrings(bundle.Partition.PersonID, bundle.Partition.AgentID, bundle.Partition.AudienceID, bundle.Partition.Scope)[:24]
	dreamRoot, err := ensurePrivateChild(cacheRoot, "dream-hot")
	if err != nil {
		return "", err
	}
	root, err := ensurePrivateChild(dreamRoot, partition)
	if err != nil {
		return "", err
	}
	final := filepath.Join(root, bundle.Digest)
	manifest, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	manifest = append(manifest, '\n')
	if _, err := os.Lstat(final); err == nil {
		if err = verifyHOTSnapshot(final, bundle, manifest); err != nil {
			return "", err
		}
		return final, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, ".publish-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err = os.Chmod(tmp, 0700); err != nil {
		return "", err
	}
	for _, name := range []string{"USER.md", "MEMORY.md", "TASKS.md"} {
		if strings.Contains(bundle.Documents[name], "\x00") {
			return "", errors.New("invalid HOT document")
		}
		if err = os.WriteFile(filepath.Join(tmp, name), []byte(bundle.Documents[name]), 0600); err != nil {
			return "", err
		}
	}
	if err = os.WriteFile(filepath.Join(tmp, "manifest.json"), manifest, 0600); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, final); err != nil {
		if verifyErr := verifyHOTSnapshot(final, bundle, manifest); verifyErr == nil {
			return final, nil
		}
		return "", err
	}
	if err = verifyHOTSnapshot(final, bundle, manifest); err != nil {
		return "", err
	}
	return final, nil
}

// PublishCurrent publishes a read-only projection of an already verified HOT
// bundle. It does not grant runtime access and is never read back for injection.
func PublishCurrent(hotRoot string, source HOTSource, bundle HOTBundle) (string, error) {
	return PublishCurrentContext(context.Background(), hotRoot, source, bundle)
}

// PublishCurrentContext is PublishCurrent with cancellable bounded lock
// acquisition for coordinated shared publication.
func PublishCurrentContext(ctx context.Context, hotRoot string, source HOTSource, bundle HOTBundle) (string, error) {
	if err := validateCurrentRoot(hotRoot); err != nil {
		return "", err
	}
	if source.ConfigID == "" {
		return "", errors.New("HOT current source requires a config ID")
	}
	if sourceErr := validatePrivateDir(source.StateHome); sourceErr != nil {
		return "", fmt.Errorf("HOT current source requires a trusted private state home: %w", sourceErr)
	}
	canonicalHome, err := filepath.EvalSymlinks(source.StateHome)
	if err != nil || canonicalHome != source.StateHome || !filepath.IsAbs(source.StateHome) || filepath.Clean(source.StateHome) != source.StateHome {
		return "", errors.New("HOT current state home must be canonical")
	}
	if bundle.Digest == "" || hotBundleDigest(bundle) != bundle.Digest {
		return "", errors.New("HOT current requires a valid bundle digest")
	}
	id := hashStrings(bundle.Partition.PersonID, bundle.Partition.AgentID, bundle.Partition.AudienceID, bundle.Partition.Scope)[:24]
	partitionRoot, err := ensurePrivateChild(hotRoot, id)
	if err != nil {
		return "", err
	}
	release, err := acquireCurrentLockContext(ctx, partitionRoot)
	if err != nil {
		return "", err
	}
	defer release()

	metadata, err := loadCurrentPartition(partitionRoot, id, source, bundle.Partition)
	if err != nil {
		return "", err
	}
	protectedDigest, err := currentDigest(partitionRoot)
	if err != nil {
		return "", err
	}
	if err = recoverCurrentCleanupBeforePublish(ctx, hotRoot, partitionRoot, &metadata, protectedDigest); err != nil {
		return "", err
	}
	versions, err := ensurePrivateChild(partitionRoot, "versions")
	if err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	manifest = append(manifest, '\n')
	final := filepath.Join(versions, bundle.Digest)
	if err = publishCurrentSnapshot(versions, final, bundle, manifest); err != nil {
		return "", err
	}
	manifestSum := fmt.Sprintf("%x", sha256.Sum256(manifest))
	if prior := metadata.Manifests[bundle.Digest]; prior != "" && prior != manifestSum {
		return "", errors.New("HOT current manifest record conflicts with immutable snapshot")
	}
	if metadata.Manifests[bundle.Digest] == "" {
		metadata.Manifests[bundle.Digest] = manifestSum
		if err = writeCurrentPartition(partitionRoot, metadata); err != nil {
			return "", err
		}
	}
	if err = validateExistingCurrent(partitionRoot, metadata); err != nil {
		return "", err
	}
	target := filepath.ToSlash(filepath.Join("versions", bundle.Digest))
	if err = replaceCurrentLink(partitionRoot, target); err != nil {
		return "", err
	}
	return filepath.Join(partitionRoot, "current"), nil
}

func validateCurrentRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("HOT current root must be an absolute clean path")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || validatePrivateDir(root) != nil {
		return errors.New("HOT current root must exist, be canonical and owner-private")
	}
	return nil
}

func acquireCurrentLock(root string) (func(), error) {
	return acquireCurrentLockContext(context.Background(), root)
}

func acquireCurrentLockContext(ctx context.Context, root string) (func(), error) {
	path := filepath.Join(root, ".publish.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	valid := func() error {
		opened, statErr := f.Stat()
		pathInfo, pathErr := os.Lstat(path)
		openedStat, openedOK := infoSys(opened)
		pathStat, pathOK := infoSys(pathInfo)
		if statErr != nil || pathErr != nil || !openedOK || !pathOK || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || int(openedStat.Uid) != os.Geteuid() || openedStat.Nlink != 1 || pathInfo.Mode()&os.ModeSymlink != 0 || openedStat.Dev != pathStat.Dev || openedStat.Ino != pathStat.Ino {
			return errors.New("HOT current publication lock is unsafe")
		}
		return nil
	}
	if err = valid(); err != nil {
		f.Close()
		return nil, err
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			f.Close()
			return nil, errors.New("HOT current publication lock wait exceeded 5s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = valid(); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// WithCurrentPartitionLock holds the existing partition publication lock for a
// bounded maintenance callback. Callers must already hold any authorization
// locks required by their operation.
func WithCurrentPartitionLock(ctx context.Context, hotRoot string, partition state.DreamPartition, fn func(string) error) error {
	if fn == nil || !validSharedPartition(partition) {
		return errors.New("HOT current partition lock requires an exact partition and callback")
	}
	if err := validateCurrentRoot(hotRoot); err != nil {
		return err
	}
	id := hashStrings(partition.PersonID, partition.AgentID, partition.AudienceID, partition.Scope)[:24]
	root := filepath.Join(hotRoot, id)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || validatePrivateDir(root) != nil {
		return errors.New("HOT current partition is missing or unsafe")
	}
	release, err := acquireCurrentLockContext(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	return fn(root)
}

func loadCurrentPartition(root, id string, source HOTSource, partition state.DreamPartition) (currentPartition, error) {
	path := filepath.Join(root, "partition.json")
	want := currentPartition{Version: "dream-current-v1", ID: id, Partition: partition, Source: source, Manifests: map[string]string{}}
	raw, err := readPrivateRegular(path, currentMetadataLimit)
	if errors.Is(err, os.ErrNotExist) {
		if err = writeCurrentPartition(root, want); err != nil {
			return currentPartition{}, err
		}
		return want, nil
	}
	if err != nil {
		return currentPartition{}, err
	}
	var got currentPartition
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&got); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || got.Version != want.Version || got.ID != id || got.Partition != partition || got.Source != source || got.Manifests == nil {
		return currentPartition{}, errors.New("HOT current partition metadata conflicts with requested source or partition")
	}
	for digest, sum := range got.Manifests {
		if !validSnapshotDigest(digest) || !validSnapshotDigest(sum) {
			return currentPartition{}, errors.New("HOT current partition metadata contains an invalid manifest record")
		}
	}
	return got, nil
}

func writeCurrentPartition(root string, value currentPartition) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > currentMetadataLimit {
		return errors.New("HOT current partition metadata exceeds its size limit")
	}
	f, err := os.CreateTemp(root, ".partition-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = errors.Join(f.Chmod(0600), writeSyncClose(f, raw)); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(root, "partition.json")); err != nil {
		return err
	}
	return syncDirectory(root)
}

func publishCurrentSnapshot(parent, final string, bundle HOTBundle, manifest []byte) error {
	if _, err := os.Lstat(final); err == nil {
		return verifyHOTSnapshot(final, bundle, manifest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = os.Chmod(tmp, 0700); err != nil {
		return err
	}
	for _, name := range []string{"USER.md", "MEMORY.md", "TASKS.md"} {
		if strings.Contains(bundle.Documents[name], "\x00") {
			return errors.New("invalid HOT document")
		}
		if err = writePrivateFile(filepath.Join(tmp, name), []byte(bundle.Documents[name])); err != nil {
			return err
		}
	}
	if err = writePrivateFile(filepath.Join(tmp, "manifest.json"), manifest); err != nil {
		return err
	}
	if err = syncDirectory(tmp); err != nil {
		return err
	}
	if err = os.Rename(tmp, final); err != nil {
		if verifyErr := verifyHOTSnapshot(final, bundle, manifest); verifyErr != nil {
			return err
		}
	}
	if err = syncDirectory(parent); err != nil {
		return err
	}
	return verifyHOTSnapshot(final, bundle, manifest)
}

func validateExistingCurrent(root string, metadata currentPartition) error {
	path := filepath.Join(root, "current")
	info, statErr := os.Lstat(path)
	if statErr == nil {
		stat, ok := infoSys(info)
		if !ok || info.Mode()&os.ModeSymlink == 0 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
			return errors.New("HOT current link is unsafe")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	target, err := os.Readlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !validCurrentTarget(target) {
		return errors.New("HOT current link is unsafe")
	}
	digest := strings.TrimPrefix(filepath.ToSlash(target), "versions/")
	if metadata.Manifests[digest] == "" {
		return errors.New("HOT current link has no verified manifest record")
	}
	return verifyRecordedSnapshot(filepath.Join(root, filepath.FromSlash(target)), digest, metadata.Manifests[digest], metadata.Partition)
}

func validCurrentTarget(target string) bool {
	target = filepath.ToSlash(target)
	return strings.HasPrefix(target, "versions/") && len(target) == len("versions/")+64 && validSnapshotDigest(strings.TrimPrefix(target, "versions/"))
}

func verifyRecordedSnapshot(path, digest, manifestSum string, partition state.DreamPartition) error {
	raw, err := readPrivateRegular(filepath.Join(path, "manifest.json"), 8<<20)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != manifestSum {
		return errors.New("HOT current manifest bytes are unsafe or changed")
	}
	var bundle HOTBundle
	if json.Unmarshal(raw, &bundle) != nil || bundle.Partition != partition || bundle.Digest != digest || hotBundleDigest(bundle) != digest {
		return errors.New("HOT current manifest identity mismatch")
	}
	return verifyHOTSnapshot(path, bundle, raw)
}

func replaceCurrentLink(root, target string) error {
	return replaceCurrentLinkWithSync(root, target, syncDirectory)
}

func replaceCurrentLinkWithSync(root, target string, syncParent func(string) error) error {
	if !validCurrentTarget(target) {
		return errors.New("invalid HOT current target")
	}
	tmp, err := os.CreateTemp(root, ".current-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err = os.Remove(name); err == nil {
		err = os.Symlink(target, name)
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(root, "current"))
	}
	os.Remove(name)
	if err != nil {
		return err
	}
	if err = syncParent(root); err != nil {
		return fmt.Errorf("HOT current switch committed but directory sync failed; publication result is uncertain and must be verified: %w", err)
	}
	return nil
}

func writePrivateFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return writeSyncClose(f, body)
}

func writeSyncClose(f *os.File, body []byte) error {
	_, writeErr := f.Write(body)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func readPrivateRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	pathStat, ok := infoSys(info)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || int(pathStat.Uid) != os.Geteuid() || pathStat.Nlink != 1 {
		return nil, errors.New("unsafe private file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	openedStat, openedOK := infoSys(opened)
	if err != nil || !openedOK || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || int(openedStat.Uid) != os.Geteuid() || openedStat.Nlink != 1 || openedStat.Dev != pathStat.Dev || openedStat.Ino != pathStat.Ino {
		return nil, errors.New("unsafe private file")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("private file exceeds its size limit")
	}
	return body, nil
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("not a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("directory has another owner")
	}
	return nil
}

func ensurePrivateChild(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", errors.New("HOT publication path contains a symlink")
	}
	if err := validatePrivateDir(path); err != nil {
		return "", err
	}
	return path, nil
}

func verifyHOTSnapshot(path string, bundle HOTBundle, manifest []byte) error {
	if err := validatePrivateDir(path); err != nil {
		return errors.New("existing HOT snapshot is unsafe or corrupt")
	}
	expected := map[string][]byte{
		"USER.md": []byte(bundle.Documents["USER.md"]), "MEMORY.md": []byte(bundle.Documents["MEMORY.md"]),
		"TASKS.md": []byte(bundle.Documents["TASKS.md"]), "manifest.json": manifest,
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != len(expected) {
		return errors.New("existing HOT snapshot is unsafe or corrupt")
	}
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok {
			return errors.New("existing HOT snapshot contains unexpected files")
		}
		name := filepath.Join(path, entry.Name())
		info, statErr := os.Lstat(name)
		stat, statOK := infoSys(info)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !statOK || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
			return errors.New("existing HOT snapshot contains unsafe files")
		}
		body, readErr := os.ReadFile(name)
		if readErr != nil || !bytes.Equal(body, want) {
			return errors.New("existing HOT snapshot content does not match its digest")
		}
	}
	return nil
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
