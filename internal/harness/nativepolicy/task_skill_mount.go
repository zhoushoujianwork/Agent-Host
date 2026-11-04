package nativepolicy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
)

// mountTaskSkillPackage retains a verified task package at a stable path in the
// conversation environment. The run-scoped source remains unchanged evidence.
func mountTaskSkillPackage(environmentRoot, source, expectedDigest string) (string, error) {
	manifest, err := validateSkillPlugin(source, expectedDigest)
	if err != nil {
		return "", err
	}
	if err := validatePrivateCanonicalDirectory(environmentRoot); err != nil {
		return "", fmt.Errorf("native Skill environment: %w", err)
	}
	parent := filepath.Join(environmentRoot, "native-skills")
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create controlled native Skill parent: %w", err)
	}
	if err := validatePrivateCanonicalDirectory(parent); err != nil {
		return "", fmt.Errorf("controlled native Skill parent: %w", err)
	}
	destination := filepath.Join(parent, expectedDigest)
	if _, err := os.Lstat(destination); err == nil {
		if _, err := validateSkillPlugin(destination, expectedDigest); err != nil {
			return "", fmt.Errorf("existing controlled native Skill package: %w", err)
		}
		return destination, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	staging, err := os.MkdirTemp(parent, ".package-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := copyTaskSkillPackage(source, staging, manifest); err != nil {
		return "", err
	}
	if _, err := validateSkillPlugin(staging, expectedDigest); err != nil {
		return "", fmt.Errorf("validate controlled native Skill staging package: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		if _, err := validateSkillPlugin(destination, expectedDigest); err != nil {
			return "", fmt.Errorf("existing controlled native Skill package: %w", err)
		}
		return destination, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", fmt.Errorf("publish controlled native Skill package: %w", err)
	}
	if _, err := validateSkillPlugin(destination, expectedDigest); err != nil {
		return "", fmt.Errorf("validate controlled native Skill package: %w", err)
	}
	return destination, nil
}

func validatePrivateCanonicalDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return errors.New("directory must be a clean absolute path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("directory must exist without symlink components")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("directory must be private and not a symlink")
	}
	return nil
}

func copyTaskSkillPackage(source, destination string, manifest harness.SkillManifest) error {
	fileLimit, totalLimit := int64(16<<10), int64(65<<10)
	if manifest.Version == harness.ManagedSkillManifestVersion {
		fileLimit, totalLimit = managedskills.MaxFileBytes, managedskills.MaxCatalogBytes
	}
	var total int64
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("controlled native Skill package contains a symlink")
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("controlled native Skill package escaped its root")
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("controlled native Skill package contains a nonprivate or non-regular file")
		}
		limit := fileLimit
		if relative == "manifest.json" {
			limit = 1 << 20
		}
		body, err := readPrivateSkillFile(path, limit)
		if err != nil {
			return err
		}
		total += int64(len(body))
		if total > totalLimit+(1<<20) {
			return errors.New("controlled native Skill package exceeds its copy budget")
		}
		mode := fs.FileMode(0600)
		if info.Mode().Perm()&0111 != 0 {
			mode = 0700
		}
		return os.WriteFile(target, body, mode)
	})
}
