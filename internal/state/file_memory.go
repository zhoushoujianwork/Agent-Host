package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FileMemoryConfig is a migration receipt, kept beside the operational state.
// It is deliberately separate from user-maintained host configuration. Opening
// a migrated state home always selects files, including read-only inspectors.
type FileMemoryConfig struct {
	Version    int    `json:"version"`
	MemoryRoot string `json:"memory_root"`
	CacheRoot  string `json:"cache_root"`
	SourceID   string `json:"source_id"`
	StateHome  string `json:"state_home"`
}

type FileMemoryReport struct {
	SourceID   string `json:"source_id"`
	Generation uint64 `json:"generation"`
	Domains    int    `json:"domains"`
	Memories   int    `json:"memories"`
	Revisions  int    `json:"revisions"`
	Skills     int    `json:"skills"`
	Evidence   int    `json:"evidence"`
	Digest     string `json:"digest"`
	IndexPath  string `json:"index_path"`
}

type fileMemory struct {
	config    FileMemoryConfig
	sourceDir string
	mu        sync.Mutex
	manifest  fileManifest
}

type fileDomain struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`
	SourceID   string `json:"source_id"`
	Scope      string `json:"scope"`
	AgentID    string `json:"agent_id"`
	AudienceID string `json:"audience_id"`
	StateHome  string `json:"state_home"`
}

type fileReference struct {
	Revision      int    `json:"revision"`
	RecordSHA256  string `json:"record_sha256"`
	ContentSHA256 string `json:"content_sha256"`
}

type fileEntry struct {
	ID           string          `json:"id"`
	Domain       string          `json:"domain"`
	Directory    string          `json:"directory"`
	Current      int             `json:"current"`
	Order        int64           `json:"order"`
	Revisions    []fileReference `json:"revisions"`
	EvidenceRuns []string        `json:"evidence_runs"`
}

type fileOperation struct {
	Table  string          `json:"table"`
	ID     string          `json:"id"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type fileManifest struct {
	Version    int             `json:"version"`
	SourceID   string          `json:"source_id"`
	StateHome  string          `json:"state_home"`
	Generation uint64          `json:"generation"`
	Domains    []fileDomain    `json:"domains"`
	Entries    []fileEntry     `json:"entries"`
	Operations []fileOperation `json:"operations,omitempty"`
}

// record.json retains the complete existing attribution model. Content is also
// stored in a readable document; both are hash-checked before every memory
// transaction, so stale index text can never conceal an edited/missing file.
type fileRecord struct {
	Version   int    `json:"version"`
	SourceID  string `json:"source_id"`
	StateHome string `json:"state_home"`
	Memory    Memory `json:"memory"`
}

type fileSnapshot struct {
	manifest  fileManifest
	current   []Memory
	revisions []Memory
	evidence  map[string][]string
	order     map[string]int64
}

const fileIndexSchema = `
CREATE TABLE IF NOT EXISTS memories(id TEXT PRIMARY KEY,agent_id TEXT NOT NULL,audience_id TEXT NOT NULL,sender_id TEXT NOT NULL,scope TEXT NOT NULL,text TEXT NOT NULL,status TEXT NOT NULL,expires_at TEXT,payload BLOB NOT NULL,UNIQUE(agent_id,audience_id,scope,sender_id,text));
CREATE INDEX IF NOT EXISTS memories_scope ON memories(agent_id,audience_id,scope,status);
CREATE TABLE IF NOT EXISTS memory_revisions(memory_id TEXT NOT NULL,revision INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(memory_id,revision));
CREATE TABLE IF NOT EXISTS memory_evidence(memory_id TEXT NOT NULL,run_id TEXT NOT NULL,PRIMARY KEY(memory_id,run_id));
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(memory_id UNINDEXED,tokens,tokenize='unicode61');
CREATE TABLE IF NOT EXISTS memory_annotations(memory_id TEXT PRIMARY KEY,version TEXT NOT NULL,payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS memory_terms(memory_id TEXT NOT NULL,term TEXT NOT NULL,PRIMARY KEY(memory_id,term));
CREATE INDEX IF NOT EXISTS memory_terms_term ON memory_terms(term,memory_id);
CREATE TABLE IF NOT EXISTS file_index_checkpoint(id INTEGER PRIMARY KEY CHECK(id=1),generation INTEGER NOT NULL);
CREATE TEMP TABLE IF NOT EXISTS file_operation_log(table_name TEXT NOT NULL,id TEXT NOT NULL,before_payload BLOB NOT NULL,after_payload BLOB NOT NULL);
CREATE TEMP TRIGGER file_runs_update AFTER UPDATE ON runtime.runs BEGIN INSERT INTO file_operation_log VALUES('runs',NEW.id,OLD.payload,NEW.payload); END;
CREATE TEMP TRIGGER file_candidates_update AFTER UPDATE ON runtime.candidates BEGIN INSERT INTO file_operation_log VALUES('candidates',NEW.id,OLD.payload,NEW.payload); END;
`

func FileMemoryMarker(home string) string { return filepath.Join(home, "file-memory.json") }

func (s *Store) FileMemoryEnabled() bool { return s.files != nil }

func fileHash(b []byte) string   { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func sourceKey(id string) string { return fileHash([]byte(id))[:24] }

func physicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("file-memory root is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for p := abs; ; p = filepath.Dir(p) {
		if info, err := os.Lstat(p); err == nil {
			if len(tail) == 0 && info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("file-memory root cannot be a symbolic link")
			}
			base, err := filepath.EvalSymlinks(p)
			if err != nil {
				return "", err
			}
			for i := len(tail) - 1; i >= 0; i-- {
				base = filepath.Join(base, tail[i])
			}
			return base, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		tail = append(tail, filepath.Base(p))
	}
}

func fileDomainFor(c FileMemoryConfig, m Memory) fileDomain {
	a, b := normalizedPartition(m.AgentID, m.AudienceID)
	identity, _ := json.Marshal([]string{c.SourceID, m.Scope, a, b})
	return fileDomain{Version: 1, ID: fileHash(identity)[:32], SourceID: c.SourceID, Scope: m.Scope, AgentID: a, AudienceID: b, StateHome: c.StateHome}
}

func validateFileConfig(c FileMemoryConfig, home string) error {
	if c.Version != 1 || strings.TrimSpace(c.SourceID) == "" || len(c.SourceID) > 200 || !filepath.IsAbs(c.MemoryRoot) || !filepath.IsAbs(c.CacheRoot) || c.StateHome != home {
		return errors.New("invalid file-memory migration receipt or state-home identity")
	}
	if c.MemoryRoot == c.CacheRoot || withinFileRoot(c.MemoryRoot, c.CacheRoot) || withinFileRoot(c.CacheRoot, c.MemoryRoot) {
		return errors.New("memory and cache roots must be separate directories")
	}
	return nil
}

func withinFileRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readPrivateFile(path string) ([]byte, error) {
	if err := checkDirectoryPath(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file-memory path is not a regular file: %s", path)
	}
	return os.ReadFile(path)
}

func decodePrivateJSON(path string, target any) error {
	b, err := readPrivateFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if d.More() {
		return fmt.Errorf("trailing JSON in %s", path)
	}
	return nil
}

// atomicFile syncs content and its parent before reporting durable publication.
// Existing revision files are immutable: retries must contain identical bytes.
func atomicFile(path string, b []byte, immutable bool) error {
	if err := privateDirectories(filepath.Dir(path)); err != nil {
		return err
	}
	if immutable {
		old, err := readPrivateFile(path)
		if err == nil {
			if bytes.Equal(old, b) {
				return nil
			}
			return fmt.Errorf("immutable memory revision differs: %s", path)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".publish-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = errors.Join(f.Chmod(0600), writeAndSync(f, b), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func writeAndSync(f *os.File, b []byte) error {
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func privateDirectories(path string) error {
	var created []string
	for p := path; ; p = filepath.Dir(p) {
		_, err := os.Lstat(p)
		if err == nil {
			if err = checkDirectoryPath(p); err != nil {
				return err
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		created = append(created, p)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	// Sync each newly created directory's entry in its parent, including the
	// first existing ancestor. Syncing only the final file's parent cannot
	// guarantee that a new revision directory survives a crash.
	for _, p := range created {
		d, err := os.Open(filepath.Dir(p))
		if err != nil {
			return err
		}
		if err = errors.Join(d.Sync(), d.Close()); err != nil {
			return err
		}
	}
	return checkDirectoryPath(path)
}

func checkDirectoryPath(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file-memory directory is unsafe: %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

func atomicJSON(path string, v any, immutable bool) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(path, append(b, '\n'), immutable)
}

func (f *fileMemory) lock(ctx context.Context) (func() error, error) {
	f.mu.Lock()
	if err := privateDirectories(f.sourceDir); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(f.sourceDir, "publish.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			lock.Close()
			f.mu.Unlock()
			return nil, err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			f.mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() error {
		defer f.mu.Unlock()
		return errors.Join(syscall.Flock(int(lock.Fd()), syscall.LOCK_UN), lock.Close())
	}, nil
}

func skillDocument(m Memory) []byte {
	name := "learned-" + sourceKey(m.ID)
	description := strings.TrimSpace(m.Conditions)
	if description == "" {
		description = "Reviewed problem-solving experience: " + strings.Split(m.Text, "\n")[0]
	}
	if len([]rune(description)) > 500 {
		description = string([]rune(description)[:500])
	}
	quoted, _ := json.Marshal(description)
	return []byte("---\nname: " + name + "\ndescription: " + string(quoted) + "\n---\n\n" + m.Text + "\n")
}

func memoryDocument(m Memory) []byte {
	if m.Kind == "procedure" && m.SourceType == "reviewed_experience" {
		return skillDocument(m)
	}
	return []byte(m.Text + "\n")
}
func memoryDirectory(m Memory) string {
	if m.Kind == "procedure" && m.SourceType == "reviewed_experience" {
		return "skills"
	}
	return "entries"
}
func memoryContentName(dir string) string {
	if dir == "skills" {
		return "SKILL.md"
	}
	return "content.md"
}
func validFileID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 200 && !strings.ContainsAny(id, "/\\\x00")
}

func (f *fileMemory) revisionPath(e fileEntry, rev int) string {
	return filepath.Join(f.config.MemoryRoot, "domains", e.Domain, e.Directory, e.ID, "revisions", strconv.Itoa(rev))
}

func (f *fileMemory) readSnapshot() (fileSnapshot, error) {
	var snap fileSnapshot
	snap.evidence = map[string][]string{}
	snap.order = map[string]int64{}
	var err error
	snap.manifest, err = f.readManifest()
	if err != nil {
		return snap, err
	}
	m := snap.manifest
	domains := map[string]fileDomain{}
	for _, d := range m.Domains {
		expected := fileDomainFor(f.config, Memory{Scope: d.Scope, AgentID: d.AgentID, AudienceID: d.AudienceID})
		if d != expected || domains[d.ID].ID != "" {
			return snap, errors.New("invalid or duplicate memory domain identity")
		}
		var actual fileDomain
		if err := decodePrivateJSON(filepath.Join(f.config.MemoryRoot, "domains", d.ID, "scope.json"), &actual); err != nil {
			return snap, err
		}
		if actual != d {
			return snap, errors.New("memory domain mapping changed outside migration")
		}
		domains[d.ID] = d
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		if !validFileID(e.ID) || seen[e.ID] || e.Current < 1 || e.Order < 1 || (e.Directory != "entries" && e.Directory != "skills") {
			return snap, errors.New("invalid memory manifest entry")
		}
		seen[e.ID] = true
		d, ok := domains[e.Domain]
		if !ok {
			return snap, errors.New("memory entry has an unauthorized domain")
		}
		found := false
		revisions := map[int]bool{}
		for _, ref := range e.Revisions {
			if ref.Revision < 1 || revisions[ref.Revision] {
				return snap, errors.New("invalid or duplicate memory revision reference")
			}
			revisions[ref.Revision] = true
			base := f.revisionPath(e, ref.Revision)
			b, err := readPrivateFile(filepath.Join(base, "record.json"))
			if err != nil {
				return snap, fmt.Errorf("read memory %s revision %d: %w", e.ID, ref.Revision, err)
			}
			if fileHash(b) != ref.RecordSHA256 {
				return snap, fmt.Errorf("memory %s revision %d metadata checksum mismatch", e.ID, ref.Revision)
			}
			var record fileRecord
			if err = json.Unmarshal(b, &record); err != nil {
				return snap, err
			}
			memory := record.Memory
			if record.Version != 1 || record.SourceID != f.config.SourceID || record.StateHome != f.config.StateHome || memory.ID != e.ID || memory.Revision != ref.Revision || fileDomainFor(f.config, memory) != d || memoryDirectory(memory) != e.Directory {
				return snap, errors.New("memory revision attribution does not match publication")
			}
			content, err := readPrivateFile(filepath.Join(base, memoryContentName(e.Directory)))
			if err != nil {
				return snap, err
			}
			if fileHash(content) != ref.ContentSHA256 || !bytes.Equal(content, memoryDocument(memory)) {
				return snap, fmt.Errorf("memory %s revision %d document checksum mismatch", e.ID, ref.Revision)
			}
			snap.revisions = append(snap.revisions, memory)
			if ref.Revision == e.Current {
				snap.current = append(snap.current, memory)
				found = true
			}
		}
		if !found {
			return snap, fmt.Errorf("memory %s has no current revision", e.ID)
		}
		if !sort.StringsAreSorted(e.EvidenceRuns) {
			return snap, errors.New("memory evidence references are not canonical")
		}
		snap.evidence[e.ID] = append([]string(nil), e.EvidenceRuns...)
		snap.order[e.ID] = e.Order
	}
	return snap, nil
}

func (f *fileMemory) readManifest() (fileManifest, error) {
	var m fileManifest
	if err := decodePrivateJSON(filepath.Join(f.sourceDir, "current.json"), &m); err != nil {
		return m, fmt.Errorf("read authoritative memory manifest: %w", err)
	}
	if m.Version != 1 || m.SourceID != f.config.SourceID || m.StateHome != f.config.StateHome || m.Generation == 0 {
		return m, errors.New("invalid authoritative memory manifest identity")
	}
	return m, nil
}

func (f *fileMemory) publish(snap fileSnapshot, operations []fileOperation) (fileManifest, error) {
	next := fileManifest{Version: 1, SourceID: f.config.SourceID, StateHome: f.config.StateHome, Generation: f.manifest.Generation + 1, Operations: operations}
	current := map[string]Memory{}
	domains := map[string]fileDomain{}
	entries := map[string]*fileEntry{}
	publishedRevisions := map[string]map[int]bool{}
	for _, entry := range f.manifest.Entries {
		publishedRevisions[entry.ID] = map[int]bool{}
		for _, ref := range entry.Revisions {
			publishedRevisions[entry.ID][ref.Revision] = true
		}
	}
	for _, m := range snap.current {
		current[m.ID] = m
	}
	for _, m := range snap.revisions {
		if !validFileID(m.ID) || m.Revision < 1 {
			return next, errors.New("memory ID cannot be represented as a safe file path")
		}
		m.AgentID, m.AudienceID = normalizedPartition(m.AgentID, m.AudienceID)
		d := fileDomainFor(f.config, m)
		domains[d.ID] = d
		e := entries[m.ID]
		if e == nil {
			e = &fileEntry{ID: m.ID, Domain: d.ID, Directory: memoryDirectory(m), Current: current[m.ID].Revision, Order: snap.order[m.ID], EvidenceRuns: snap.evidence[m.ID]}
			entries[m.ID] = e
		}
		if e.Domain != d.ID || e.Directory != memoryDirectory(m) {
			return next, errors.New("memory revision changed identity or storage kind")
		}
		if err := atomicJSON(filepath.Join(f.config.MemoryRoot, "domains", d.ID, "scope.json"), d, true); err != nil {
			return next, err
		}
		base := f.revisionPath(*e, m.Revision)
		record := fileRecord{Version: 1, SourceID: f.config.SourceID, StateHome: f.config.StateHome, Memory: m}
		b, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return next, err
		}
		b = append(b, '\n')
		content := memoryDocument(m)
		// An interruption before manifest publication can leave staged files.
		// They have never been visible and may be replaced on retry; every
		// revision already referenced by the manifest remains immutable.
		immutable := publishedRevisions[m.ID][m.Revision]
		if immutable {
			if _, err = readPrivateFile(filepath.Join(base, "record.json")); err != nil {
				return next, err
			}
			if _, err = readPrivateFile(filepath.Join(base, memoryContentName(e.Directory))); err != nil {
				return next, err
			}
		}
		if err = atomicFile(filepath.Join(base, "record.json"), b, immutable); err != nil {
			return next, err
		}
		if err = atomicFile(filepath.Join(base, memoryContentName(e.Directory)), content, immutable); err != nil {
			return next, err
		}
		e.Revisions = append(e.Revisions, fileReference{Revision: m.Revision, RecordSHA256: fileHash(b), ContentSHA256: fileHash(content)})
	}
	for _, d := range domains {
		next.Domains = append(next.Domains, d)
	}
	sort.Slice(next.Domains, func(i, j int) bool { return next.Domains[i].ID < next.Domains[j].ID })
	for _, e := range entries {
		sort.Slice(e.Revisions, func(i, j int) bool { return e.Revisions[i].Revision < e.Revisions[j].Revision })
		sort.Strings(e.EvidenceRuns)
		next.Entries = append(next.Entries, *e)
	}
	sort.Slice(next.Entries, func(i, j int) bool { return next.Entries[i].Order < next.Entries[j].Order })
	if err := atomicJSON(filepath.Join(f.sourceDir, "publications", strconv.FormatUint(next.Generation, 10)+".json"), next, false); err != nil {
		return next, err
	}
	if err := atomicJSON(filepath.Join(f.sourceDir, "current.json"), next, false); err != nil {
		return next, err
	}
	f.manifest = next
	// Domain views are informational pointers. The source manifest above is the
	// sole authority, including cross-domain changes in one publication.
	for _, d := range next.Domains {
		view := struct {
			SourceID   string      `json:"source_id"`
			Generation uint64      `json:"generation"`
			Entries    []fileEntry `json:"entries"`
		}{SourceID: next.SourceID, Generation: next.Generation, Entries: []fileEntry{}}
		for _, e := range next.Entries {
			if e.Domain == d.ID {
				view.Entries = append(view.Entries, e)
			}
		}
		if err := atomicJSON(filepath.Join(f.config.MemoryRoot, "domains", d.ID, "current.json"), view, false); err != nil {
			return next, err
		}
	}
	return next, nil
}

func (s *Store) openFileMemory(ctx context.Context) error {
	var config FileMemoryConfig
	err := decodePrivateJSON(FileMemoryMarker(s.home), &config)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.enableFileMemory(ctx, config)
}

func (s *Store) enableFileMemory(ctx context.Context, c FileMemoryConfig) error {
	if err := validateFileConfig(c, s.home); err != nil {
		return err
	}
	f := &fileMemory{config: c, sourceDir: filepath.Join(c.MemoryRoot, "sources", sourceKey(c.SourceID))}
	release, err := f.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	snap, err := f.readSnapshot()
	if err != nil {
		return err
	}
	f.manifest = snap.manifest
	cacheDir := filepath.Join(c.CacheRoot, sourceKey(c.SourceID))
	if err = privateDirectories(cacheDir); err != nil {
		return err
	}
	path := filepath.Join(cacheDir, "index.db")
	cacheFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = errors.Join(cacheFile.Chmod(0600), cacheFile.Close()); err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) error { return errors.Join(err, db.Close()) }
	op := url.URL{Scheme: "file", Path: filepath.Join(s.home, "state.db")}
	oq := op.Query()
	if s.readOnly {
		oq.Set("mode", "ro")
	}
	op.RawQuery = oq.Encode()
	if _, err = db.ExecContext(ctx, "ATTACH DATABASE ? AS runtime", op.String()); err != nil {
		return fail(err)
	}
	if _, err = db.ExecContext(ctx, fileIndexSchema); err != nil {
		return fail(err)
	}
	old := s.db
	s.db = db
	s.files = f
	if err = s.sqlTransaction(ctx, func(tx *sql.Tx) error {
		if err = s.recoverFileOperations(ctx, tx, snap.manifest); err != nil {
			return err
		}
		return rebuildFileIndex(ctx, tx, snap)
	}); err != nil {
		s.db = old
		s.files = nil
		return fail(err)
	}
	if !s.readOnly {
		if err = f.ack(snap.manifest.Generation); err != nil {
			s.db = old
			s.files = nil
			return fail(err)
		}
	}
	return old.Close()
}

func captureFileSnapshot(ctx context.Context, tx *sql.Tx) (fileSnapshot, error) {
	snap := fileSnapshot{evidence: map[string][]string{}, order: map[string]int64{}}
	fileIndex := fileMemorySQL(ctx, tx)
	rows, err := tx.QueryContext(ctx, "SELECT rowid,id,payload,agent_id,audience_id,sender_id,scope,text,status,expires_at FROM memories ORDER BY rowid")
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var order int64
		var b []byte
		var m Memory
		var a, u, p string
		var id string
		var scope, text, status string
		var expiry sql.NullString
		if err = rows.Scan(&order, &id, &b, &a, &u, &p, &scope, &text, &status, &expiry); err != nil {
			rows.Close()
			return snap, err
		}
		if err = json.Unmarshal(b, &m); err != nil {
			rows.Close()
			return snap, err
		}
		m.AgentID, m.AudienceID = normalizedPartition(a, u)
		m.SenderID = p
		if fileIndex && (id != m.ID || scope != m.Scope || text != m.Text || status != m.Status || expiry.Valid != (m.ExpiresAt != nil) || expiry.Valid && expiry.String != timestamp(*m.ExpiresAt)) {
			rows.Close()
			return snap, errors.New("file-memory index columns disagree with source payload")
		}
		snap.current = append(snap.current, m)
		snap.order[m.ID] = order
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return snap, err
	}
	owners := map[string]Memory{}
	for _, m := range snap.current {
		owners[m.ID] = m
		snap.evidence[m.ID] = nil
	}
	rows, err = tx.QueryContext(ctx, "SELECT memory_id,revision,payload FROM memory_revisions ORDER BY memory_id,revision")
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var id string
		var revision int
		var b []byte
		var m Memory
		if err = rows.Scan(&id, &revision, &b); err != nil {
			rows.Close()
			return snap, err
		}
		if err = json.Unmarshal(b, &m); err != nil {
			rows.Close()
			return snap, err
		}
		owner, ok := owners[id]
		if !ok || m.ID != id || m.Revision != revision {
			rows.Close()
			return snap, errors.New("historical memory revision attribution is inconsistent")
		}
		m.AgentID, m.AudienceID, m.SenderID = owner.AgentID, owner.AudienceID, owner.SenderID
		snap.revisions = append(snap.revisions, m)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return snap, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT memory_id,run_id FROM memory_evidence ORDER BY memory_id,run_id")
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var id, run string
		if err = rows.Scan(&id, &run); err != nil {
			rows.Close()
			return snap, err
		}
		if _, ok := owners[id]; !ok {
			rows.Close()
			return snap, errors.New("memory evidence references an unknown memory")
		}
		snap.evidence[id] = append(snap.evidence[id], run)
	}
	return snap, errors.Join(rows.Err(), rows.Close())
}

func rebuildFileIndex(ctx context.Context, tx *sql.Tx, snap fileSnapshot) error {
	for _, table := range []string{"memory_terms", "memory_annotations", "memory_fts", "memory_evidence", "memory_revisions", "memories"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	for _, m := range snap.current {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		var expiry any
		if m.ExpiresAt != nil {
			expiry = timestamp(*m.ExpiresAt)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO memories(rowid,id,agent_id,audience_id,sender_id,scope,text,status,expires_at,payload) VALUES(?,?,?,?,?,?,?,?,?,?)", snap.order[m.ID], m.ID, m.AgentID, m.AudienceID, m.SenderID, m.Scope, m.Text, m.Status, expiry, b); err != nil {
			return err
		}
		if recallEligible(m, time.Now()) {
			if _, err = tx.ExecContext(ctx, "INSERT INTO memory_fts(memory_id,tokens) VALUES(?,?)", m.ID, strings.Join(searchTokens(m.Text), " ")); err != nil {
				return err
			}
		}
		for _, run := range snap.evidence[m.ID] {
			if _, err = tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", m.ID, run); err != nil {
				return err
			}
		}
	}
	for _, m := range snap.revisions {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", m.ID, m.Revision, b); err != nil {
			return err
		}
	}
	if err := migrateCurrentAnnotations(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO file_index_checkpoint(id,generation) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET generation=excluded.generation", snap.manifest.Generation)
	return err
}

func snapshotDigest(snap fileSnapshot) string {
	// Ignore publication generation and file paths: historical comparison checks
	// the actual identity, every revision, evidence links, and insertion order.
	value := struct {
		Current   []Memory
		Revisions []Memory
		Evidence  map[string][]string
		Order     map[string]int64
	}{snap.current, snap.revisions, snap.evidence, snap.order}
	sort.Slice(value.Current, func(i, j int) bool { return value.Current[i].ID < value.Current[j].ID })
	sort.Slice(value.Revisions, func(i, j int) bool {
		a, b := value.Revisions[i], value.Revisions[j]
		if a.ID == b.ID {
			return a.Revision < b.Revision
		}
		return a.ID < b.ID
	})
	b, _ := json.Marshal(value)
	return fileHash(b)
}

func reportFileSnapshot(snap fileSnapshot) FileMemoryReport {
	r := FileMemoryReport{SourceID: snap.manifest.SourceID, Generation: snap.manifest.Generation, Domains: len(snap.manifest.Domains), Memories: len(snap.current), Revisions: len(snap.revisions), Digest: snapshotDigest(snap)}
	for _, m := range snap.current {
		if memoryDirectory(m) == "skills" {
			r.Skills++
		}
		r.Evidence += len(snap.evidence[m.ID])
	}
	return r
}

func (s *Store) fileReport(snap fileSnapshot) FileMemoryReport {
	r := reportFileSnapshot(snap)
	r.IndexPath = filepath.Join(s.files.config.CacheRoot, sourceKey(s.files.config.SourceID), "index.db")
	return r
}

// MigrateFileMemory publishes a verified snapshot before atomically enabling
// files for this state home. It never rewrites historical memory rows. Callers
// must hold Acquire(home) and stop the owning worker before migration.
func (s *Store) MigrateFileMemory(ctx context.Context, root, cacheRoot, sourceID string) (FileMemoryReport, error) {
	if s.readOnly || s.borrowed {
		return FileMemoryReport{}, errors.New("file-memory migration requires an owning writable store")
	}
	root, err := physicalPath(root)
	if err != nil {
		return FileMemoryReport{}, err
	}
	cacheRoot, err = physicalPath(cacheRoot)
	if err != nil {
		return FileMemoryReport{}, err
	}
	c := FileMemoryConfig{Version: 1, MemoryRoot: root, CacheRoot: cacheRoot, SourceID: sourceID, StateHome: s.home}
	if err = validateFileConfig(c, s.home); err != nil {
		return FileMemoryReport{}, err
	}
	if s.files != nil {
		if s.files.config != c {
			return FileMemoryReport{}, errors.New("state home is already migrated to a different file-memory identity")
		}
		return s.VerifyFileMemory(ctx)
	}
	f := &fileMemory{config: c, sourceDir: filepath.Join(root, "sources", sourceKey(sourceID))}
	release, err := f.lock(ctx)
	if err != nil {
		return FileMemoryReport{}, err
	}
	var snap fileSnapshot
	err = s.sqlTransaction(ctx, func(tx *sql.Tx) error { var e error; snap, e = captureFileSnapshot(ctx, tx); return e })
	if err == nil {
		var existing fileManifest
		err = decodePrivateJSON(filepath.Join(f.sourceDir, "current.json"), &existing)
		if err == nil {
			f.manifest = existing
			published, e := f.readSnapshot()
			if e != nil {
				err = e
			} else if snapshotDigest(published) != snapshotDigest(snap) {
				err = errors.New("existing file-memory source differs from operational historical snapshot")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			_, err = f.publish(snap, nil)
		}
	}
	if err == nil {
		var published fileSnapshot
		published, err = f.readSnapshot()
		if err == nil && snapshotDigest(published) != snapshotDigest(snap) {
			err = errors.New("file-memory migration historical comparison failed")
		}
		if err == nil {
			snap = published
		}
	}
	if err == nil {
		err = f.ack(snap.manifest.Generation)
	}
	if err == nil {
		err = atomicJSON(FileMemoryMarker(s.home), c, false)
	}
	unlockErr := release()
	err = errors.Join(err, unlockErr)
	if err != nil {
		return FileMemoryReport{}, err
	}
	if err = s.enableFileMemory(ctx, c); err != nil {
		return FileMemoryReport{}, err
	}
	return s.fileReport(snap), nil
}

func (s *Store) VerifyFileMemory(ctx context.Context) (FileMemoryReport, error) {
	if s.files == nil {
		return FileMemoryReport{}, errors.New("state home has not enabled file memory")
	}
	f := s.files
	release, err := f.lock(ctx)
	if err != nil {
		return FileMemoryReport{}, err
	}
	defer release()
	snap, err := f.readSnapshot()
	if err != nil {
		return FileMemoryReport{}, err
	}
	// Verification also repairs every derived row. Payload/generation checks
	// alone cannot prove that a cache's FTS, alias or topic rows are correct.
	err = s.sqlTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.recoverFileOperations(ctx, tx, snap.manifest); err != nil {
			return err
		}
		return rebuildFileIndex(ctx, tx, snap)
	})
	if err != nil {
		return FileMemoryReport{}, err
	}
	if !s.readOnly {
		if err = f.ack(snap.manifest.Generation); err != nil {
			return FileMemoryReport{}, err
		}
	}
	return s.fileReport(snap), nil
}

func (f *fileMemory) ackPath(generation uint64) string {
	return filepath.Join(f.sourceDir, "applied", strconv.FormatUint(generation, 10)+".json")
}
func (f *fileMemory) ack(generation uint64) error {
	return atomicJSON(f.ackPath(generation), struct {
		Generation uint64 `json:"generation"`
	}{generation}, true)
}

func (s *Store) recoverFileOperations(ctx context.Context, tx *sql.Tx, m fileManifest) error {
	if _, err := readPrivateFile(s.files.ackPath(m.Generation)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, op := range m.Operations {
		if op.Table != "runs" && op.Table != "candidates" {
			return errors.New("invalid file-memory recovery operation")
		}
		var payload []byte
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM runtime."+op.Table+" WHERE id=?", op.ID).Scan(&payload); err != nil {
			return err
		}
		if equalJSON(payload, op.After) {
			continue
		}
		if !equalJSON(payload, op.Before) {
			return fmt.Errorf("file-memory receipt recovery conflicts with operational %s %s", op.Table, op.ID)
		}
		if s.readOnly {
			return errors.New("file-memory publication needs writable receipt recovery")
		}
		if op.Table == "runs" {
			var run Run
			if err := json.Unmarshal(op.After, &run); err != nil {
				return err
			}
			if err := writeRun(ctx, tx, run); err != nil {
				return err
			}
		} else {
			var candidate Candidate
			if err := json.Unmarshal(op.After, &candidate); err != nil {
				return err
			}
			if err := writeCandidate(ctx, tx, candidate, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// Receipt recovery precedes operational mutations, but does not read learned
// documents or the retrieval index. Thus a post-publication storage error
// cannot be followed by a failure-status write that makes its receipt stale,
// while an edited memory document does not block real execution evidence.
func (s *Store) fileOperationalTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	f := s.files
	release, err := f.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	manifest, err := f.readManifest()
	if err != nil {
		return fmt.Errorf("operational memory receipt is unavailable: %w", err)
	}
	if _, err = readPrivateFile(f.ackPath(manifest.Generation)); errors.Is(err, os.ErrNotExist) {
		if err = s.sqlTransaction(ctx, func(tx *sql.Tx) error { return s.recoverFileOperations(ctx, tx, manifest) }); err != nil {
			return err
		}
		if !s.readOnly {
			if err = f.ack(manifest.Generation); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	return s.sqlTransaction(ctx, fn)
}

func readFileOperations(ctx context.Context, tx *sql.Tx) ([]fileOperation, error) {
	rows, err := tx.QueryContext(ctx, "SELECT table_name,id,before_payload,after_payload FROM file_operation_log ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	positions := map[string]int{}
	var result []fileOperation
	for rows.Next() {
		var op fileOperation
		if err = rows.Scan(&op.Table, &op.ID, &op.Before, &op.After); err != nil {
			rows.Close()
			return nil, err
		}
		key := op.Table + "\x00" + op.ID
		if i, ok := positions[key]; ok {
			result[i].After = op.After
		} else {
			positions[key] = len(result)
			result = append(result, op)
		}
	}
	return result, errors.Join(rows.Err(), rows.Close())
}

func (s *Store) fileTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	f := s.files
	release, err := f.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	snap, err := f.readSnapshot()
	if err != nil {
		return err
	}
	f.manifest = snap.manifest
	// Recover receipts in their own commit, before running caller work. A caller
	// failure must not undo recovery of an already published file snapshot.
	err = s.sqlTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.recoverFileOperations(ctx, tx, snap.manifest); err != nil {
			return err
		}
		var generation uint64
		err := tx.QueryRowContext(ctx, "SELECT generation FROM file_index_checkpoint WHERE id=1").Scan(&generation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if generation != snap.manifest.Generation {
			return rebuildFileIndex(ctx, tx, snap)
		}
		indexed, err := captureFileSnapshot(ctx, tx)
		if err != nil {
			return rebuildFileIndex(ctx, tx, snap)
		}
		if snapshotDigest(indexed) != snapshotDigest(snap) {
			return rebuildFileIndex(ctx, tx, snap)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !s.readOnly {
		if err = f.ack(snap.manifest.Generation); err != nil {
			return err
		}
	}
	published := false
	err = s.sqlTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM file_operation_log"); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		after, err := captureFileSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		if snapshotDigest(after) == snapshotDigest(snap) {
			return nil
		}
		if s.readOnly {
			return errors.New("cannot mutate file memory from a read-only store")
		}
		ops, err := readFileOperations(ctx, tx)
		if err != nil {
			return err
		}
		manifest, err := f.publish(after, ops)
		if err != nil {
			return err
		}
		published = true
		_, err = tx.ExecContext(ctx, "UPDATE file_index_checkpoint SET generation=? WHERE id=1", manifest.Generation)
		return err
	})
	if err != nil {
		return err
	}
	if published {
		return f.ack(f.manifest.Generation)
	}
	return nil
}

// MemoryFingerprint rotates native sessions when a selected knowledge scope
// changes, including withdrawal. Partition predicates precede fingerprinting.
func (s *Store) MemoryFingerprint(ctx context.Context, scopes []string) (string, error) {
	var fingerprint string
	err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		a, b := s.Partition()
		var err error
		fingerprint, err = memoryFingerprintTx(ctx, tx, scopes, a, b)
		return err
	})
	if err != nil {
		return "", err
	}
	return fingerprint, nil
}

func fileMemorySQL(ctx context.Context, tx *sql.Tx) bool {
	var found bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE name='file_index_checkpoint')").Scan(&found)
	return err == nil && found
}

func memoryFingerprintTx(ctx context.Context, tx *sql.Tx, scopes []string, a, b string) (string, error) {
	return memoryFingerprintTxAt(ctx, tx, scopes, a, b, time.Now())
}

func memoryFingerprintTxAt(ctx context.Context, tx *sql.Tx, scopes []string, a, b string, now time.Time) (string, error) {
	sorted := append([]string(nil), scopes...)
	sort.Strings(sorted)
	var records []Memory
	for _, scope := range sorted {
		rows, err := tx.QueryContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? ORDER BY id", a, b, scope)
		if err != nil {
			return "", err
		}
		items, err := scanMemories(rows)
		err = errors.Join(err, rows.Close())
		if err != nil {
			return "", err
		}
		records = append(records, items...)
	}
	type effectiveMemory struct {
		Memory   Memory `json:"memory"`
		Eligible bool   `json:"eligible"`
	}
	effective := make([]effectiveMemory, 0, len(records))
	for _, m := range records {
		effective = append(effective, effectiveMemory{m, recallEligible(m, now)})
	}
	payload, _ := json.Marshal(effective)
	return fileHash(payload), nil
}

// RollbackFileMemory imports every file revision and evidence association into
// the operational database before retiring the backend marker. It preserves
// new knowledge instead of restoring an old backup and discarding updates.
func (s *Store) RollbackFileMemory(ctx context.Context) (FileMemoryReport, error) {
	if s.files == nil || s.readOnly || s.borrowed {
		return FileMemoryReport{}, errors.New("rollback requires an owning writable file-memory store")
	}
	f := s.files
	release, err := f.lock(ctx)
	if err != nil {
		return FileMemoryReport{}, err
	}
	defer release()
	snap, err := f.readSnapshot()
	if err != nil {
		return FileMemoryReport{}, err
	}
	// An existing handle may still have an unacknowledged publication after
	// its SQL commit failed. Retiring the marker before repairing those review
	// and run receipts would leave accepted file knowledge paired with pending
	// operational state in the restored SQLite backend.
	if err = s.sqlTransaction(ctx, func(tx *sql.Tx) error { return s.recoverFileOperations(ctx, tx, snap.manifest) }); err != nil {
		return FileMemoryReport{}, err
	}
	if err = f.ack(snap.manifest.Generation); err != nil {
		return FileMemoryReport{}, err
	}
	op, err := sql.Open("sqlite", filepath.Join(s.home, "state.db"))
	if err != nil {
		return FileMemoryReport{}, err
	}
	op.SetMaxOpenConns(1)
	defer op.Close()
	// The migration owner holds the operational host lock. Disable foreign keys
	// only for this verified replacement transaction, then verify them afterward.
	if _, err = op.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return FileMemoryReport{}, err
	}
	legacy := &Store{db: op}
	err = legacy.sqlTransaction(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"memory_terms", "memory_annotations", "memory_fts", "memory_evidence", "memory_revisions", "memories"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		for _, m := range snap.current {
			b, _ := json.Marshal(m)
			var expiry any
			if m.ExpiresAt != nil {
				expiry = timestamp(*m.ExpiresAt)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO memories(rowid,id,agent_id,audience_id,sender_id,scope,text,status,expires_at,payload) VALUES(?,?,?,?,?,?,?,?,?,?)", snap.order[m.ID], m.ID, m.AgentID, m.AudienceID, m.SenderID, m.Scope, m.Text, m.Status, expiry, b); err != nil {
				return err
			}
			if recallEligible(m, time.Now()) {
				if _, err := tx.ExecContext(ctx, "INSERT INTO memory_fts(memory_id,tokens) VALUES(?,?)", m.ID, strings.Join(searchTokens(m.Text), " ")); err != nil {
					return err
				}
			}
		}
		for _, m := range snap.revisions {
			b, _ := json.Marshal(m)
			if _, err := tx.ExecContext(ctx, "INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", m.ID, m.Revision, b); err != nil {
				return err
			}
		}
		for id, runs := range snap.evidence {
			for _, run := range runs {
				if _, err := tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", id, run); err != nil {
					return err
				}
			}
		}
		if err := migrateCurrentAnnotations(ctx, tx); err != nil {
			return err
		}
		actual, err := captureFileSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		if snapshotDigest(actual) != snapshotDigest(snap) {
			return errors.New("rollback historical memory comparison failed")
		}
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		broken := rows.Next()
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if broken {
			return errors.New("rollback would leave broken operational evidence references")
		}
		return nil
	})
	if err != nil {
		return FileMemoryReport{}, err
	}
	if err = os.Rename(FileMemoryMarker(s.home), filepath.Join(s.home, "file-memory.rollback.json")); err != nil {
		return FileMemoryReport{}, err
	}
	directory, err := os.Open(s.home)
	if err != nil {
		return FileMemoryReport{}, err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return FileMemoryReport{}, err
	}
	// Keep the current handle in file mode until it closes. Future opens select
	// SQLite; no publication may follow rollback from this handle.
	s.readOnly = true
	return s.fileReport(snap), nil
}

func equalJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return false
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}
