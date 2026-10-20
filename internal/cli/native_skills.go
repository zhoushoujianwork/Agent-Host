package cli

import (
	"errors"
	"os"
	"path/filepath"
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
