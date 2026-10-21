// Package acp implements the Host's private ACP execution boundary.
package acp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const ClaudeBridgeVersion = "0.81.2"

type Config struct {
	Version        int      `json:"version"`
	Transport      string   `json:"transport"`
	Engine         string   `json:"engine"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	AdapterVersion string   `json:"adapter_version"`
}

func Load(path string) (Config, string, error) {
	var c Config
	if !filepath.IsAbs(path) {
		return c, "", errors.New("execution_config must be an absolute path")
	}
	file, err := os.Open(path)
	if err != nil {
		return c, "", fmt.Errorf("open execution configuration: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return c, "", errors.New("execution configuration must be a regular file of at most 64 KiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return c, "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, "", errors.New("invalid execution configuration fields")
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return c, "", errors.New("execution configuration has trailing content")
	}
	if c.Version != 1 || c.Transport != "acp" || c.Engine != "claude" || c.AdapterVersion != ClaudeBridgeVersion {
		return c, "", errors.New("execution profile requires version 1, transport acp, engine claude and adapter_version " + ClaudeBridgeVersion)
	}
	if !filepath.IsAbs(c.Command) || strings.ContainsAny(c.Command, "\x00\r\n") || len(c.Args) > 32 {
		return c, "", errors.New("ACP requires an absolute executable and at most 32 arguments")
	}
	resolved, err := exec.LookPath(c.Command)
	if err != nil {
		return c, "", errors.New("configured ACP executable is unavailable")
	}
	c.Command, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return c, "", err
	}
	for _, arg := range c.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
			return c, "", errors.New("invalid ACP argument")
		}
	}
	canonical, _ := json.Marshal(c)
	// Renew Host sessions created before receipt-bound MCP authority was stable.
	sum := sha256.Sum256(append([]byte("host-acp-receipt-bindings-v2\n"), canonical...))
	return c, hex.EncodeToString(sum[:]), nil
}
