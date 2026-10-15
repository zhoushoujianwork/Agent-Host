// Package backup implements encrypted, compact Host recovery checkpoints.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-host/internal/state"
	"filippo.io/age"
)

const maxArchiveBytes = 256 << 20
const maxFileBytes = 64 << 20

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}
type Manifest struct {
	Version      int       `json:"version"`
	HostVersion  string    `json:"host_version"`
	CreatedAt    time.Time `json:"created_at"`
	Root         string    `json:"root"`
	Files        []File    `json:"files"`
	Sources      []string  `json:"sources"`
	Dependencies []string  `json:"external_dependencies"`
}
type payload struct {
	manifest Manifest
	files    map[string][]byte
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func safeName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\\\x00") && !filepath.IsAbs(name) && filepath.ToSlash(filepath.Clean(name)) == name && name != ".." && !strings.HasPrefix(name, "../")
}
func inside(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && safeName(filepath.ToSlash(rel))
}
func privateRead(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, fmt.Errorf("not a regular bounded backup file: %s", path)
	}
	return os.ReadFile(path)
}
func (p *payload) add(name string, b []byte) error {
	if !safeName(name) || len(b) > maxFileBytes {
		return fmt.Errorf("unsafe or oversized backup member %s", name)
	}
	if _, ok := p.files[name]; ok {
		return fmt.Errorf("duplicate backup member %s", name)
	}
	p.files[name] = b
	return nil
}
func (p *payload) inventory() error {
	p.manifest.Files = nil
	var total int
	for path, b := range p.files {
		total += len(b)
		p.manifest.Files = append(p.manifest.Files, File{Path: path, SHA256: digest(b), Size: len(b)})
	}
	if total > maxArchiveBytes {
		return errors.New("compact backup exceeds 256 MiB; no files were silently skipped")
	}
	sort.Slice(p.manifest.Files, func(i, j int) bool { return p.manifest.Files[i].Path < p.manifest.Files[j].Path })
	return nil
}
func encodeArchive(w io.Writer, p payload, key *age.X25519Identity) error {
	ew, e := age.Encrypt(w, key.Recipient())
	if e != nil {
		return e
	}
	zw := gzip.NewWriter(ew)
	tw := tar.NewWriter(zw)
	b, e := json.MarshalIndent(p.manifest, "", "  ")
	if e != nil {
		return e
	}
	write := func(name string, b []byte) error {
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		_, e := tw.Write(b)
		return e
	}
	if e = write("manifest.json", b); e != nil {
		return e
	}
	for _, f := range p.manifest.Files {
		if e = write(f.Path, p.files[f.Path]); e != nil {
			return e
		}
	}
	return errors.Join(tw.Close(), zw.Close(), ew.Close())
}
func decodeArchive(path string, key *age.X25519Identity) (payload, error) {
	p := payload{files: map[string][]byte{}}
	f, e := os.Open(path)
	if e != nil {
		return p, e
	}
	defer f.Close()
	r, e := age.Decrypt(f, key)
	if e != nil {
		return p, e
	}
	// Read to authenticated EOF before parsing or restoring any plaintext.
	b, e := io.ReadAll(io.LimitReader(r, maxArchiveBytes+1))
	if e != nil {
		return p, e
	}
	if len(b) > maxArchiveBytes {
		return p, errors.New("encrypted archive exceeds limit")
	}
	zr, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		return p, e
	}
	defer zr.Close()
	plain, e := io.ReadAll(io.LimitReader(zr, maxArchiveBytes+1))
	if e != nil {
		return p, e
	}
	if len(plain) > maxArchiveBytes {
		return p, errors.New("expanded archive exceeds limit")
	}
	tr := tar.NewReader(bytes.NewReader(plain))
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return p, e
		}
		if !safeName(h.Name) || h.Typeflag != tar.TypeReg || seen[h.Name] || h.Size < 0 || h.Size > maxFileBytes {
			return p, errors.New("unsafe, duplicate or oversized archive member")
		}
		seen[h.Name] = true
		b, e := io.ReadAll(io.LimitReader(tr, maxFileBytes+1))
		if e != nil {
			return p, e
		}
		if h.Name == "manifest.json" {
			if e = json.Unmarshal(b, &p.manifest); e != nil {
				return p, e
			}
		} else {
			p.files[h.Name] = b
		}
	}
	if !seen["manifest.json"] || p.manifest.Version != 1 || !filepath.IsAbs(p.manifest.Root) || p.manifest.CreatedAt.IsZero() || len(p.manifest.Files) != len(p.files) {
		return p, errors.New("unsupported or incomplete archive manifest")
	}
	listed := map[string]bool{}
	sources := map[string]bool{}
	for _, name := range p.manifest.Sources {
		if !strings.HasPrefix(name, "recovery/") || !strings.HasSuffix(name, ".json") || sources[name] {
			return p, errors.New("invalid recovery source inventory")
		}
		sources[name] = true
	}
	for _, f := range p.manifest.Files {
		b, ok := p.files[f.Path]
		if !ok || listed[f.Path] || f.Size != len(b) || digest(b) != f.SHA256 {
			return p, fmt.Errorf("archive checksum mismatch: %s", f.Path)
		}
		listed[f.Path] = true
		if strings.HasPrefix(f.Path, "recovery/") && !sources[f.Path] {
			return p, errors.New("unlisted recovery source")
		}
		if !strings.HasPrefix(f.Path, "workspace/config/") && !strings.HasPrefix(f.Path, "workspace/users/") && !strings.HasPrefix(f.Path, "workspace/datasource/plugins/local-documents/sources/") && !strings.HasPrefix(f.Path, "recovery/") {
			return p, errors.New("unexpected archive member")
		}
	}
	for name := range sources {
		if !listed[name] {
			return p, errors.New("missing recovery source")
		}
	}
	return p, nil
}

func restorePayload(ctx context.Context, p payload, target string) (err error) {
	if _, e := os.Lstat(target); !errors.Is(e, os.ErrNotExist) {
		return errors.New("restore target must not exist")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(target))
	if e != nil {
		return e
	}
	target = filepath.Join(parent, filepath.Base(target))
	if err = os.Mkdir(target, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(target))
		}
	}()
	for _, f := range p.manifest.Files {
		if !strings.HasPrefix(f.Path, "workspace/") {
			continue
		}
		path := filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(f.Path, "workspace/")))
		if !inside(target, path) {
			return errors.New("restore path escapes target")
		}
		b := p.files[f.Path]
		if strings.HasPrefix(f.Path, "workspace/config/") && strings.HasSuffix(f.Path, ".json") {
			var v any
			if e = json.Unmarshal(b, &v); e != nil {
				return e
			}
			v = relocate(v, p.manifest.Root, target)
			if f.Path == "workspace/config/backup.json" {
				if m, ok := v.(map[string]any); ok {
					m["enabled"] = false
				}
			}
			b, e = json.MarshalIndent(v, "", "  ")
			if e != nil {
				return e
			}
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			return err
		}
	}
	seenHomes, seenSources := map[string]bool{}, map[string]bool{}
	for _, name := range p.manifest.Sources {
		var source struct {
			Home  string               `json:"home"`
			State state.MemoryRecovery `json:"state"`
		}
		if e = json.Unmarshal(p.files[name], &source); e != nil {
			return e
		}
		if !safeName(source.Home) || seenHomes[source.Home] || seenSources[source.State.Config.SourceID] {
			return errors.New("invalid or repeated restore source")
		}
		if source.Home != "cli-state" && !(strings.HasPrefix(source.Home, "channels/") && strings.HasSuffix(source.Home, "/state")) {
			return errors.New("unexpected restore state home")
		}
		seenHomes[source.Home], seenSources[source.State.Config.SourceID] = true, true
		home := filepath.Join(target, filepath.FromSlash(source.Home))
		if e = state.RestoreMemoryRecovery(ctx, source.State, home, filepath.Join(target, "memory"), filepath.Join(target, "cache", "memory")); e != nil {
			return e
		}
		s, e := state.Open(home, true)
		if e != nil {
			return e
		}
		report, e := s.VerifyFileMemory(ctx)
		if e == nil {
			for _, expected := range source.State.Memories {
				got, readErr := s.WithPartition(expected.AgentID, expected.AudienceID).Memory(ctx, expected.ID)
				if readErr != nil {
					e = readErr
					break
				}
				before, _ := json.Marshal(expected)
				after, _ := json.Marshal(got)
				if !bytes.Equal(before, after) {
					e = fmt.Errorf("restored memory differs: %s", expected.ID)
					break
				}
			}
		}
		closeErr := s.Close()
		if e = errors.Join(e, closeErr); e != nil {
			return e
		}
		if report.Memories != len(source.State.Memories) || report.Revisions != len(source.State.Memories) {
			return errors.New("compact restore memory count differs")
		}
	}
	return writeJSON(filepath.Join(target, "recovery.json"), map[string]any{"version": 1, "restored_at": time.Now().UTC(), "original_root": p.manifest.Root, "external_dependencies": p.manifest.Dependencies, "history": "current revisions only; old tasks are not resumed"})
}
func relocate(v any, oldRoot, newRoot string) any {
	switch x := v.(type) {
	case string:
		if x == oldRoot || strings.HasPrefix(x, oldRoot+string(filepath.Separator)) {
			return newRoot + strings.TrimPrefix(x, oldRoot)
		}
	case []any:
		for i, item := range x {
			x[i] = relocate(item, oldRoot, newRoot)
		}
	case map[string]any:
		for k, item := range x {
			x[k] = relocate(item, oldRoot, newRoot)
		}
	}
	return v
}

// Verify decrypts and checks the complete tar, then imports it without starting a Host.
func Verify(ctx context.Context, archive, identity string) (Manifest, error) {
	key, e := loadIdentity(identity)
	if e != nil {
		return Manifest{}, e
	}
	p, e := decodeArchive(archive, key)
	if e != nil {
		return Manifest{}, e
	}
	tmp, e := os.MkdirTemp("", "agent-host-backup-verify-")
	if e != nil {
		return Manifest{}, e
	}
	defer os.RemoveAll(tmp)
	e = restorePayload(ctx, p, filepath.Join(tmp, "restored"))
	return p.manifest, e
}
func Restore(ctx context.Context, archive, identity, target string) (Manifest, error) {
	key, e := loadIdentity(identity)
	if e != nil {
		return Manifest{}, e
	}
	p, e := decodeArchive(archive, key)
	if e != nil {
		return Manifest{}, e
	}
	return p.manifest, restorePayload(ctx, p, target)
}
