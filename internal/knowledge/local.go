package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxLocalFileBytes   = 2 << 20
	maxLocalScanBytes   = 32 << 20
	maxLocalScanFiles   = 1024
	maxLocalScanEntries = 4096
)

func validLocalPath(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.IndexFunc(segment, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func isMarkdown(value string) bool {
	ext := strings.ToLower(path.Ext(value))
	return ext == ".md" || ext == ".markdown"
}

// Startup validates directory and allowlist metadata only. Document bodies are
// read exclusively in response to search/read operations.
func validateLocalSource(s Source) error {
	info, err := os.Lstat(s.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("local Markdown root must be an existing canonical directory")
	}
	actual, err := filepath.EvalSymlinks(s.Root)
	if err != nil || actual != s.Root {
		return errors.New("local Markdown root must use its canonical path")
	}
	for _, relative := range s.Paths {
		info, err := localPathInfo(s, relative)
		if err != nil {
			return err
		}
		if !info.IsDir() && (!info.Mode().IsRegular() || !isMarkdown(relative)) {
			return errors.New("local Markdown paths must identify directories or regular Markdown files")
		}
	}
	return nil
}

// Every component is inspected, including the root, and no symlink is followed.
// File identity is checked again after opening and after the bounded read.
func localPathInfo(s Source, relative string) (os.FileInfo, error) {
	if !validLocalPath(relative) {
		return nil, errors.New("local Markdown requires a clean relative path without hidden segments or traversal")
	}
	current := s.Root
	info, err := os.Lstat(current)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("local Markdown root is unavailable or unsafe")
	}
	actual, err := filepath.EvalSymlinks(current)
	if err != nil || actual != current {
		return nil, errors.New("local Markdown root canonical path changed")
	}
	parts := strings.Split(relative, "/")
	for i, segment := range parts {
		current = filepath.Join(current, segment)
		info, err = os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("local Markdown path is unavailable or contains a symlink")
		}
	}
	return info, nil
}

func localPathAllowed(s Source, relative string) bool {
	if !validLocalPath(relative) || !isMarkdown(relative) {
		return false
	}
	for _, allowed := range s.Paths {
		if relative == allowed || strings.HasPrefix(relative, allowed+"/") {
			return true
		}
	}
	return false
}

// A rooted open also prevents an ancestor rename/symlink race from escaping
// the configured root between the component checks and opening the file.
func openLocalPath(s Source, relative string) (*os.File, error) {
	before, err := os.Lstat(s.Root)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("local Markdown root is unavailable or unsafe")
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, errors.New("local Markdown root cannot be opened")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	current, pathErr := os.Lstat(s.Root)
	if err != nil || pathErr != nil || !os.SameFile(before, opened) || !os.SameFile(opened, current) {
		return nil, errors.New("local Markdown root changed while opening")
	}
	f, err := root.Open(relative)
	if err != nil {
		return nil, errors.New("local Markdown path cannot be opened within the configured root")
	}
	return f, nil
}

func readLocalFile(ctx context.Context, s Source, relative string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !localPathAllowed(s, relative) {
		return "", errors.New("local Markdown document is outside the configured paths")
	}
	before, err := localPathInfo(s, relative)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() > maxLocalFileBytes {
		return "", errors.New("local Markdown document must be a regular file within the 2 MiB size limit")
	}
	f, err := openLocalPath(s, relative)
	if err != nil {
		return "", errors.New("local Markdown document cannot be opened")
	}
	defer f.Close()
	opened, err := f.Stat()
	current, pathErr := localPathInfo(s, relative)
	if err != nil || pathErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, current) || before.Size() != opened.Size() || !before.ModTime().Equal(opened.ModTime()) {
		return "", errors.New("local Markdown document changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxLocalFileBytes+1))
	if err != nil || len(raw) > maxLocalFileBytes {
		return "", errors.New("local Markdown document exceeds its bounded read or cannot be read")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	end, err := f.Stat()
	current, pathErr = localPathInfo(s, relative)
	if err != nil || pathErr != nil || !os.SameFile(opened, current) || end.Size() != int64(len(raw)) || !opened.ModTime().Equal(end.ModTime()) {
		return "", errors.New("local Markdown document changed during read")
	}
	if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return "", errors.New("local Markdown document must contain UTF-8 text without NUL bytes")
	}
	return string(raw), nil
}

type localScan struct {
	entries, files, bytes int
	truncated             bool
	seen                  map[string]bool
	paths                 []string
}

func (scan *localScan) collect(ctx context.Context, source Source, relative string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if scan.seen[relative] || scan.truncated {
		return nil
	}
	scan.seen[relative] = true
	if scan.entries >= maxLocalScanEntries {
		scan.truncated = true
		return nil
	}
	scan.entries++
	info, err := localPathInfo(source, relative)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if isMarkdown(relative) {
			if !info.Mode().IsRegular() {
				return errors.New("local Markdown scan encountered an unsafe document")
			}
			if len(scan.paths) >= maxLocalScanFiles {
				scan.truncated = true
				return nil
			}
			scan.paths = append(scan.paths, relative)
		}
		return nil
	}
	directory, err := openLocalPath(source, relative)
	if err != nil {
		return errors.New("local Markdown scan cannot open a configured directory")
	}
	defer directory.Close()
	opened, err := directory.Stat()
	current, pathErr := localPathInfo(source, relative)
	if err != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(opened, current) {
		return errors.New("local Markdown directory changed during scan")
	}
	for !scan.truncated {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := directory.ReadDir(64)
		if err != nil && !errors.Is(err, io.EOF) {
			return errors.New("local Markdown directory enumeration failed")
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				if scan.entries >= maxLocalScanEntries {
					scan.truncated = true
					break
				}
				scan.entries++
				continue
			}
			if err := scan.collect(ctx, source, path.Join(relative, entry.Name())); err != nil {
				return err
			}
			if scan.truncated {
				break
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	current, err = localPathInfo(source, relative)
	if err != nil || !os.SameFile(opened, current) {
		return errors.New("local Markdown directory changed during scan")
	}
	return nil
}

func localTitle(body, relative string) string {
	for _, line := range strings.SplitN(body, "\n", 64) {
		if strings.HasPrefix(line, "# ") {
			return boundedLocalText(strings.TrimSpace(strings.TrimPrefix(line, "# ")), 256)
		}
	}
	return boundedLocalText(path.Base(relative), 256)
}

func boundedLocalText(text string, limit int) string {
	characters := []rune(text)
	return string(characters[:min(len(characters), limit)])
}

func localSnippet(body string, terms []string) string {
	characters := []rune(body)
	lower := []rune(strings.ToLower(body))
	start := 0
	for _, term := range terms {
		if index := strings.Index(string(lower), term); index >= 0 {
			start = max(utf8.RuneCountInString(string(lower)[:index])-100, 0)
			break
		}
	}
	return string(characters[start:min(start+500, len(characters))])
}

func (c *sourceClient) searchLocalMarkdown(ctx context.Context, query string, limit int) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 2000 || !utf8.ValidString(query) || strings.IndexFunc(query, unicode.IsControl) >= 0 || limit < 1 || limit > 20 {
		return nil, errors.New("local Markdown search requires a query and a limit of 1..20")
	}
	if err := validateLocalSource(c.source.Source); err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) > 32 {
		return nil, errors.New("local Markdown search supports at most 32 terms")
	}
	scan := localScan{seen: map[string]bool{}}
	for _, relative := range c.source.Source.Paths {
		if err := scan.collect(ctx, c.source.Source, relative); err != nil {
			return nil, err
		}
	}
	sort.Strings(scan.paths)
	type hit struct {
		score int
		path  string
		value map[string]any
	}
	var hits []hit
	for _, relative := range scan.paths {
		info, err := localPathInfo(c.source.Source, relative)
		if err != nil {
			return nil, err
		}
		if info.Size() > maxLocalFileBytes {
			return nil, errors.New("local Markdown scan encountered a document exceeding the 2 MiB size limit")
		}
		if scan.bytes+int(info.Size()) > maxLocalScanBytes {
			scan.truncated = true
			break
		}
		body, err := readLocalFile(ctx, c.source.Source, relative)
		if err != nil {
			return nil, err
		}
		scan.files++
		scan.bytes += len(body)
		lower := strings.ToLower(body)
		score := 0
		for _, term := range terms {
			if strings.Contains(lower, term) || strings.Contains(strings.ToLower(relative), term) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		digest := sha256.Sum256([]byte(body))
		hits = append(hits, hit{score, relative, map[string]any{"path": relative, "title": localTitle(body, relative), "snippet": localSnippet(body, terms), "content_sha256": hex.EncodeToString(digest[:])}})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].path < hits[j].path
	})
	rows := make([]any, 0, min(limit, len(hits)))
	for _, item := range hits[:min(limit, len(hits))] {
		rows = append(rows, item.value)
	}
	return c.receipt(map[string]any{"results": rows, "returned_count": len(rows)}, map[string]any{
		"visibility": "configured_paths", "coverage": "bounded_local_markdown_scan", "query": query, "query_mode": "case_insensitive_any_term", "paths": c.source.Source.Paths,
		"scanned_entries": scan.entries, "scanned_files": scan.files, "scanned_bytes": scan.bytes, "scan_limit_reached": scan.truncated,
		"truncated": scan.truncated || len(hits) > limit, "matched_in_scan": len(hits), "returned_count": len(rows), "limit": limit,
		"max_scan_entries": maxLocalScanEntries, "max_scan_files": maxLocalScanFiles, "max_scan_bytes": maxLocalScanBytes,
		"limitation": "Only authorized Markdown files encountered in this bounded scan were searched; hidden paths are excluded. Zero matches do not establish global absence or current runtime state.",
	})
}

func (c *sourceClient) readLocalMarkdown(ctx context.Context, relative string, offset, limit int, expectedDigest string) (map[string]any, error) {
	if err := validateReadWindow(offset, limit, expectedDigest); err != nil {
		return nil, err
	}
	body, err := readLocalFile(ctx, c.source.Source, relative)
	if err != nil {
		return nil, err
	}
	excerpt, encoding, coverage, err := contentExcerpt(body, offset, limit, expectedDigest)
	if err != nil {
		return nil, err
	}
	coverage["visibility"], coverage["digest_scope"] = "configured_paths", "local_document_utf8"
	coverage["path"] = relative
	return c.receipt(map[string]any{"path": relative, "title": localTitle(body, relative), "content": excerpt, "content_encoding": encoding, "content_format": "markdown"}, coverage)
}
