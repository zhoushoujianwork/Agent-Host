package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skills are operator-installed capabilities, unlike audience conversation state.
// Inherit only the explicit base profile's skill directory. Never discover a
// different home profile implicitly or copy sessions, credentials or memory.
func inheritNativeSkills(base, destination string) error {
	source := filepath.Join(base, "skills")
	target := filepath.Join(destination, "skills")
	info, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		if link, linkErr := os.Readlink(target); linkErr == nil && link == source {
			return os.Remove(target)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("base native skills must be a directory")
	}
	if existing, err := os.Lstat(target); err == nil {
		if existing.IsDir() {
			// An audience-specific directory is an explicit local override.
			return nil
		}
		if link, err := os.Readlink(target); err == nil && link == source {
			return nil
		}
		return errors.New("native skills destination conflicts with base inheritance")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(source, target)
}

// Explicit selections only mount installed user skills from this native profile.
// Native commands/plugins, workspace skills and Host-selected memory skills remain native.
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

func mountSelectedNativeSkills(base, destination string, names []string) error {
	if err := validateNativeSkillSelection(base, names); err != nil {
		return err
	}
	target := filepath.Join(destination, "skills")
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
