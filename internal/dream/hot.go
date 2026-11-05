package dream

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"agent-host/internal/harness"
)

func PresetDocuments(bundle HOTBundle) []harness.PresetDocument {
	documents := []harness.PresetDocument{}
	if body := bundle.Documents["USER.md"]; body != "" {
		documents = append(documents, harness.PresetDocument{Name: "USERS.md", Content: []byte(body)})
	}
	memory := bundle.Documents["MEMORY.md"]
	if tasks := bundle.Documents["TASKS.md"]; tasks != "" {
		memory += "\n# Active tasks\n\n" + tasks
	}
	if memory != "" {
		documents = append(documents, harness.PresetDocument{Name: "MEMORY.md", Content: []byte(memory)})
	}
	return documents
}

func PublishHOT(cacheRoot string, bundle HOTBundle) (string, error) {
	if !filepath.IsAbs(cacheRoot) || filepath.Clean(cacheRoot) != cacheRoot || bundle.Digest == "" || hotBundleDigest(bundle) != bundle.Digest {
		return "", errors.New("HOT publication requires absolute cache root and digest")
	}
	canonical, err := filepath.EvalSymlinks(cacheRoot)
	if err != nil || canonical != cacheRoot {
		return "", errors.New("HOT cache root must be canonical without symlinks")
	}
	if err := validatePrivateDir(cacheRoot); err != nil {
		return "", errors.New("HOT cache root must be owner-private")
	}
	partition := hashStrings(bundle.Partition.PersonID, bundle.Partition.AgentID, bundle.Partition.AudienceID, bundle.Partition.Scope)[:24]
	dreamRoot, err := ensurePrivateChild(cacheRoot, "dream-hot")
	if err != nil {
		return "", err
	}
	root, err := ensurePrivateChild(dreamRoot, partition)
	if err != nil {
		return "", err
	}
	final := filepath.Join(root, bundle.Digest)
	manifest, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	manifest = append(manifest, '\n')
	if _, err := os.Lstat(final); err == nil {
		if err = verifyHOTSnapshot(final, bundle, manifest); err != nil {
			return "", err
		}
		return final, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, ".publish-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err = os.Chmod(tmp, 0700); err != nil {
		return "", err
	}
	for _, name := range []string{"USER.md", "MEMORY.md", "TASKS.md"} {
		if strings.Contains(bundle.Documents[name], "\x00") {
			return "", errors.New("invalid HOT document")
		}
		if err = os.WriteFile(filepath.Join(tmp, name), []byte(bundle.Documents[name]), 0600); err != nil {
			return "", err
		}
	}
	if err = os.WriteFile(filepath.Join(tmp, "manifest.json"), manifest, 0600); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, final); err != nil {
		if verifyErr := verifyHOTSnapshot(final, bundle, manifest); verifyErr == nil {
			return final, nil
		}
		return "", err
	}
	if err = verifyHOTSnapshot(final, bundle, manifest); err != nil {
		return "", err
	}
	return final, nil
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("not a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("directory has another owner")
	}
	return nil
}

func ensurePrivateChild(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", errors.New("HOT publication path contains a symlink")
	}
	if err := validatePrivateDir(path); err != nil {
		return "", err
	}
	return path, nil
}

func verifyHOTSnapshot(path string, bundle HOTBundle, manifest []byte) error {
	if err := validatePrivateDir(path); err != nil {
		return errors.New("existing HOT snapshot is unsafe or corrupt")
	}
	expected := map[string][]byte{
		"USER.md": []byte(bundle.Documents["USER.md"]), "MEMORY.md": []byte(bundle.Documents["MEMORY.md"]),
		"TASKS.md": []byte(bundle.Documents["TASKS.md"]), "manifest.json": manifest,
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != len(expected) {
		return errors.New("existing HOT snapshot is unsafe or corrupt")
	}
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok {
			return errors.New("existing HOT snapshot contains unexpected files")
		}
		name := filepath.Join(path, entry.Name())
		info, statErr := os.Lstat(name)
		stat, statOK := infoSys(info)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !statOK || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
			return errors.New("existing HOT snapshot contains unsafe files")
		}
		body, readErr := os.ReadFile(name)
		if readErr != nil || !bytes.Equal(body, want) {
			return errors.New("existing HOT snapshot content does not match its digest")
		}
	}
	return nil
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
