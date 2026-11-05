package dream

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-host/internal/state"
)

type CleanupResult struct {
	Completed []string          `json:"completed"`
	Skipped   map[string]string `json:"skipped"`
}

// CleanupHOT removes only verified derived snapshots. Raw evidence and managed
// skills are outside this root. Current documents are cloned into native runs.
func CleanupHOT(ctx context.Context, store *state.Store, cacheRoot string, current HOTBundle, now time.Time, retention time.Duration, limit int) (CleanupResult, error) {
	result := CleanupResult{Skipped: map[string]string{}}
	if retention <= 0 || limit < 1 || limit > 200 {
		return result, errors.New("cleanup requires positive retention and limit 1..200")
	}
	currentPath, err := PublishHOT(cacheRoot, current)
	if err != nil {
		return result, err
	}
	root := filepath.Dir(currentPath)
	release, err := state.Acquire(root)
	if err != nil {
		return result, err
	}
	defer release()
	records, err := store.DreamCleanupRecords(ctx, current.Partition, "planned", 200)
	if err != nil {
		return result, err
	}
	used := 0
	for _, record := range records {
		if used >= limit {
			break
		}
		if record.Digest == current.Digest {
			result.Skipped[record.Path] = "current snapshot"
			continue
		}
		if err = finishCleanup(ctx, store, root, current.Partition, record, now); err != nil {
			result.Skipped[record.Path] = err.Error()
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
		} else {
			used++
			result.Completed = append(result.Completed, record.ID)
		}
	}
	// Directory inspection is bounded; reaching the bound is explicit, never a
	// reason to broaden cleanup to arbitrary paths.
	dir, err := os.Open(root)
	if err != nil {
		return result, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxRootEntries + 1)
	if err != nil && len(entries) == 0 {
		return result, err
	}
	if len(entries) > maxRootEntries {
		return result, errors.New("HOT cleanup directory budget exceeded")
	}
	for _, entry := range entries {
		if used >= limit {
			break
		}
		name := entry.Name()
		if !validSnapshotDigest(name) || name == current.Digest {
			continue
		}
		path := filepath.Join(root, name)
		if result.Skipped[path] != "" {
			continue
		}
		info, e := os.Lstat(path)
		if e != nil {
			result.Skipped[path] = e.Error()
			continue
		}
		if now.Sub(info.ModTime()) < retention {
			continue
		}
		bundle, manifest, e := readCleanupSnapshot(path, current.Partition, name)
		if e != nil {
			result.Skipped[path] = e.Error()
			continue
		}
		_ = bundle
		stat, ok := infoSys(info)
		if !ok {
			result.Skipped[path] = "unsupported file identity"
			continue
		}
		record := state.DreamCleanup{ID: "dream-cleanup-" + hashStrings(current.Partition.String(), path, name, fmt.Sprint(stat.Dev), fmt.Sprint(stat.Ino))[:24], Partition: current.Partition, Path: path, Digest: name, Status: "planned", Reason: "derived HOT snapshot retention elapsed", Manifest: manifest, Inode: stat.Ino, Device: uint64(stat.Dev), PlannedAt: now.UTC()}
		if err = store.SaveDreamCleanup(ctx, record); err != nil {
			return result, err
		}
		used++
		if err = finishCleanup(ctx, store, root, current.Partition, record, now); err != nil {
			result.Skipped[path] = err.Error()
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
		} else {
			result.Completed = append(result.Completed, record.ID)
		}
	}
	return result, nil
}

func validSnapshotDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32
}

func readCleanupSnapshot(path string, p state.DreamPartition, digest string) (HOTBundle, []byte, error) {
	var bundle HOTBundle
	if err := validatePrivateDir(path); err != nil {
		return bundle, nil, err
	}
	info, err := os.Lstat(filepath.Join(path, "manifest.json"))
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Size() > 8<<20 || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return bundle, nil, errors.New("unsafe cleanup manifest")
	}
	raw, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return bundle, nil, err
	}
	if err = json.Unmarshal(raw, &bundle); err != nil {
		return bundle, nil, err
	}
	if bundle.Partition != p || bundle.Digest != digest || hotBundleDigest(bundle) != digest {
		return bundle, nil, errors.New("cleanup manifest identity mismatch")
	}
	canonical, _ := json.MarshalIndent(bundle, "", "  ")
	canonical = append(canonical, '\n')
	if !bytes.Equal(raw, canonical) {
		return bundle, nil, errors.New("noncanonical cleanup manifest")
	}
	return bundle, canonical, verifyHOTSnapshot(path, bundle, canonical)
}

func finishCleanup(ctx context.Context, store *state.Store, root string, p state.DreamPartition, r state.DreamCleanup, now time.Time) error {
	if r.Partition != p || !validSnapshotDigest(r.Digest) || r.Path != filepath.Join(root, r.Digest) {
		return errors.New("cleanup intent path mismatch")
	}
	var bundle HOTBundle
	if err := json.Unmarshal(r.Manifest, &bundle); err != nil {
		return err
	}
	if bundle.Partition != p || bundle.Digest != r.Digest || hotBundleDigest(bundle) != r.Digest {
		return errors.New("cleanup intent manifest mismatch")
	}
	manifest, _ := json.MarshalIndent(bundle, "", "  ")
	manifest = append(manifest, '\n')
	quarantine := filepath.Join(root, ".retire-"+r.Digest)
	info, err := os.Lstat(quarantine)
	if errors.Is(err, os.ErrNotExist) {
		info, err = os.Lstat(r.Path)
		if err == nil {
			if !sameCleanupDirectory(info, r) {
				return errors.New("cleanup source replaced")
			}
			if err = verifyHOTSnapshot(r.Path, bundle, manifest); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = os.Rename(r.Path, quarantine); err != nil {
				return err
			}
			info, err = os.Lstat(quarantine)
		} else if errors.Is(err, os.ErrNotExist) {
			return completeCleanup(ctx, store, r, now)
		}
	}
	if err != nil {
		return err
	}
	if !sameCleanupDirectory(info, r) {
		return errors.New("cleanup quarantine replaced")
	}
	canonical, err := filepath.EvalSymlinks(quarantine)
	if err != nil || canonical != quarantine {
		return errors.New("unsafe cleanup quarantine path")
	}
	expected := map[string][]byte{"USER.md": []byte(bundle.Documents["USER.md"]), "MEMORY.md": []byte(bundle.Documents["MEMORY.md"]), "TASKS.md": []byte(bundle.Documents["TASKS.md"]), "manifest.json": manifest}
	entries, err := os.ReadDir(quarantine)
	if err != nil {
		return err
	}
	// Validate all remaining files before deleting any. Already removed files are
	// allowed only here, after matching the durable intent's directory identity.
	for _, entry := range entries {
		want, known := expected[entry.Name()]
		if !known {
			return errors.New("unexpected file in cleanup quarantine")
		}
		path := filepath.Join(quarantine, entry.Name())
		info, e := os.Lstat(path)
		stat, ok := infoSys(info)
		if e != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || info.Size() != int64(len(want)) {
			return errors.New("unsafe cleanup file")
		}
		body, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(body, want) {
			return errors.New("modified cleanup file")
		}
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(quarantine, entry.Name())); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Remove(quarantine); err != nil {
		return err
	}
	return completeCleanup(ctx, store, r, now)
}

func sameCleanupDirectory(info os.FileInfo, r state.DreamCleanup) bool {
	stat, ok := infoSys(info)
	return ok && info.IsDir() && info.Mode().Perm()&0077 == 0 && int(stat.Uid) == os.Geteuid() && stat.Ino == r.Inode && uint64(stat.Dev) == r.Device
}
func completeCleanup(ctx context.Context, store *state.Store, r state.DreamCleanup, now time.Time) error {
	r.Status = "completed"
	now = now.UTC()
	r.CompletedAt = &now
	return store.SaveDreamCleanup(ctx, r)
}
