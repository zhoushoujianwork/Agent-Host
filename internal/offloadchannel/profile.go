package offloadchannel

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
	"syscall"
	"time"

	"agent-host/internal/mcpstdio"
	"agent-host/internal/offloadexec"
)

// FingerprintPrefix namespaces runtime fingerprints of this engine path.
const FingerprintPrefix = "offload:v1:"

const maxProfileBytes = 32 << 10

// Profile is the validated Offload execution profile of one channel runtime.
type Profile struct {
	Version int
	// Command is the resolved absolute Offload executable.
	Command string
	// Profile is the explicit Offload profile name passed to fresh runs.
	Profile string
	Model   string
	Timeout time.Duration
	// MemoryEnabled is always false: current Offload cannot supply Host memory.
	MemoryEnabled bool
}

type profileFile struct {
	Version int    `json:"version"`
	Command string `json:"command"`
	Profile string `json:"profile"`
	Model   string `json:"model,omitempty"`
	Timeout string `json:"timeout"`
	Memory  *struct {
		Enabled *bool `json:"enabled"`
	} `json:"memory,omitempty"`
}

// Load reads a private version 1 profile and returns it with its fingerprint.
func Load(path string) (Profile, string, error) {
	var p Profile
	if !filepath.IsAbs(path) {
		return p, "", errors.New("offload configuration must be an absolute path")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return p, "", errors.New("offload configuration must be an existing regular file, not a symlink")
	}
	if owner, ok := before.Sys().(*syscall.Stat_t); !ok || int(owner.Uid) != os.Geteuid() || before.Mode().Perm()&0077 != 0 {
		return p, "", errors.New("offload configuration must belong to the current user and deny group/other access")
	}
	file, err := os.Open(path)
	if err != nil {
		return p, "", errors.New("cannot open offload configuration")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return p, "", errors.New("offload configuration changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxProfileBytes+1))
	if err != nil || len(raw) > maxProfileBytes {
		return p, "", fmt.Errorf("offload configuration could not be read within %d bytes", maxProfileBytes)
	}
	return Parse(raw)
}

// Parse validates profile JSON. It does not read credentials or Offload's own
// configuration, and it never contacts a provider.
func Parse(raw []byte) (Profile, string, error) {
	var p Profile
	var f profileFile
	if err := mcpstdio.Decode(bytes.TrimSpace(raw), &f); err != nil {
		return p, "", errors.New("offload configuration must be one JSON object with documented fields only")
	}
	if f.Version != 1 {
		return p, "", errors.New("offload configuration requires version 1")
	}
	if f.Memory != nil {
		if f.Memory.Enabled == nil {
			return p, "", errors.New("offload memory requires an explicit enabled value")
		}
		if *f.Memory.Enabled {
			return p, "", errors.New("offload memory.enabled true is not implemented: the installed Offload cannot provide Host memory tools or hooks")
		}
	}
	timeout, err := time.ParseDuration(f.Timeout)
	if err != nil {
		return p, "", errors.New("offload timeout must be an explicit positive duration such as 30m")
	}
	if !filepath.IsAbs(f.Command) || filepath.Clean(f.Command) != f.Command {
		return p, "", errors.New("offload command must be a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(f.Command)
	if err != nil {
		return p, "", errors.New("configured Offload executable is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return p, "", errors.New("configured Offload command must be an executable regular file")
	}
	p = Profile{Version: 1, Command: resolved, Profile: f.Profile, Model: f.Model, Timeout: timeout}
	if err := p.ClientConfig().Validate(); err != nil {
		return Profile{}, "", err
	}
	return p, p.Fingerprint(), nil
}

// ClientConfig returns the executor configuration for this profile.
func (p Profile) ClientConfig() offloadexec.Config {
	return offloadexec.Config{Command: p.Command, Profile: p.Profile, Model: p.Model, Timeout: p.Timeout}
}

// Fingerprint identifies every setting that changes how Offload is invoked.
func (p Profile) Fingerprint() string {
	canonical, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Command string `json:"command"`
		Profile string `json:"profile"`
		Model   string `json:"model"`
		Timeout string `json:"timeout"`
		Memory  bool   `json:"memory"`
	}{p.Version, p.Command, p.Profile, p.Model, p.Timeout.String(), p.MemoryEnabled})
	sum := sha256.Sum256(canonical)
	return FingerprintPrefix + hex.EncodeToString(sum[:])
}
