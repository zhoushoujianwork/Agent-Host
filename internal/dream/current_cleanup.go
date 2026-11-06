package dream

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-host/internal/state"
)

const (
	currentCleanupVersion = "dream-current-cleanup-v1"
	currentCleanupLimit   = 512 << 10
	currentCleanupEntries = 512
)

type CurrentCleanupResult struct {
	Completed []string          `json:"completed"`
	Skipped   map[string]string `json:"skipped"`
}

type cleanupFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type currentCleanupIntent struct {
	Version        string               `json:"version"`
	ID             string               `json:"id"`
	TargetKey      string               `json:"target_key"`
	StateHome      string               `json:"state_home"`
	ConfigID       string               `json:"config_id"`
	HotRoot        string               `json:"hot_root"`
	Partition      state.DreamPartition `json:"partition"`
	Digest         string               `json:"digest"`
	ManifestSHA256 string               `json:"manifest_sha256"`
	Device         uint64               `json:"device"`
	Inode          uint64               `json:"inode"`
	Status         string               `json:"status"`
	Reason         string               `json:"reason"`
	PlannedAt      time.Time            `json:"planned_at"`
	QuarantinedAt  *time.Time           `json:"quarantined_at,omitempty"`
	CompletedAt    *time.Time           `json:"completed_at,omitempty"`
	Files          []cleanupFile        `json:"files"`
}

// CleanupCurrent retires only verified immutable versions below a fixed HOT
// partition. It never opens or mutates the source Store.
func CleanupCurrent(ctx context.Context, hotRoot string, source HOTSource, partition state.DreamPartition, now time.Time, retention time.Duration, limit int) (CurrentCleanupResult, error) {
	result := CurrentCleanupResult{Skipped: map[string]string{}}
	if retention < 24*time.Hour || limit < 1 || limit > 200 || source.ConfigID == "" || !validSharedPartition(partition) {
		return result, errors.New("current cleanup requires retention >=24h, limit 1..200, source and exact partition")
	}
	if err := validateCurrentRoot(hotRoot); err != nil {
		return result, err
	}
	canonical, err := filepath.EvalSymlinks(source.StateHome)
	if err != nil || canonical != source.StateHome || validatePrivateDir(source.StateHome) != nil {
		return result, errors.New("current cleanup source home is unsafe")
	}
	work := func() error {
		return WithCurrentPartitionLock(ctx, hotRoot, partition, func(partitionRoot string) error {
			return cleanupCurrentLocked(ctx, hotRoot, source, partition, partitionRoot, now.UTC(), retention, limit, &result)
		})
	}
	anchorPath, _, anchorErr := anchorPaths(source.StateHome, partition, false)
	if anchorErr == nil {
		if _, statErr := os.Lstat(anchorPath); statErr == nil {
			err = WithAuthorizationLock(ctx, CoordinationTarget{StateHome: source.StateHome, Partition: partition}, func(AuthorizationStatus) error { return work() })
			return result, err
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return result, statErr
		}
	} else if !errors.Is(anchorErr, os.ErrNotExist) {
		return result, anchorErr
	}
	err = work()
	return result, err
}

func cleanupIntentRoot(hotRoot string, partition state.DreamPartition, create bool) (string, error) {
	root, err := coordinationRoot(hotRoot)
	if err != nil {
		return "", err
	}
	cleanup := filepath.Join(root, "cleanup")
	target := filepath.Join(cleanup, targetCoordinationKey(partition))
	if create {
		if root, err = ensureCoordinationRoot(hotRoot); err == nil {
			cleanup, err = ensurePrivateChild(root, "cleanup")
		}
		if err == nil {
			target, err = ensurePrivateChild(cleanup, targetCoordinationKey(partition))
		}
		return target, err
	}
	for _, path := range []string{root, cleanup, target} {
		exists, checkErr := existingPrivateDirectory(path)
		if checkErr != nil {
			return "", checkErr
		}
		if !exists {
			return "", os.ErrNotExist
		}
	}
	return target, nil
}

func cleanupCurrentLocked(ctx context.Context, hotRoot string, source HOTSource, partition state.DreamPartition, partitionRoot string, now time.Time, retention time.Duration, limit int, result *CurrentCleanupResult) error {
	metadata, err := loadCurrentPartition(partitionRoot, targetCoordinationKey(partition), source, partition)
	if err != nil {
		return fmt.Errorf("load current cleanup partition: %w", err)
	}
	intentRoot, err := cleanupIntentRoot(hotRoot, partition, true)
	if err != nil {
		return err
	}
	currentDigest, err := currentDigest(partitionRoot)
	if err != nil {
		return err
	}
	if err = recoverCleanupIntents(ctx, intentRoot, partitionRoot, &metadata, currentDigest, now, result); err != nil {
		return fmt.Errorf("recover current cleanup intents: %w", err)
	}
	versions := filepath.Join(partitionRoot, "versions")
	if validatePrivateDir(versions) != nil {
		return errors.New("HOT current versions directory is unsafe")
	}
	dir, err := os.Open(versions)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(currentCleanupEntries + 1)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > currentCleanupEntries {
		return errors.New("HOT current version scan limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	used := 0
	for _, entry := range entries {
		if used >= limit {
			break
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		digest := entry.Name()
		if !validSnapshotDigest(digest) || digest == currentDigest {
			continue
		}
		path := filepath.Join(versions, digest)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			result.Skipped[digest] = statErr.Error()
			continue
		}
		if now.Sub(info.ModTime()) < retention {
			continue
		}
		intent, makeErr := buildCleanupIntent(path, hotRoot, source, partition, digest, metadata.Manifests[digest], now)
		if makeErr != nil {
			result.Skipped[digest] = makeErr.Error()
			continue
		}
		if writeErr := writeAtomicJSON(intentRoot, intent.ID+".json", intent); writeErr != nil {
			return fmt.Errorf("persist current cleanup intent: %w", writeErr)
		}
		used++
		if finishErr := finishCurrentCleanup(ctx, intentRoot, partitionRoot, &metadata, &intent, currentDigest, now); finishErr != nil {
			result.Skipped[digest] = finishErr.Error()
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if intent.Status == "completed" {
			result.Completed = append(result.Completed, intent.ID)
		} else {
			result.Skipped[digest] = intent.Reason
		}
	}
	return nil
}

func currentDigest(root string) (string, error) {
	target, err := os.Readlink(filepath.Join(root, "current"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !validCurrentTarget(target) {
		return "", errors.New("HOT current link is unsafe")
	}
	return strings.TrimPrefix(filepath.ToSlash(target), "versions/"), nil
}

func buildCleanupIntent(path, hotRoot string, source HOTSource, partition state.DreamPartition, digest, manifestSum string, now time.Time) (currentCleanupIntent, error) {
	var out currentCleanupIntent
	if !validSnapshotDigest(manifestSum) {
		return out, errors.New("version has no verified manifest index")
	}
	bundle, manifest, err := readCleanupSnapshot(path, partition, digest)
	if err != nil {
		return out, err
	}
	_ = bundle
	if fmt.Sprintf("%x", sha256.Sum256(manifest)) != manifestSum {
		return out, errors.New("version manifest index mismatch")
	}
	info, err := os.Lstat(path)
	st, ok := infoSys(info)
	if err != nil || !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || int(st.Uid) != os.Geteuid() {
		return out, errors.New("unsafe version directory identity")
	}
	files := make([]cleanupFile, 0, 4)
	for _, name := range []string{"MEMORY.md", "TASKS.md", "USER.md", "manifest.json"} {
		body, readErr := readPrivateRegular(filepath.Join(path, name), 8<<20)
		if readErr != nil {
			return out, readErr
		}
		files = append(files, cleanupFile{Name: name, Size: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body))})
	}
	id := hashStrings(targetCoordinationKey(partition), digest, fmt.Sprint(st.Dev), fmt.Sprint(st.Ino))[:32]
	return currentCleanupIntent{Version: currentCleanupVersion, ID: id, TargetKey: targetCoordinationKey(partition), StateHome: source.StateHome, ConfigID: source.ConfigID, HotRoot: hotRoot, Partition: partition, Digest: digest, ManifestSHA256: manifestSum, Device: uint64(st.Dev), Inode: st.Ino, Status: "planned", Reason: "derived HOT version retention elapsed", PlannedAt: now, Files: files}, nil
}

func recoverCleanupIntents(ctx context.Context, intentRoot, partitionRoot string, metadata *currentPartition, protected string, now time.Time, result *CurrentCleanupResult) error {
	dir, err := os.Open(intentRoot)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(currentCleanupEntries + 1)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > currentCleanupEntries {
		return errors.New("HOT cleanup intent limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			return errors.New("unexpected HOT cleanup intent entry")
		}
		var intent currentCleanupIntent
		raw, readErr := readPrivateRegular(filepath.Join(intentRoot, entry.Name()), currentCleanupLimit)
		if readErr != nil || strictJSON(raw, &intent) != nil || entry.Name() != intent.ID+".json" || validateCleanupIntent(intent, partitionRoot, metadata) != nil {
			return errors.New("invalid HOT cleanup intent")
		}
		if intent.Status == "completed" || intent.Status == "skipped" {
			continue
		}
		if finishErr := finishCurrentCleanup(ctx, intentRoot, partitionRoot, metadata, &intent, protected, now); finishErr != nil {
			return finishErr
		}
		if result != nil && intent.Status == "completed" {
			result.Completed = append(result.Completed, intent.ID)
		}
	}
	return nil
}

func validateCleanupIntent(i currentCleanupIntent, partitionRoot string, metadata *currentPartition) error {
	wantID := hashStrings(targetCoordinationKey(i.Partition), i.Digest, fmt.Sprint(i.Device), fmt.Sprint(i.Inode))[:32]
	if i.Version != currentCleanupVersion || len(i.ID) != 32 || !validHex(i.ID) || i.ID != wantID || i.TargetKey != metadata.ID || i.StateHome != metadata.Source.StateHome || i.ConfigID != metadata.Source.ConfigID || i.Partition != metadata.Partition || i.HotRoot != filepath.Dir(partitionRoot) || !validSnapshotDigest(i.Digest) || !validSnapshotDigest(i.ManifestSHA256) || len(i.Files) != 4 || i.PlannedAt.IsZero() {
		return errors.New("cleanup intent identity mismatch")
	}
	if i.Status != "planned" && i.Status != "quarantined" && i.Status != "completed" && i.Status != "skipped" {
		return errors.New("cleanup intent status invalid")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range i.Files {
		if seen[f.Name] || (f.Name != "USER.md" && f.Name != "MEMORY.md" && f.Name != "TASKS.md" && f.Name != "manifest.json") || f.Size < 0 || f.Size > 8<<20 || !validSnapshotDigest(f.SHA256) {
			return errors.New("cleanup intent files invalid")
		}
		total += f.Size
		seen[f.Name] = true
	}
	if total > 32<<20 || fileDigest(i.Files, "manifest.json") != i.ManifestSHA256 {
		return errors.New("cleanup intent manifest or byte budget is invalid")
	}
	return nil
}

func validHex(value string) bool {
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func fileDigest(files []cleanupFile, name string) string {
	for _, file := range files {
		if file.Name == name {
			return file.SHA256
		}
	}
	return ""
}

func finishCurrentCleanup(ctx context.Context, intentRoot, partitionRoot string, metadata *currentPartition, intent *currentCleanupIntent, protected string, now time.Time) error {
	versions := filepath.Join(partitionRoot, "versions")
	source := filepath.Join(versions, intent.Digest)
	quarantine := filepath.Join(partitionRoot, ".retired-"+intent.ID)
	if intent.Status == "planned" {
		if intent.Digest == protected {
			intent.Status = "skipped"
			intent.Reason = "current version is protected"
			return writeAtomicJSON(intentRoot, intent.ID+".json", intent)
		}
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			if quarantineInfo, quarantineErr := os.Lstat(quarantine); quarantineErr == nil {
				qstat, ok := infoSys(quarantineInfo)
				if !ok || !quarantineInfo.IsDir() || qstat.Ino != intent.Inode || uint64(qstat.Dev) != intent.Device {
					return errors.New("cleanup quarantine incarnation changed")
				}
				when := now
				intent.Status, intent.QuarantinedAt = "quarantined", &when
				if err = writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
					return fmt.Errorf("quarantine exists but intent recovery is uncertain: %w", err)
				}
			} else if !errors.Is(quarantineErr, os.ErrNotExist) {
				return quarantineErr
			} else {
				intent.Status = "skipped"
				intent.Reason = "version disappeared before quarantine"
				return writeAtomicJSON(intentRoot, intent.ID+".json", intent)
			}
		}
		if intent.Status == "planned" {
			st, ok := infoSys(info)
			if err != nil || !ok || st.Ino != intent.Inode || uint64(st.Dev) != intent.Device {
				intent.Status = "skipped"
				intent.Reason = "version incarnation changed"
				return writeAtomicJSON(intentRoot, intent.ID+".json", intent)
			}
			if err = verifyIntentDirectoryIdentity(source, *intent); err != nil {
				return err
			}
			if err = verifyIntentFiles(source, *intent, false); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = os.Rename(source, quarantine); err != nil {
				return err
			}
			if err = errors.Join(syncDirectory(versions), syncDirectory(partitionRoot)); err != nil {
				return fmt.Errorf("version quarantine committed but sync is uncertain: %w", err)
			}
			when := now
			intent.Status = "quarantined"
			intent.QuarantinedAt = &when
			if err = writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
				return fmt.Errorf("version is quarantined but intent update is uncertain: %w", err)
			}
		}
	}
	if intent.Status != "quarantined" {
		return nil
	}
	if err := verifyIntentDirectoryIdentity(quarantine, *intent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, sourceErr := os.Lstat(source); sourceErr == nil {
				intent.Status = "skipped"
				intent.Reason = "version was republished with a new incarnation"
				return writeAtomicJSON(intentRoot, intent.ID+".json", intent)
			} else if errors.Is(sourceErr, os.ErrNotExist) {
				intent.Status = "skipped"
				intent.Reason = "cleanup source and quarantine are absent; prior deletion outcome is unknown"
				return writeAtomicJSON(intentRoot, intent.ID+".json", intent)
			}
		}
		return err
	}
	if err := verifyIntentFiles(quarantine, *intent, true); err != nil {
		return err
	}
	for _, file := range intent.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyIntentDirectoryIdentity(quarantine, *intent); err != nil {
			return err
		}
		path := filepath.Join(quarantine, file.Name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := verifyIntentFile(path, file); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if err := syncDirectory(quarantine); err != nil {
		return fmt.Errorf("cleanup file removal sync is uncertain: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(quarantine); err != nil {
		return err
	}
	if err := syncDirectory(partitionRoot); err != nil {
		return fmt.Errorf("version files removed but directory sync is uncertain: %w", err)
	}
	// Never remove an index for a same-digest incarnation that was republished.
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		delete(metadata.Manifests, intent.Digest)
		if err = writeCurrentPartition(partitionRoot, *metadata); err != nil {
			return fmt.Errorf("version removed but manifest index update is uncertain: %w", err)
		}
	}
	when := now
	intent.Status = "completed"
	intent.CompletedAt = &when
	if err := writeAtomicJSON(intentRoot, intent.ID+".json", intent); err != nil {
		return fmt.Errorf("version removal committed but completion status is uncertain: %w", err)
	}
	return nil
}

func verifyIntentDirectoryIdentity(path string, intent currentCleanupIntent) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := infoSys(info)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || int(st.Uid) != os.Geteuid() || st.Ino != intent.Inode || uint64(st.Dev) != intent.Device {
		return errors.New("cleanup quarantine identity changed")
	}
	return nil
}

func verifyIntentFiles(root string, intent currentCleanupIntent, missingAllowed bool) error {
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(5)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 4 {
		return errors.New("cleanup version contains unexpected files")
	}
	want := map[string]cleanupFile{}
	for _, f := range intent.Files {
		want[f.Name] = f
	}
	for _, entry := range entries {
		f, ok := want[entry.Name()]
		if !ok {
			return errors.New("cleanup version contains unexpected files")
		}
		if readErr := verifyIntentFile(filepath.Join(root, f.Name), f); readErr != nil {
			return errors.New("cleanup version file changed")
		}
	}
	if !missingAllowed && len(entries) != len(want) {
		return errors.New("cleanup version files are incomplete")
	}
	return nil
}

func verifyIntentFile(path string, file cleanupFile) error {
	body, err := readPrivateRegular(path, 8<<20)
	if err != nil || int64(len(body)) != file.Size || fmt.Sprintf("%x", sha256.Sum256(body)) != file.SHA256 {
		return errors.New("cleanup version file changed")
	}
	return nil
}

// recoverCurrentCleanupBeforePublish is called while the partition EX lock is
// held. It never creates cleanup state merely because a version is published.
func recoverCurrentCleanupBeforePublish(ctx context.Context, hotRoot string, partitionRoot string, metadata *currentPartition, protected string) error {
	root, err := cleanupIntentRoot(hotRoot, metadata.Partition, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return recoverCleanupIntents(ctx, root, partitionRoot, metadata, protected, time.Now().UTC(), nil)
}
