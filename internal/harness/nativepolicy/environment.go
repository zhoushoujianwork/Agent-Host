package nativepolicy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-host/internal/harness"
	"github.com/zhoushoujianwork/offload/pkg/execution"
)

// executionEnvironment binds durable native state to a Host conversation. Calls
// without a Host identity (including extraction) receive a disposable root.
func executionEnvironment(req harness.Request) (execution.Environment, func(), error) {
	cleanup := func() {}
	if req.Extraction && req.Resume {
		return execution.Environment{}, cleanup, errors.New("extraction cannot resume a native environment")
	}
	if req.Resume && req.HostSessionID == "" {
		return execution.Environment{}, cleanup, errors.New("resume requires the original Host session identity")
	}
	if req.Extraction || req.HostSessionID == "" {
		env, err := execution.NewEnvironment()
		if err != nil {
			return env, cleanup, err
		}
		return env, func() { _ = os.RemoveAll(env.Root) }, nil
	}
	if req.NativeProfile == "" || !filepath.IsAbs(req.NativeProfile) || filepath.Clean(req.NativeProfile) != req.NativeProfile || strings.ContainsRune(req.NativeProfile, 0) {
		return execution.Environment{}, cleanup, errors.New("persistent native execution requires a clean absolute Host profile")
	}
	if !safeHostSessionID(req.HostSessionID) {
		return execution.Environment{}, cleanup, errors.New("Host session identity must be one safe path segment")
	}
	canonicalProfile, err := filepath.EvalSymlinks(req.NativeProfile)
	if err != nil || canonicalProfile != req.NativeProfile {
		return execution.Environment{}, cleanup, errors.New("Host profile must exist and contain no symlink components")
	}
	profileInfo, err := os.Lstat(req.NativeProfile)
	if err != nil || !profileInfo.IsDir() || profileInfo.Mode()&os.ModeSymlink != 0 || profileInfo.Mode().Perm()&0077 != 0 {
		return execution.Environment{}, cleanup, errors.New("Host profile must be a private directory")
	}
	base := filepath.Join(req.NativeProfile, "environments")
	if err = os.Mkdir(base, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return execution.Environment{}, cleanup, fmt.Errorf("create Host environment parent: %w", err)
	}
	baseInfo, err := os.Lstat(base)
	if err != nil || !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 || baseInfo.Mode().Perm()&0077 != 0 {
		return execution.Environment{}, cleanup, errors.New("Host environment parent must be a private directory")
	}
	root := filepath.Join(base, req.HostSessionID)
	if req.Resume {
		if err := validateExistingEnvironment(root); err != nil {
			return execution.Environment{}, cleanup, fmt.Errorf("resume Host environment: %w", err)
		}
		return execution.Environment{Root: root, Workspace: filepath.Join(root, "workspace")}, cleanup, nil
	}
	env, err := execution.NewEnvironmentAt(root)
	if err != nil {
		return env, cleanup, fmt.Errorf("create Host environment: %w", err)
	}
	return env, cleanup, nil
}

func safeHostSessionID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && id != "." && id != ".." && filepath.Base(id) == id && filepath.Clean(id) == id && !strings.ContainsAny(id, "/\\\x00\r\n")
}

func validateExistingEnvironment(root string) error {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return errors.New("original environment is missing or no longer canonical")
	}
	for _, name := range []string{"", "workspace", "home", "codex", "claude", "config", "cache", "data"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("original environment is missing or not private")
		}
	}
	info, err := os.Lstat(filepath.Join(root, "environment.json"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("original environment identity is missing or not private")
	}
	identity, err := os.ReadFile(filepath.Join(root, "environment.json"))
	if err != nil || string(identity) != `{"version":1}` {
		return errors.New("original environment identity is incompatible")
	}
	return nil
}
