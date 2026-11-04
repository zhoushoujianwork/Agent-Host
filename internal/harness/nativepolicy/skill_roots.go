package nativepolicy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// approvedSkillRoots resolves each explicitly selected Skill. Audience profiles
// may link selected roots, but links inside a Skill are never trusted.
func approvedSkillRoots(profile, taskPackage string) ([]string, error) {
	type source struct {
		root string
		task bool
	}
	var sources []source
	if profile != "" {
		sources = append(sources, source{root: filepath.Join(profile, "skills")})
	}
	if taskPackage != "" {
		sources = append(sources, source{root: filepath.Join(taskPackage, "skills"), task: true})
	}
	byName := map[string]string{}
	for _, source := range sources {
		entries, err := os.ReadDir(source.root)
		if errors.Is(err, os.ErrNotExist) && !source.task {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00\r\n") {
				return nil, errors.New("native Skill has an unsafe directory name")
			}
			root, err := filepath.EvalSymlinks(filepath.Join(source.root, name))
			if err != nil {
				return nil, fmt.Errorf("resolve native Skill %s: %w", name, err)
			}
			info, err := os.Stat(root)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("native Skill %s is not a directory", name)
			}
			if info, err = os.Stat(filepath.Join(root, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("native Skill %s has no regular SKILL.md", name)
			}
			if prior := byName[name]; prior != "" && prior != root {
				return nil, fmt.Errorf("native Skill identity %s is duplicated", name)
			}
			byName[name] = root
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	roots := make([]string, 0, len(names))
	for _, name := range names {
		roots = append(roots, byName[name])
	}
	return roots, nil
}

func controlledClaudePlugin(roots []string) (string, func(), error) {
	if len(roots) == 0 {
		return "", func() {}, nil
	}
	bundle, err := os.MkdirTemp("", "agh-native-skills-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(bundle) }
	if err = os.Chmod(bundle, 0700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	plugin := filepath.Join(bundle, ".claude-plugin")
	if err = os.Mkdir(plugin, 0700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	declaration := []byte("{\"name\":\"agent-host-controlled\",\"version\":\"1.0.0\",\"description\":\"Host-selected Skills\"}\n")
	if err = os.WriteFile(filepath.Join(plugin, "plugin.json"), declaration, 0600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	destination := filepath.Join(bundle, "skills")
	if err = os.Mkdir(destination, 0700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	for _, root := range roots {
		if err = copyControlledSkill(root, filepath.Join(destination, filepath.Base(root))); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("copy native Skill %s: %w", filepath.Base(root), err)
		}
	}
	return bundle, cleanup, nil
}

func copyControlledSkill(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("links inside a native Skill are not allowed")
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("native Skill path escaped its root")
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if !info.Mode().IsRegular() {
			return errors.New("native Skill contains a non-regular file")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0600)
		if info.Mode().Perm()&0111 != 0 {
			mode = 0700
		}
		return os.WriteFile(target, body, mode)
	})
}
