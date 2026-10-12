package assembly

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Claude 2.1.223 loads cwd project settings and merges cwd local settings with
// canonical Git-root local settings. The latter requires a non-home root owned
// by the current user. Discover it from metadata, without invoking Git or hooks.
func nativeLocalSettingsRoot(workspace string) (string, error) {
	root := workspace
	for {
		entry := filepath.Join(root, ".git")
		info, err := os.Lstat(entry)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				actual, err := resolveNativeLocalPath(entry)
				if err != nil {
					return "", err
				}
				info, err = os.Stat(actual)
				if err != nil {
					return "", err
				}
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return "", fmt.Errorf("Git metadata %s must be a directory or regular file", entry)
			}
			if !info.IsDir() {
				root, err = nativeCanonicalGitRoot(root)
				if err != nil {
					return "", err
				}
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect Git metadata %s: %w", entry, err)
		}
		if filepath.Dir(root) == root {
			return workspace, nil
		}
		root = filepath.Dir(root)
	}
	if root == workspace || os.Geteuid() < 0 {
		return workspace, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve native settings home: %w", err)
	}
	home, err = resolveNativeLocalPath(home)
	if err != nil {
		return "", fmt.Errorf("resolve native settings home: %w", err)
	}
	if root == home {
		return workspace, nil
	}
	for i, path := range []string{root, filepath.Join(root, ".git"), filepath.Join(root, ".claude")} {
		info, err := os.Lstat(path)
		if i == 0 {
			// Native follows the root itself but checks the ownership of the
			// .git and .claude entries without following their final links.
			info, err = os.Stat(path)
		}
		if errors.Is(err, os.ErrNotExist) && i > 0 {
			if i == 1 {
				return workspace, nil
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect native settings ownership %s: %w", path, err)
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(owner.Uid) != os.Geteuid() {
			return workspace, nil
		}
	}
	return root, nil
}

// A submodule stays at its own root. Only linked-worktree metadata with a
// matching backlink selects the common repository, as native Claude does.
func nativeCanonicalGitRoot(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return "", fmt.Errorf("read Git metadata: %w", err)
	}
	value := strings.TrimSpace(string(data))
	if !strings.HasPrefix(value, "gitdir:") {
		return root, nil
	}
	gitdir, err := nativeMetadataPath(root, strings.TrimSpace(strings.TrimPrefix(value, "gitdir:")))
	if err != nil {
		return "", err
	}
	commonRef, present, err := readNativeGitMetadata(filepath.Join(gitdir, "commondir"))
	if err != nil || !present {
		return root, err
	}
	common, err := nativeMetadataPath(gitdir, commonRef)
	if err != nil {
		return "", err
	}
	if filepath.Dir(gitdir) != filepath.Join(common, "worktrees") {
		return root, nil
	}
	backRef, present, err := readNativeGitMetadata(filepath.Join(gitdir, "gitdir"))
	if err != nil || !present {
		return root, err
	}
	back, err := nativeMetadataPath(gitdir, backRef)
	if err != nil {
		return "", err
	}
	back, err = resolveNativeLocalPath(back)
	if err != nil {
		return "", err
	}
	if back != filepath.Join(root, ".git") {
		return root, nil
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	return common, nil
}

func readNativeGitMetadata(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect Git metadata %s: %w", path, err)
	}
	// Native does not follow links for the commondir and backlink entries.
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, nil
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("Git metadata %s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read Git metadata %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func nativeMetadataPath(base, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("empty native Git metadata path")
	}
	if nativeNetworkPath(ref) {
		return "", errors.New("native Git metadata references a network path")
	}
	if !filepath.IsAbs(ref) {
		ref = filepath.Join(base, ref)
	}
	if _, err := resolveNativeLocalPath(ref); err != nil {
		return "", err
	}
	// Native compares the resolved lexical metadata paths; only the worktree
	// backlink is realpathed. Preserve aliases for those structural checks.
	return filepath.Clean(ref), nil
}

func nativeNetworkPath(path string) bool {
	if len(path) >= 2 && strings.ContainsRune(`/\`, rune(path[0])) && strings.ContainsRune(`/\`, rune(path[1])) {
		return true
	}
	path = strings.ToLower(filepath.Clean(path))
	return path == "/net" || strings.HasPrefix(path, "/net/")
}

// Inspect each link before following it and reject native's explicit network
// path forms. Paths are otherwise canonicalized as native paths.
func resolveNativeLocalPath(path string) (string, error) {
	if nativeNetworkPath(path) {
		return "", errors.New("native Git metadata references a network path")
	}
	// Native applies path.resolve before realpath. Clean the input once, then
	// preserve dot segments inside link targets while resolving their components.
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	resolved := string(filepath.Separator)
	links := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		candidate := filepath.Join(resolved, part)
		if nativeNetworkPath(candidate) {
			return "", errors.New("native Git metadata references a network path")
		}
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve native Git path %s: %w", candidate, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if len(parts) > 0 && !info.IsDir() {
				return "", fmt.Errorf("native Git path %s must be a directory", candidate)
			}
			resolved = candidate
			continue
		}
		links++
		if links > 40 {
			return "", errors.New("too many links in native Git metadata path")
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", fmt.Errorf("read native Git path link %s: %w", candidate, err)
		}
		if nativeNetworkPath(target) {
			return "", errors.New("native Git metadata references a network path")
		}
		if filepath.IsAbs(target) {
			resolved = string(filepath.Separator)
		}
		parts = append(strings.Split(target, string(filepath.Separator)), parts...)
	}
	return resolved, nil
}
