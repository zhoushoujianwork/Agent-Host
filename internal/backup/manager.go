package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"agent-host/internal/buildinfo"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
	"filippo.io/age"
)

type Policy struct {
	Version      int    `json:"version"`
	Enabled      bool   `json:"enabled"`
	Destination  string `json:"destination"`
	IdentityFile string `json:"identity_file"`
	Timezone     string `json:"timezone"`
}
type Point struct {
	CheckedAt  time.Time `json:"checked_at"`
	Archive    string    `json:"archive"`
	Bytes      int64     `json:"bytes"`
	VerifiedAt time.Time `json:"verified_at"`
}
type Status struct {
	Enabled     bool      `json:"enabled"`
	Destination string    `json:"destination,omitempty"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	Latest      *Point    `json:"latest,omitempty"`
}

func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".backup-write-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(append(b, '\n')); e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}
func loadJSON(path string, v any) error {
	b, e := privateRead(path)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e = d.Decode(&extra); !errors.Is(e, io.EOF) {
		return errors.New("trailing JSON in backup metadata")
	}
	return nil
}
func loadIdentity(path string) (*age.X25519Identity, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("backup identity must be private (0600)")
	}
	b, e := privateRead(path)
	if e != nil {
		return nil, e
	}
	key, e := age.ParseX25519Identity(strings.TrimSpace(string(b)))
	if e != nil {
		return nil, errors.New("invalid age recovery identity")
	}
	return key, nil
}
func canonical(path string) (string, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	var parts []string
	for p := abs; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("backup path must not be a symbolic link: %s", p)
			}
			base, e := filepath.EvalSymlinks(p)
			if e != nil {
				return "", e
			}
			for i := len(parts) - 1; i >= 0; i-- {
				base = filepath.Join(base, parts[i])
			}
			return base, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		parts = append(parts, filepath.Base(p))
	}
}
func LoadPolicy(root string) (Policy, error) {
	var p Policy
	var e error
	root, e = canonical(root)
	if e != nil {
		return p, e
	}
	if e := loadJSON(filepath.Join(root, "config", "backup.json"), &p); e != nil {
		return p, e
	}
	if p.Version != 1 || !filepath.IsAbs(p.Destination) || !filepath.IsAbs(p.IdentityFile) {
		return p, errors.New("backup policy requires version 1 and absolute destination/identity paths")
	}
	if _, e := time.LoadLocation(p.Timezone); e != nil {
		return p, e
	}
	p.Destination, e = canonical(p.Destination)
	if e != nil {
		return p, e
	}
	p.IdentityFile, e = canonical(p.IdentityFile)
	if e != nil {
		return p, e
	}
	if e = validateStoragePaths(root, p.Destination, p.IdentityFile); e != nil {
		return p, e
	}
	return p, nil
}

func validateStoragePaths(root, destination, identity string) error {
	backups := filepath.Join(root, "backups")
	if destination == root || inside(root, destination) && destination != backups && !inside(backups, destination) {
		return errors.New("internal backup destination must be under the Host backups directory")
	}
	if identity == root || inside(root, identity) || identity == destination || inside(destination, identity) {
		return errors.New("keep the recovery key outside the Host root and archive directory")
	}
	return nil
}

// Init creates a separate recovery key and policy, never overwriting either.
func Init(root, destination, identity, timezone string) error {
	var e error
	root, e = canonical(root)
	if e != nil {
		return e
	}
	destination, e = canonical(destination)
	if e != nil {
		return e
	}
	identity, e = canonical(identity)
	if e != nil {
		return e
	}
	p := Policy{Version: 1, Enabled: true, Destination: destination, IdentityFile: identity, Timezone: timezone}
	if _, e = time.LoadLocation(timezone); e != nil {
		return e
	}
	if e = validateStoragePaths(root, destination, identity); e != nil {
		return e
	}
	policyPath := filepath.Join(root, "config", "backup.json")
	if _, e = os.Lstat(policyPath); !errors.Is(e, os.ErrNotExist) {
		return errors.New("backup policy already exists")
	}
	if e = os.MkdirAll(filepath.Dir(identity), 0700); e != nil {
		return e
	}
	key, e := age.GenerateX25519Identity()
	if e != nil {
		return e
	}
	f, e := os.OpenFile(identity, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.WriteString(key.String() + "\n")
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.MkdirAll(destination, 0700); e != nil {
		return e
	}
	return writeJSON(policyPath, p)
}
func ReadStatus(root string) (Status, error) {
	var s Status
	e := loadJSON(filepath.Join(root, "backup-state", "status.json"), &s)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return s, e
	}
	p, e := LoadPolicy(root)
	if errors.Is(e, os.ErrNotExist) {
		s.Enabled = false
		return s, nil
	}
	if e != nil {
		return s, e
	}
	s.Enabled, s.Destination = p.Enabled, p.Destination
	return s, nil
}
func List(root string) ([]Point, error) {
	p, e := LoadPolicy(root)
	if e != nil {
		return nil, e
	}
	var points []Point
	e = loadJSON(filepath.Join(p.Destination, "checkpoints.json"), &points)
	if errors.Is(e, os.ErrNotExist) {
		e = nil
	}
	return points, e
}

func collect(ctx context.Context, root string) (payload, error) {
	p := payload{manifest: Manifest{Version: 1, HostVersion: buildinfo.Current().Version, CreatedAt: time.Now().UTC(), Root: root}, files: map[string][]byte{}}
	dependencies := map[string]bool{}
	var configBefore map[string][]byte
	scan := func(base string) (map[string][]byte, error) {
		files := map[string][]byte{}
		e := filepath.WalkDir(base, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("backup refuses symbolic link: %s", path)
			}
			if d.IsDir() {
				return nil
			}
			if base == filepath.Join(root, "users") && (strings.HasPrefix(d.Name(), ".account-") || strings.HasPrefix(d.Name(), ".profile-")) {
				return nil // Unpublished identity receipt, never recovery authority.
			}
			b, e := privateRead(path)
			if e != nil {
				return e
			}
			rel, e := filepath.Rel(root, path)
			if e != nil {
				return e
			}
			files["workspace/"+filepath.ToSlash(rel)] = b
			return nil
		})
		return files, e
	}
	var e error
	configBefore, e = scan(filepath.Join(root, "config"))
	if e != nil {
		return p, e
	}
	for name, b := range configBefore {
		if e = p.add(name, b); e != nil {
			return p, e
		}
	}
	// Authored publications are self-contained durable authority, not cache.
	skillRoot := filepath.Join(root, "managed-skills")
	if _, err := os.Lstat(filepath.Join(skillRoot, "catalog.json")); err == nil {
		catalog, err := managedskills.Load(skillRoot)
		if err != nil {
			return p, err
		}
		raw, err := json.Marshal(catalog)
		if err != nil {
			return p, err
		}
		if err = p.add("workspace/managed-skills/catalog.json", raw); err != nil {
			return p, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return p, err
	}
	var paths func(any)
	paths = func(v any) {
		switch x := v.(type) {
		case string:
			if filepath.IsAbs(x) && x != root && !inside(root, x) {
				dependencies[x] = true
			}
		case map[string]any:
			for _, item := range x {
				paths(item)
			}
		case []any:
			for _, item := range x {
				paths(item)
			}
		}
	}
	for name, b := range configBefore {
		if strings.HasSuffix(name, ".json") {
			var v any
			if e = json.Unmarshal(b, &v); e != nil {
				return p, e
			}
			paths(v)
		}
	}
	homes := map[string]bool{}
	for _, glob := range []string{"channels/*/state/state.db", "cli-state/state.db"} {
		matches, e := filepath.Glob(filepath.Join(root, glob))
		if e != nil {
			return p, e
		}
		for _, path := range matches {
			homes[filepath.Dir(path)] = true
		}
	}
	// Also account for every published source, even if its state was moved or lost.
	manifests, e := filepath.Glob(filepath.Join(root, "memory", "sources", "*", "current.json"))
	if e != nil {
		return p, e
	}
	for _, path := range manifests {
		var m struct {
			StateHome string `json:"state_home"`
		}
		b, e := privateRead(path)
		if e != nil {
			return p, e
		}
		if e = json.Unmarshal(b, &m); e != nil {
			return p, e
		}
		if !inside(root, m.StateHome) {
			return p, fmt.Errorf("memory source state home outside backup root: %s", m.StateHome)
		}
		homes[m.StateHome] = true
	}
	ids := map[string]bool{}
	homeList := make([]string, 0, len(homes))
	for home := range homes {
		homeList = append(homeList, home)
	}
	sort.Strings(homeList)
	for i, home := range homeList {
		r, e := state.CaptureMemoryRecovery(ctx, home)
		if e != nil {
			return p, fmt.Errorf("capture %s: %w", home, e)
		}
		if ids[r.Config.SourceID] {
			return p, errors.New("duplicate memory source identity")
		}
		ids[r.Config.SourceID] = true
		if r.Config.MemoryRoot != filepath.Join(root, "memory") {
			return p, errors.New("memory root is outside the configured workspace layout")
		}
		rel, e := filepath.Rel(root, home)
		if e != nil {
			return p, e
		}
		b, e := json.Marshal(struct {
			Home  string               `json:"home"`
			State state.MemoryRecovery `json:"state"`
		}{filepath.ToSlash(rel), r})
		if e != nil {
			return p, e
		}
		name := fmt.Sprintf("recovery/source-%03d.json", i)
		p.manifest.Sources = append(p.manifest.Sources, name)
		if e = p.add(name, b); e != nil {
			return p, e
		}
	}
	for _, retainedRoot := range []string{filepath.Join(root, "datasource", "plugins", "local-documents", "sources"), filepath.Join(root, "users")} {
		if _, e = os.Lstat(retainedRoot); e == nil {
			files, e := scan(retainedRoot)
			if e != nil {
				return p, e
			}
			for name, b := range files {
				if e = p.add(name, b); e != nil {
					return p, e
				}
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return p, e
		}
	}
	configAfter, e := scan(filepath.Join(root, "config"))
	if e != nil {
		return p, e
	}
	if len(configAfter) != len(configBefore) {
		return p, errors.New("configuration changed during backup; retry")
	}
	for name, b := range configBefore {
		if !bytes.Equal(b, configAfter[name]) {
			return p, errors.New("configuration changed during backup; retry")
		}
	}
	for path := range dependencies {
		p.manifest.Dependencies = append(p.manifest.Dependencies, path)
	}
	sort.Strings(p.manifest.Dependencies)
	return p, p.inventory()
}

// Create verifies a full isolated restore before publishing a recovery point.
// dueOnly is used by the supervisor and does no work before the daily deadline.
func Create(ctx context.Context, root string, dueOnly bool) (point Point, err error) {
	root, err = canonical(root)
	if err != nil {
		return point, err
	}
	p, err := LoadPolicy(root)
	if err != nil {
		err = errors.Join(err, recordPolicyFailure(root, err))
		return point, err
	}
	if dueOnly && !p.Enabled {
		return point, nil
	}
	lockDir := filepath.Join(root, "backup-state")
	if err = os.MkdirAll(lockDir, 0700); err != nil {
		return point, err
	}
	lock, err := os.OpenFile(filepath.Join(lockDir, "job.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return point, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return point, errors.New("another backup is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	s, err := ReadStatus(root)
	if err != nil {
		return point, err
	}
	if dueOnly && !s.LastSuccess.IsZero() && time.Since(s.LastSuccess) < 24*time.Hour {
		return point, nil
	}
	s.LastAttempt = time.Now().UTC()
	s.Enabled, s.Destination = p.Enabled, p.Destination
	defer func() {
		if err != nil {
			s.LastError = err.Error()
		}
		err = errors.Join(err, writeJSON(filepath.Join(lockDir, "status.json"), s))
	}()
	key, err := loadIdentity(p.IdentityFile)
	if err != nil {
		return point, err
	}
	data, err := collect(ctx, root)
	if err != nil {
		return point, err
	}
	canonicalManifest := data.manifest
	canonicalManifest.CreatedAt = time.Time{}
	b, err := json.Marshal(canonicalManifest)
	if err != nil {
		return point, err
	}
	name := "backup-" + digest(b) + ".tar.gz.age"
	if err = os.MkdirAll(p.Destination, 0700); err != nil {
		return point, err
	}
	path := filepath.Join(p.Destination, name)
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		f, e := os.CreateTemp(p.Destination, ".backup-pending-")
		if e != nil {
			return point, e
		}
		defer os.Remove(f.Name())
		e = encodeArchive(f, data, key)
		if e == nil {
			e = f.Sync()
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			return point, e
		}
		if _, e = Verify(ctx, f.Name(), p.IdentityFile); e != nil {
			return point, fmt.Errorf("restore verification failed: %w", e)
		}
		if e = os.Rename(f.Name(), path); e != nil {
			return point, e
		}
		d, e := os.Open(p.Destination)
		if e != nil {
			return point, e
		}
		e = errors.Join(d.Sync(), d.Close())
		if e != nil {
			return point, e
		}
	} else if err != nil {
		return point, err
	} else {
		if _, err = Verify(ctx, path, p.IdentityFile); err != nil {
			return point, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return point, err
	}
	now := time.Now().UTC()
	point = Point{CheckedAt: now, Archive: name, Bytes: info.Size(), VerifiedAt: now}
	points, err := List(root)
	if err != nil {
		return point, err
	}
	previous := append([]Point(nil), points...)
	points = append(points, point)
	location, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return point, err
	}
	points = retain(points, location)
	if err = writeJSON(filepath.Join(p.Destination, "checkpoints.json"), points); err != nil {
		return point, err
	}
	s.LastSuccess, s.LastError, s.Latest = now, "", &point
	keep := map[string]bool{}
	for _, v := range points {
		keep[v.Archive] = true
	}
	for _, v := range previous {
		if !keep[v.Archive] && ownedArchive(v.Archive) {
			if e := os.Remove(filepath.Join(p.Destination, v.Archive)); e != nil && !errors.Is(e, os.ErrNotExist) {
				return point, e
			}
		}
	}
	return point, nil
}
func ownedArchive(name string) bool {
	return len(name) == len("backup-")+64+len(".tar.gz.age") && strings.HasPrefix(name, "backup-") && strings.HasSuffix(name, ".tar.gz.age") && filepath.Base(name) == name
}
func retain(points []Point, location *time.Location) []Point {
	sort.SliceStable(points, func(i, j int) bool { return points[i].CheckedAt.Before(points[j].CheckedAt) })
	days, weeks := map[string]int{}, map[string]int{}
	for i, p := range points {
		t := p.CheckedAt.In(location)
		days[t.Format("2006-01-02")] = i
		y, w := t.ISOWeek()
		weeks[fmt.Sprintf("%04d-%02d", y, w)] = i
	}
	keep := map[int]bool{}
	for _, group := range []struct {
		values map[string]int
		n      int
	}{{days, 7}, {weeks, 4}} {
		keys := make([]string, 0, len(group.values))
		for k := range group.values {
			keys = append(keys, k)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
		for i, k := range keys {
			if i >= group.n {
				break
			}
			keep[group.values[k]] = true
		}
	}
	out := []Point{}
	for i, p := range points {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

// Run polls the daily deadline; restart performs one overdue run, never a backlog.
func Run(ctx context.Context, root string, report func(error)) {
	check := func() {
		p, e := LoadPolicy(root)
		if errors.Is(e, os.ErrNotExist) {
			return
		}
		if e == nil && !p.Enabled {
			return
		}
		if e == nil {
			_, e = Create(ctx, root, true)
		} else {
			e = errors.Join(e, recordPolicyFailure(root, e))
		}
		if e != nil && ctx.Err() == nil {
			report(e)
		}
	}
	check()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func recordPolicyFailure(root string, cause error) error {
	var status Status
	if e := loadJSON(filepath.Join(root, "backup-state", "status.json"), &status); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	status.LastAttempt = time.Now().UTC()
	status.LastError = cause.Error()
	return writeJSON(filepath.Join(root, "backup-state", "status.json"), status)
}
