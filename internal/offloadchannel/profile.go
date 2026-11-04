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
	"strings"
	"syscall"
	"time"

	"agent-host/internal/decision/jev"
	"agent-host/internal/mcpstdio"
	"agent-host/internal/offloadexec"
)

// FingerprintPrefix namespaces runtime fingerprints of this engine path.
const FingerprintPrefix = "offload:v1:"

const maxProfileBytes = 32 << 10

// Profile is the validated Offload execution profile of one channel runtime.
type Profile struct {
	Version int
	// ConfigPath selects Offload's private native config for version 2 Go calls.
	ConfigPath string
	library    *offloadexec.Library
	// Profile is the explicit Offload profile name passed to fresh runs.
	Profile       string
	Model         string
	Timeout       time.Duration
	MemoryEnabled bool
	Memory        MemoryConfig
}

// FromLibrary builds the default Host selector from an already resolved native
// Offload snapshot. It cannot select a CLI transport or enable memory implicitly.
func FromLibrary(library *offloadexec.Library, timeout time.Duration) Profile {
	return Profile{Version: 2, ConfigPath: library.ConfigPath(), Profile: library.ProfileName(), Timeout: timeout, library: library}
}

// MemoryConfig selects AGH memory components, not Offload tools or credentials.
type MemoryConfig struct {
	Processing bool          `json:"processing_enabled"`
	Provider   string        `json:"provider"`
	Endpoint   string        `json:"endpoint,omitempty"`
	Model      string        `json:"model"`
	Timeout    time.Duration `json:"timeout"`
}

type profileFile struct {
	Version    int    `json:"version"`
	Command    string `json:"command"`
	ConfigPath string `json:"config_path,omitempty"`
	Profile    string `json:"profile"`
	Model      string `json:"model,omitempty"`
	Timeout    string `json:"timeout"`
	Memory     *struct {
		Enabled    *bool  `json:"enabled"`
		Processing *bool  `json:"processing_enabled,omitempty"`
		Provider   string `json:"provider,omitempty"`
		Endpoint   string `json:"endpoint,omitempty"`
		Model      string `json:"model,omitempty"`
		Timeout    string `json:"timeout,omitempty"`
	} `json:"memory,omitempty"`
}

// Load reads a private Go package profile and its fingerprint.
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

// Parse validates profile JSON without contacting a provider. Version 2 loads
// the explicit native config into an in-memory, fingerprinted SDK snapshot;
// it does not run credential commands or persist credentials.
func Parse(raw []byte) (Profile, string, error) {
	var p Profile
	var f profileFile
	if err := mcpstdio.Decode(bytes.TrimSpace(raw), &f); err != nil {
		return p, "", errors.New("offload configuration must be one JSON object with documented fields only")
	}
	if f.Version != 2 {
		return p, "", errors.New("only version 2 Offload Go package profiles are supported; CLI transport has been removed")
	}
	if f.Memory != nil {
		if f.Memory.Enabled == nil {
			return p, "", errors.New("offload memory requires an explicit enabled value")
		}
	}
	timeout, err := time.ParseDuration(f.Timeout)
	if err != nil {
		return p, "", errors.New("offload timeout must be an explicit positive duration such as 30m")
	}
	p = Profile{Version: f.Version, Profile: f.Profile, Model: f.Model, Timeout: timeout}
	if f.Command != "" {
		return Profile{}, "", errors.New("Offload CLI command is no longer supported")
	}
	p.ConfigPath = f.ConfigPath
	p.library, err = offloadexec.NewLibrary(f.ConfigPath, f.Profile, f.Model, timeout)
	if err != nil {
		return Profile{}, "", err
	}
	if f.Memory != nil && *f.Memory.Enabled {
		p.MemoryEnabled = true
		p.Memory = MemoryConfig{Processing: true, Provider: "typesafe", Model: jev.DefaultModel, Timeout: 30 * time.Second}
		if f.Memory.Processing != nil {
			p.Memory.Processing = *f.Memory.Processing
		}
		if f.Memory.Provider != "" {
			p.Memory.Provider = f.Memory.Provider
		}
		if f.Memory.Model != "" {
			p.Memory.Model = f.Memory.Model
		}
		p.Memory.Endpoint = f.Memory.Endpoint
		if f.Memory.Timeout != "" {
			p.Memory.Timeout, err = time.ParseDuration(f.Memory.Timeout)
			if err != nil {
				return Profile{}, "", errors.New("memory timeout must be a positive duration")
			}
		}
		if p.Memory.Timeout <= 0 || p.Memory.Timeout > 5*time.Minute {
			return Profile{}, "", errors.New("memory timeout must be between zero and five minutes")
		}
		if p.Memory.Processing {
			switch p.Memory.Provider {
			case "typesafe":
				if p.Memory.Endpoint != "" {
					return Profile{}, "", errors.New("memory endpoint requires provider custom")
				}
			case "custom":
				if err := jev.ValidateEndpoint(p.Memory.Endpoint); err != nil {
					return Profile{}, "", err
				}
			default:
				return Profile{}, "", errors.New("memory provider must be typesafe or custom")
			}
			if strings.TrimSpace(p.Memory.Model) != p.Memory.Model || p.Memory.Model == "" || len(p.Memory.Model) > 256 || strings.ContainsAny(p.Memory.Model, "\x00\r\n") {
				return Profile{}, "", errors.New("invalid memory model")
			}
		}
	}
	return p, p.Fingerprint(), nil
}

// NewClient returns only the resolved in-process package client.
func (p Profile) NewClient() (offloadexec.Executor, error) {
	if p.library == nil {
		return nil, errors.New("Offload requires a loaded Go package profile")
	}
	return p.library, nil
}

// Fingerprint identifies every setting that changes how Offload is invoked.
func (p Profile) Fingerprint() string {
	var memoryConfig *MemoryConfig
	if p.MemoryEnabled {
		memoryConfig = &p.Memory
	}
	native := ""
	if p.library != nil {
		native = p.library.Fingerprint()
	}
	canonical, _ := json.Marshal(struct {
		Version                                             int
		ConfigPath, Profile, Model, Timeout, NativeSnapshot string
		Memory                                              *MemoryConfig
	}{p.Version, p.ConfigPath, p.Profile, p.Model, p.Timeout.String(), native, memoryConfig})
	sum := sha256.Sum256(canonical)
	return FingerprintPrefix + hex.EncodeToString(sum[:])
}
