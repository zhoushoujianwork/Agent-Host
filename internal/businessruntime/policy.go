// Package businessruntime binds explicit operator-selected business resources.
// Policies contain paths only; credential contents never identify a policy.
package businessruntime

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const Manifest = "business-runtime.json"
const ownership = "business-runtime-policy.json"
const maxFile = 1 << 20
const maxTotal = 4 << 20

type HomeFile struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type Policy struct {
	HomeFiles    []HomeFile `json:"home_files,omitempty"`
	BinDirectory string     `json:"bin_directory,omitempty"`
}

func (p Policy) Empty() bool { return len(p.HomeFiles) == 0 && p.BinDirectory == "" }

func fail() error { return errors.New("business runtime binding validation or preparation failed") }

func canonical(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
		return fail()
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || actual != path {
		return fail()
	}
	info, err := os.Lstat(path)
	if err != nil || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fail()
	}
	return nil
}

func validTarget(target string) bool {
	if target == "" || len(target) > 1024 || filepath.IsAbs(target) || filepath.Clean(target) != target || strings.ContainsAny(target, "\\\x00\r\n") {
		return false
	}
	// Build Hub business authentication has one exact lowercase leaf exception.
	if target == ".claude/build-hub/token" {
		return true
	}
	for _, part := range strings.Split(target, "/") {
		name := strings.ToLower(part)
		if name == "." || name == ".." || name == ".claude" || name == ".codex" || name == ".agents" || name == "skills" || name == "claude.md" || name == "agents.md" || strings.HasPrefix(name, ".zsh") || strings.HasPrefix(name, ".bash") || name == ".profile" || name == ".zprofile" || name == ".zlogin" || name == ".zlogout" || name == ".pam_environment" || name == ".kshrc" || name == ".cshrc" || name == ".tcshrc" || name == ".login" || name == ".logout" || name == "fish" {
			return false
		}
	}
	return true
}

// readSource refuses symlinks, special files and changes during a bounded read.
func readSource(path string) ([]byte, error) {
	if canonical(path, false) != nil {
		return nil, fail()
	}
	parent, err := directoryRoot(filepath.Dir(path))
	if err != nil {
		return nil, fail()
	}
	defer parent.Close()
	before, err := parent.Lstat(filepath.Base(path))
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxFile {
		return nil, fail()
	}
	f, err := parent.Open(filepath.Base(path))
	if err != nil {
		return nil, fail()
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, fail()
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil || len(raw) > maxFile {
		return nil, fail()
	}
	after, err := f.Stat()
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fail()
	}
	return raw, nil
}

func (p Policy) Validate() error {
	if len(p.HomeFiles) > 16 {
		return fail()
	}
	if p.BinDirectory != "" && (strings.Contains(p.BinDirectory, ":") || canonical(p.BinDirectory, true) != nil) {
		return fail()
	}
	total := 0
	for i, binding := range p.HomeFiles {
		if !validTarget(binding.Target) {
			return fail()
		}
		for _, prior := range p.HomeFiles[:i] {
			current, previous := strings.ToLower(binding.Target), strings.ToLower(prior.Target)
			if current == previous || strings.HasPrefix(current, previous+"/") || strings.HasPrefix(previous, current+"/") {
				return fail()
			}
		}
		raw, err := readSource(binding.Source)
		if err != nil {
			return fail()
		}
		total += len(raw)
		if total > maxTotal {
			return fail()
		}
	}
	return nil
}

func (p Policy) encoded() []byte {
	p.HomeFiles = append([]HomeFile(nil), p.HomeFiles...)
	sort.Slice(p.HomeFiles, func(i, j int) bool { return p.HomeFiles[i].Target < p.HomeFiles[j].Target })
	raw, _ := json.Marshal(p)
	return raw
}

func (p Policy) ID() string {
	if p.Empty() {
		return ""
	}
	sum := sha256.Sum256(append([]byte("business-runtime-v1\n"), p.encoded()...))
	return hex.EncodeToString(sum[:])
}

func (p Policy) Guidance() string {
	if p.BinDirectory == "" {
		return ""
	}
	raw, _ := json.Marshal(p.BinDirectory)
	return "\nHost business tools directory: " + string(raw) + ". Use the exact configured tools there (absolute wrapper paths for non-login bash). Do not install packages per session or initiate relogin to repair tool discovery. This binding grants no additional authorization and permits no account or provider fallback.\n"
}

func privateRoot(path string) (*os.Root, error) {
	if canonical(path, true) != nil {
		return nil, fail()
	}
	r, err := directoryRoot(path)
	if err != nil {
		return nil, fail()
	}
	opened, err := r.Stat(".")
	if err != nil || opened.Mode().Perm()&0077 != 0 {
		r.Close()
		return nil, fail()
	}
	return r, nil
}

// Walk from a pinned filesystem root, refusing links at every directory boundary.
func directoryRoot(path string) (*os.Root, error) {
	volume := filepath.VolumeName(path) + string(filepath.Separator)
	r, err := os.OpenRoot(volume)
	if err != nil {
		return nil, fail()
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		info, err := r.Lstat(part)
		if err != nil || !info.IsDir() {
			r.Close()
			return nil, fail()
		}
		next, err := r.OpenRoot(part)
		r.Close()
		if err != nil {
			return nil, fail()
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, fail()
		}
		r = next
	}
	return r, nil
}

func privateRead(r *os.Root, name string) ([]byte, error) {
	info, err := r.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxFile {
		return nil, fail()
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, fail()
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fail()
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil || len(raw) > maxFile {
		return nil, fail()
	}
	return raw, nil
}

func atomicWrite(r *os.Root, name string, raw []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail()
	}
	tmp := filepath.Join(filepath.Dir(name), ".agh-binding-"+hex.EncodeToString(nonce[:]))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fail()
	}
	defer r.Remove(tmp)
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return fail()
	}
	if r.Rename(tmp, name) != nil {
		return fail()
	}
	return nil
}

// Prepare stores immutable, private path policy in a selection-specific profile.
func (p Policy) Prepare(profile string) error {
	if p.Empty() {
		return nil
	}
	if p.Validate() != nil {
		return fail()
	}
	r, err := privateRoot(profile)
	if err != nil {
		return fail()
	}
	defer r.Close()
	if _, err := r.Lstat(Manifest); err == nil {
		raw, err := privateRead(r, Manifest)
		if err != nil || !bytes.Equal(raw, p.encoded()) {
			return fail()
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	return atomicWrite(r, Manifest, p.encoded())
}

func Load(profile string) (Policy, error) {
	var p Policy
	if profile == "" {
		return p, nil
	}
	// An omitted policy adds no profile requirements to the previous contract.
	if _, err := os.Lstat(filepath.Join(profile, Manifest)); errors.Is(err, os.ErrNotExist) {
		if filepath.Base(filepath.Dir(profile)) == "business-sets" {
			return p, fail()
		}
		return p, nil
	}
	r, err := privateRoot(profile)
	if err != nil {
		return p, fail()
	}
	defer r.Close()
	if _, err := r.Lstat(Manifest); errors.Is(err, os.ErrNotExist) {
		if filepath.Base(filepath.Dir(profile)) == "business-sets" {
			return p, fail()
		}
		return p, nil
	}
	raw, err := privateRead(r, Manifest)
	if err != nil {
		return p, fail()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return Policy{}, fail()
	}
	if filepath.Base(filepath.Dir(profile)) == "business-sets" && p.ID() != filepath.Base(profile) {
		return Policy{}, fail()
	}
	if p.Validate() != nil {
		return Policy{}, fail()
	}
	return p, nil
}

// parentRoot holds a directory descriptor for each verified private parent.
func parentRoot(home *os.Root, target string) (*os.Root, error) {
	r, err := home.OpenRoot(".")
	if err != nil {
		return nil, fail()
	}
	for _, part := range strings.Split(filepath.Dir(target), "/") {
		if part == "." {
			continue
		}
		if err := r.Mkdir(part, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			r.Close()
			return nil, fail()
		}
		info, err := r.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			r.Close()
			return nil, fail()
		}
		next, err := r.OpenRoot(part)
		r.Close()
		if err != nil {
			return nil, fail()
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, fail()
		}
		r = next
	}
	return r, nil
}

// Apply refreshes only a recorded binding set. The caller must exclude extraction.
func (p Policy) Apply(environment string) error {
	if p.Empty() {
		if _, err := os.Lstat(filepath.Join(environment, ownership)); !errors.Is(err, os.ErrNotExist) {
			return fail()
		}
		return nil
	}
	if p.Validate() != nil {
		return fail()
	}
	r, err := privateRoot(environment)
	if err != nil {
		return fail()
	}
	defer r.Close()
	owned := false
	if _, err := r.Lstat(ownership); err == nil {
		raw, err := privateRead(r, ownership)
		if err != nil || !bytes.Equal(raw, p.encoded()) {
			return fail()
		}
		owned = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	homePath := filepath.Join(environment, "home")
	home, err := privateRoot(homePath)
	if err != nil {
		return fail()
	}
	defer home.Close()
	files := map[string][]byte{}
	total := 0
	for _, binding := range p.HomeFiles {
		raw, err := readSource(binding.Source)
		if err != nil {
			return fail()
		}
		total += len(raw)
		if total > maxTotal {
			return fail()
		}
		files[binding.Target] = raw
	}
	startup := map[string]bool{}
	if p.BinDirectory != "" {
		quoted := "'" + strings.ReplaceAll(p.BinDirectory, "'", "'\\''") + "'"
		body := []byte("# Host-owned business tools path; no global initialization.\nexport PATH=" + quoted + ":\"$PATH\"\n")
		for _, name := range []string{".zshenv", ".profile", ".bash_profile", ".bashrc"} {
			files[name] = body
			startup[name] = true
		}
	}
	// Preflight every destination before recording ownership or replacing a file.
	for target, raw := range files {
		parent, err := parentRoot(home, target)
		if err != nil {
			return fail()
		}
		info, err := parent.Lstat(filepath.Base(target))
		if err == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				parent.Close()
				return fail()
			}
			if startup[target] {
				old, err := privateRead(parent, filepath.Base(target))
				if err != nil || !bytes.Equal(old, raw) {
					parent.Close()
					return fail()
				}
			} else if !owned {
				parent.Close()
				return fail()
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			parent.Close()
			return fail()
		}
		parent.Close()
	}
	if !owned && atomicWrite(r, ownership, p.encoded()) != nil {
		return fail()
	}
	for target, raw := range files {
		parent, err := parentRoot(home, target)
		if err != nil {
			return fail()
		}
		err = atomicWrite(parent, filepath.Base(target), raw)
		parent.Close()
		if err != nil {
			return fail()
		}
	}
	return nil
}
