// Package dream implements bounded, read-only inspection of finalized handoff
// evidence. It does not extract, publish, or persist memory.
package dream

import (
	"bytes"
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
	"syscall"
	"time"
)

const (
	PolicyVersion  = "dream-scan-v1"
	maxDirectories = 256
	maxRootEntries = 4096
	maxArtifacts   = 32
	maxFileBytes   = 8 << 20
	maxTotalBytes  = 32 << 20
)

type Destination struct {
	PersonID    string `json:"person_id"`
	Agent       string `json:"agent"`
	Audience    string `json:"audience"`
	MemoryScope string `json:"memory_scope"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	FinalizedAtUTC   string `json:"finalized_at_utc"`
	ExitCode         int    `json:"exit_code"`
	CanonicalRunID   string `json:"canonical_run_id,omitempty"`
	CanonicalSession string `json:"canonical_session_id,omitempty"`
	Files            []File `json:"files"`
	Digest           string `json:"digest"`
}

type Entry struct {
	Directory      string       `json:"directory"`
	Status         string       `json:"status"` // validated or diagnostic_only
	Eligible       bool         `json:"eligible"`
	Manifest       *Manifest    `json:"manifest,omitempty"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	Documents      []Document   `json:"-"`
}

type Document struct {
	Path string
	Body []byte
}

type Report struct {
	Version         string      `json:"version"`
	PolicyVersion   string      `json:"policy_version"`
	Source          string      `json:"source"`
	Destination     Destination `json:"destination"`
	Status          string      `json:"status"`
	WritesPerformed bool        `json:"writes_performed"`
	ModelInvoked    bool        `json:"model_invoked"`
	Entries         []Entry     `json:"entries"`
}

type Scanner struct {
	// afterRead exists only to make source-change rejection deterministic in tests.
	afterRead func(directory, name string)
}

func Scan(source string, destination Destination) (Report, error) {
	return (Scanner{}).Scan(source, destination)
}

func (s Scanner) Scan(source string, destination Destination) (Report, error) {
	r := Report{Version: PolicyVersion, PolicyVersion: PolicyVersion, Source: source,
		Destination: destination, Status: "scan_only", Entries: []Entry{}}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || strings.ContainsRune(source, 0) {
		return r, errors.New("dream scan source must be a clean absolute path")
	}
	if err := validateIdentity(destination); err != nil {
		return r, err
	}
	if err := privateDirectory(source); err != nil {
		return r, fmt.Errorf("dream scan source: %w", err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return r, err
	}
	if len(entries) > maxRootEntries {
		return r, errors.New("dream scan source exceeds root entry budget")
	}
	for _, item := range entries {
		if !strings.HasPrefix(item.Name(), "handoff-") {
			continue
		}
		if len(r.Entries) >= maxDirectories {
			return r, errors.New("dream scan source exceeds handoff directory budget")
		}
		r.Entries = append(r.Entries, s.scanDirectory(source, item.Name(), destination))
	}
	return r, nil
}

func validateIdentity(d Destination) error {
	for _, field := range []struct{ name, value string }{{"person", d.PersonID}, {"agent", d.Agent}, {"audience", d.Audience}, {"memory-scope", d.MemoryScope}} {
		name, value := field.name, field.value
		if strings.TrimSpace(value) == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("dream scan requires a bounded explicit --%s", name)
		}
	}
	return nil
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path must be a non-symlink directory")
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || actual != path {
		return errors.New("path must not traverse symlinks")
	}
	return privateOwner(info, true)
}

func privateOwner(info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("path must be owned by the current user")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("path must deny group and other access")
	}
	if !directory && stat.Nlink != 1 {
		return errors.New("artifact must not be hard linked")
	}
	return nil
}

func (s Scanner) scanDirectory(root, name string, destination Destination) Entry {
	out := Entry{Directory: name, Status: "diagnostic_only", Diagnostics: []Diagnostic{}}
	if name == "handoff-" || strings.ContainsAny(name, "/\\\x00") {
		return reject(out, "invalid_directory", "handoff directory name is invalid")
	}
	dir := filepath.Join(root, name)
	if err := privateDirectory(dir); err != nil {
		return reject(out, "unsafe_directory", err.Error())
	}
	manifest, launch, refs, documents, err := s.manifest(dir)
	if err != nil {
		return reject(out, diagnosticCode(err), err.Error())
	}
	manifest.CanonicalRunID, manifest.CanonicalSession = refs.run, refs.session
	manifest.Digest = digestManifest(manifest)
	out.Manifest = &manifest
	out.Documents = documents
	out.IdempotencyKey = idempotencyKey(manifest, refs, destination, dir)
	if *launch.ExitCode != 0 {
		return reject(out, "source_execution_failed", "nonzero process exit is diagnostic evidence only")
	}
	out.Status, out.Eligible = "validated", true
	return out
}

type launchRecord struct {
	ExitCode       *int   `json:"exit_code"`
	FinalizedAtUTC string `json:"finalized_at_utc"`
	StreamFormat   string `json:"stream_format"`
}

type sourceRefs struct{ run, session string }

func (s Scanner) manifest(dir string) (Manifest, launchRecord, sourceRefs, []Document, error) {
	var manifest Manifest
	var launch launchRecord
	before, err := directorySnapshot(dir)
	if err != nil {
		return manifest, launch, sourceRefs{}, nil, err
	}
	if len(before) > maxArtifacts {
		return manifest, launch, sourceRefs{}, nil, errors.New("handoff directory exceeds entry budget")
	}
	for _, name := range snapshotNames(before) {
		info := before[name]
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return manifest, launch, sourceRefs{}, nil, fmt.Errorf("unsafe artifact: %s", name)
		}
		if err := privateOwner(info, false); err != nil {
			return manifest, launch, sourceRefs{}, nil, fmt.Errorf("unsafe artifact %s: %w", name, err)
		}
	}
	data := map[string][]byte{}
	var total int64
	for _, name := range snapshotNames(before) {
		body, file, err := s.readStableFile(dir, name)
		if err != nil {
			return manifest, launch, sourceRefs{}, nil, err
		}
		total += file.Bytes
		if total > maxTotalBytes {
			return manifest, launch, sourceRefs{}, nil, errors.New("artifact set exceeds total size budget")
		}
		manifest.Files, data[name] = append(manifest.Files, file), body
	}
	for _, name := range []string{"launch.json", "request.txt"} {
		if data[name] == nil {
			return manifest, launch, sourceRefs{}, nil, fmt.Errorf("required artifact missing: %s", name)
		}
	}
	if err := decodeOne(data["launch.json"], &launch); err != nil {
		return manifest, launch, sourceRefs{}, nil, fmt.Errorf("invalid launch.json: %w", err)
	}
	if launch.ExitCode == nil || launch.FinalizedAtUTC == "" {
		return manifest, launch, sourceRefs{}, nil, errors.New("handoff is not finalized")
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, launch.FinalizedAtUTC)
	if err != nil {
		return manifest, launch, sourceRefs{}, nil, errors.New("launch finalization timestamp is invalid")
	}
	_, offset := parsedTime.Zone()
	if offset != 0 {
		return manifest, launch, sourceRefs{}, nil, errors.New("launch finalization timestamp must be UTC")
	}
	output := "events.jsonl"
	switch launch.StreamFormat {
	case "text":
		output = "stdout.txt"
	case "json", "result":
	default:
		return manifest, launch, sourceRefs{}, nil, errors.New("launch stream format is invalid")
	}
	if data[output] == nil {
		return manifest, launch, sourceRefs{}, nil, fmt.Errorf("required artifact missing: %s", output)
	}
	after, err := directorySnapshot(dir)
	if err != nil || privateDirectory(dir) != nil || !sameSnapshot(before, after) {
		return manifest, launch, sourceRefs{}, nil, errors.New("source changed while scanning")
	}
	refs, err := structuredRefs(data)
	if err != nil {
		return manifest, launch, sourceRefs{}, nil, err
	}
	manifest.ExitCode, manifest.FinalizedAtUTC = *launch.ExitCode, launch.FinalizedAtUTC
	documents := make([]Document, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		documents = append(documents, Document{Path: file.Path, Body: append([]byte(nil), data[file.Path]...)})
	}
	return manifest, launch, refs, documents, nil
}

func (s Scanner) readStableFile(dir, name string) ([]byte, File, error) {
	path := filepath.Join(dir, name)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, File{}, fmt.Errorf("unsafe artifact: %s", name)
	}
	if err := privateOwner(before, false); err != nil {
		return nil, File{}, fmt.Errorf("unsafe artifact %s: %w", name, err)
	}
	if before.Size() > maxFileBytes {
		return nil, File{}, fmt.Errorf("artifact exceeds per-file size budget: %s", name)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, File{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		syscall.Close(fd)
		return nil, File{}, errors.New("open stable artifact")
	}
	body, readErr := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	opened, statErr := f.Stat()
	closeErr := f.Close()
	if s.afterRead != nil {
		s.afterRead(dir, name)
	}
	after, pathErr := os.Lstat(path)
	if err := errors.Join(readErr, statErr, closeErr, pathErr); err != nil {
		return nil, File{}, err
	}
	if len(body) > maxFileBytes || !os.SameFile(before, opened) || !os.SameFile(opened, after) || before.Size() != opened.Size() || opened.Size() != after.Size() || before.ModTime() != opened.ModTime() || opened.ModTime() != after.ModTime() {
		return nil, File{}, errors.New("source changed while scanning")
	}
	sum := sha256.Sum256(body)
	return body, File{Path: name, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func directorySnapshot(dir string) (map[string]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out[entry.Name()] = info
	}
	return out, nil
}

func snapshotNames(snapshot map[string]os.FileInfo) []string {
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sameSnapshot(a, b map[string]os.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for name, x := range a {
		y, ok := b[name]
		if !ok || !os.SameFile(x, y) || x.Mode() != y.Mode() || x.Size() != y.Size() || x.ModTime() != y.ModTime() {
			return false
		}
		xs, xok := x.Sys().(*syscall.Stat_t)
		ys, yok := y.Sys().(*syscall.Stat_t)
		if !xok || !yok || xs.Uid != ys.Uid || xs.Nlink != ys.Nlink {
			return false
		}
	}
	return true
}

func decodeOne(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func digestManifest(m Manifest) string {
	copy := m
	copy.Digest = ""
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func idempotencyKey(m Manifest, refs sourceRefs, destination Destination, source string) string {
	parts := []string{PolicyVersion, source, destination.PersonID, destination.Agent, destination.Audience, destination.MemoryScope, m.Digest, refs.run, refs.session}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func reject(e Entry, code, message string) Entry {
	e.Diagnostics = append(e.Diagnostics, Diagnostic{Code: code, Message: message})
	return e
}

func diagnosticCode(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "changed while scanning"):
		return "source_changed"
	case strings.Contains(message, "size budget"):
		return "size_budget_exceeded"
	case strings.Contains(message, "not finalized"):
		return "not_finalized"
	case strings.Contains(message, "missing"):
		return "missing_artifact"
	case strings.Contains(message, "unsafe") || strings.Contains(message, "owned") || strings.Contains(message, "private") || strings.Contains(message, "hard linked"):
		return "unsafe_source"
	default:
		return "invalid_source"
	}
}
