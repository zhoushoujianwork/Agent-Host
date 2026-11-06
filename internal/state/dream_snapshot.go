package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	dreamSnapshotMarkerLimit     = 64 << 10
	dreamSnapshotManifestLimit   = 8 << 20
	dreamSnapshotFileLimit       = 8 << 20
	dreamSnapshotDomainLimit     = 10000
	dreamSnapshotDomainFileLimit = 64 << 10
	dreamSnapshotTotalLimit      = 32 << 20
	dreamSnapshotEntryLimit      = 10000
)

// ReadDreamMemoriesSnapshot reads up to limit ID-ordered accepted Dream memories
// without opening the writable file-memory backend, taking its lock, or creating
// its cache index. Callers choose an explicit bound from 1 through 200.
func ReadDreamMemoriesSnapshot(ctx context.Context, home string, p DreamPartition, now time.Time, limit int) ([]Memory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validDreamPartition(p) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid bounded dream memory snapshot query")
	}
	canonical, err := snapshotPrivateDirectory(home)
	if err != nil {
		return nil, fmt.Errorf("dream memory state home: %w", err)
	}
	marker := FileMemoryMarker(canonical)
	if _, err = os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		return readDreamSQLiteSnapshot(ctx, canonical, p, now, limit)
	} else if err != nil {
		return nil, err
	}
	var config FileMemoryConfig
	if _, err = snapshotJSONFile(marker, dreamSnapshotMarkerLimit, &config); err != nil {
		return nil, fmt.Errorf("dream file-memory marker: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = validateFileConfig(config, canonical); err != nil {
		return nil, err
	}
	return readDreamFileSnapshot(ctx, config, p, now, limit)
}

func readDreamSQLiteSnapshot(ctx context.Context, home string, p DreamPartition, now time.Time, limit int) ([]Memory, error) {
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		info, err := os.Lstat(filepath.Join(home, name))
		if name != "state.db" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		stat, ok := snapshotFileStat(info)
		if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
			return nil, fmt.Errorf("unsafe dream SQLite source file: %s", name)
		}
	}
	store, err := OpenForHistoryInspection(home)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	if _, err = store.db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return nil, err
	}
	memories, err := store.WithPartition(p.AgentID, p.AudienceID).DreamMemories(ctx, p, now, limit)
	if err != nil {
		return nil, err
	}
	for _, memory := range memories {
		if memory.AgentID != p.AgentID || memory.AudienceID != p.AudienceID || memory.SenderID != p.PersonID || memory.Scope != p.Scope || memory.Status != "active" || memory.SourceType != "dream_reviewed" || memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
			return nil, errors.New("dream SQLite payload does not match its authorized partition")
		}
	}
	return memories, nil
}

func readDreamFileSnapshot(ctx context.Context, config FileMemoryConfig, p DreamPartition, now time.Time, limit int) ([]Memory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	memoryRoot, err := snapshotPrivateDirectory(config.MemoryRoot)
	if err != nil || memoryRoot != config.MemoryRoot {
		return nil, errors.New("dream file-memory root is unsafe")
	}
	f := &fileMemory{config: config, sourceDir: filepath.Join(config.MemoryRoot, "sources", sourceKey(config.SourceID))}
	if _, err = snapshotPrivateDirectory(f.sourceDir); err != nil {
		return nil, fmt.Errorf("dream file-memory source: %w", err)
	}
	manifestPath := filepath.Join(f.sourceDir, "current.json")
	var manifest fileManifest
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	before, err := snapshotJSONFile(manifestPath, dreamSnapshotManifestLimit, &manifest)
	if err != nil {
		return nil, fmt.Errorf("read authoritative memory manifest: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if manifest.Version != 1 || manifest.SourceID != config.SourceID || manifest.StateHome != config.StateHome || manifest.Generation == 0 {
		return nil, errors.New("invalid authoritative memory manifest identity")
	}
	if len(manifest.Domains) > dreamSnapshotDomainLimit || len(manifest.Entries) > dreamSnapshotEntryLimit {
		return nil, errors.New("dream file-memory manifest exceeds domain or entry limit")
	}
	domains := make(map[string]fileDomain, len(manifest.Domains))
	selectedDomains := map[string]bool{}
	var totalBytes int64
	for _, domain := range manifest.Domains {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		expected := fileDomainFor(config, Memory{Scope: domain.Scope, AgentID: domain.AgentID, AudienceID: domain.AudienceID})
		if domain != expected || domains[domain.ID].ID != "" {
			return nil, errors.New("invalid or duplicate memory domain identity")
		}
		var actual fileDomain
		path := filepath.Join(config.MemoryRoot, "domains", domain.ID, "scope.json")
		var domainRaw []byte
		if domainRaw, err = snapshotJSONFile(path, dreamSnapshotDomainFileLimit, &actual); err != nil || actual != domain {
			return nil, errors.New("memory domain mapping changed outside migration")
		}
		if err = addDreamSnapshotBytes(&totalBytes, len(domainRaw)); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		domains[domain.ID] = domain
		selectedDomains[domain.ID] = samePartition(domain.AgentID, domain.AudienceID, p.AgentID, p.AudienceID) && domain.Scope == p.Scope
	}
	seen := map[string]bool{}
	var selected []Memory
	for _, entry := range manifest.Entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !validFileID(entry.ID) || seen[entry.ID] || entry.Current < 1 || entry.Order < 1 || entry.Directory != "entries" && entry.Directory != "skills" {
			return nil, errors.New("invalid memory manifest entry")
		}
		seen[entry.ID] = true
		domain, ok := domains[entry.Domain]
		if !ok {
			return nil, errors.New("memory entry has an unauthorized domain")
		}
		var current *fileReference
		revisions := map[int]bool{}
		for index := range entry.Revisions {
			ref := &entry.Revisions[index]
			if ref.Revision < 1 || revisions[ref.Revision] || !snapshotHexDigest(ref.RecordSHA256) || !snapshotHexDigest(ref.ContentSHA256) {
				return nil, errors.New("invalid or duplicate memory revision reference")
			}
			revisions[ref.Revision] = true
			if ref.Revision == entry.Current {
				current = ref
			}
		}
		if current == nil {
			return nil, fmt.Errorf("memory %s has no current revision", entry.ID)
		}
		if !sort.StringsAreSorted(entry.EvidenceRuns) {
			return nil, errors.New("memory evidence references are not canonical")
		}
		if !selectedDomains[domain.ID] {
			continue
		}
		base := f.revisionPath(entry, current.Revision)
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		recordRaw, err := snapshotReadFile(filepath.Join(base, "record.json"), dreamSnapshotFileLimit)
		if err != nil {
			return nil, fmt.Errorf("read memory %s current revision: %w", entry.ID, err)
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		content, err := snapshotReadFile(filepath.Join(base, memoryContentName(entry.Directory)), dreamSnapshotFileLimit)
		if err != nil {
			return nil, err
		}
		if err = addDreamSnapshotBytes(&totalBytes, len(recordRaw)+len(content)); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if fileHash(recordRaw) != current.RecordSHA256 || fileHash(content) != current.ContentSHA256 {
			return nil, fmt.Errorf("memory %s current revision checksum mismatch", entry.ID)
		}
		var record fileRecord
		if err = decodeSnapshotJSON(recordRaw, &record); err != nil {
			return nil, err
		}
		memory := record.Memory
		if record.Version != 1 || record.SourceID != config.SourceID || record.StateHome != config.StateHome || memory.ID != entry.ID || memory.Revision != current.Revision || fileDomainFor(config, memory) != domain || memoryDirectory(memory) != entry.Directory || !bytes.Equal(content, memoryDocument(memory)) {
			return nil, errors.New("memory current revision attribution does not match publication")
		}
		if memory.SenderID == p.PersonID && memory.Status == "active" && (memory.ExpiresAt == nil || memory.ExpiresAt.After(now)) && memory.SourceType == "dream_reviewed" {
			selected = append(selected, memory)
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	after, err := snapshotReadFile(manifestPath, dreamSnapshotManifestLimit)
	if err != nil || !bytes.Equal(before, after) {
		return nil, errors.New("dream file-memory manifest changed during snapshot read")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	if len(selected) > limit {
		selected = selected[:limit]
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return selected, nil
}

func addDreamSnapshotBytes(total *int64, size int) error {
	if size < 0 || *total > dreamSnapshotTotalLimit-int64(size) {
		return errors.New("dream file-memory snapshot exceeds total read limit")
	}
	*total += int64(size)
	return nil
}

func snapshotPrivateDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be absolute and clean")
	}
	actual, err := filepath.EvalSymlinks(path)
	info, statErr := os.Lstat(path)
	stat, ok := snapshotFileStat(info)
	if err != nil || statErr != nil || actual != path || !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("path must be canonical and owner-private")
	}
	return actual, nil
}

func snapshotJSONFile(path string, limit int64, target any) ([]byte, error) {
	raw, err := snapshotReadFile(path, limit)
	if err != nil {
		return nil, err
	}
	if err = decodeSnapshotJSON(raw, target); err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeSnapshotJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("snapshot JSON must contain one value")
	}
	return nil
}

func snapshotReadFile(path string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("invalid snapshot file limit")
	}
	parent, err := snapshotPrivateDirectory(filepath.Dir(path))
	if err != nil || parent != filepath.Dir(path) {
		return nil, errors.New("snapshot file parent is unsafe")
	}
	before, err := os.Lstat(path)
	beforeStat, ok := snapshotFileStat(before)
	if err != nil || !ok || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || int(beforeStat.Uid) != os.Geteuid() || beforeStat.Nlink != 1 || before.Size() > limit {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("snapshot file is unsafe or exceeds its size limit")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	afterStat, afterOK := snapshotFileStat(after)
	if err != nil || !afterOK || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 || int(afterStat.Uid) != os.Geteuid() || afterStat.Nlink != 1 || beforeStat.Dev != afterStat.Dev || beforeStat.Ino != afterStat.Ino {
		return nil, errors.New("snapshot file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("snapshot file exceeds its size limit")
	}
	return raw, nil
}

func snapshotHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func snapshotFileStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
