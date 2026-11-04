package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func validateNativeSkillsRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, '\x00') {
		return errors.New("must be a clean absolute path")
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || actual != root {
		return errors.New("must exist without symlinks in its path")
	}
	if _, err := privateServicePath(root, true); err != nil {
		return err
	}
	skills := filepath.Join(root, "skills")
	if _, err := privateServicePath(skills, true); err != nil {
		return fmt.Errorf("skills directory: %w", err)
	}
	if _, err := os.ReadDir(skills); err != nil {
		return errors.New("skills directory must be readable")
	}
	return nil
}

// Explicit selections mount only named Skills from the configured source.
// Host-managed task Skills are assembled separately and never grant permissions.
func validateNativeSkillSelection(base string, names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00\r\n") || seen[name] {
			return errors.New("native skill selection requires distinct installed skill directory names")
		}
		seen[name] = true
		info, err := os.Stat(filepath.Join(base, "skills", name, "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("selected native skill is unavailable in the configured base profile")
		}
	}
	return nil
}

func nativeSkillSelectionID(names []string) string {
	ordered := append([]string{}, names...)
	sort.Strings(ordered)
	raw, _ := json.Marshal(ordered)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func nativeSkillSelectionFingerprint(base string, names []string) (string, error) {
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)
	digest := sha256.New()
	writeNativeSkillDigestField(digest, "exact-native-skills-v1")
	if len(ordered) > 0 {
		writeNativeSkillDigestField(digest, base)
	}
	for _, name := range ordered {
		writeNativeSkillDigestField(digest, name)
		root, err := filepath.EvalSymlinks(filepath.Join(base, "skills", name))
		if err != nil {
			return "", fmt.Errorf("resolve selected native skill %s: %w", name, err)
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("selected native skill %s must resolve to a directory", name)
		}
		writeNativeSkillDigestField(digest, root)
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("selected native skills must not contain nested symlinks")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("selected native skills must contain regular files only")
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			writeNativeSkillDigestField(digest, filepath.ToSlash(relative))
			writeNativeSkillDigestField(digest, fmt.Sprint(info.Mode().Perm()&0111))
			writeNativeSkillDigestField(digest, fmt.Sprint(info.Size()))
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			opened, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() || !opened.ModTime().Equal(info.ModTime()) {
				_ = file.Close()
				return errors.New("selected native skill file changed while opening")
			}
			_, copyErr := io.CopyN(digest, file, info.Size())
			var extra [1]byte
			if n, readErr := file.Read(extra[:]); copyErr == nil && (n != 0 || !errors.Is(readErr, io.EOF)) {
				copyErr = errors.New("selected native skill file changed while reading")
			}
			closed, statErr := file.Stat()
			if copyErr == nil && (statErr != nil || closed.Size() != opened.Size() || !closed.ModTime().Equal(opened.ModTime())) {
				copyErr = errors.New("selected native skill file changed while reading")
			}
			closeErr := file.Close()
			return errors.Join(copyErr, closeErr)
		})
		if err != nil {
			return "", fmt.Errorf("fingerprint selected native skill %s: %w", name, err)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeNativeSkillDigestField(digest hash.Hash, value string) {
	fmt.Fprintf(digest, "%d:", len(value))
	_, _ = io.WriteString(digest, value)
}

func mountSelectedNativeSkills(base, destination string, names []string) error {
	if err := validateNativeSkillSelection(base, names); err != nil {
		return err
	}
	target := filepath.Join(destination, "skills")
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	if _, err := privateServicePath(target, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	// Selection-specific directories are immutable; never delete local overrides.
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return errors.New("selected skill profile has unexpected local entries")
		}
	}
	for _, name := range names {
		source := filepath.Join(base, "skills", name)
		path := filepath.Join(target, name)
		if link, err := os.Readlink(path); err == nil && link == source {
			continue
		}
		if err := os.Symlink(source, path); err != nil {
			return errors.New("selected native skill conflicts with an existing profile entry")
		}
	}
	return nil
}
