// Package dingtalkworkspace embeds the official DWS CLI behind isolated workers.
package dingtalkworkspace

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	official "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/edition"

	"agent-host/internal/mcpstdio"
)

const Version = "v1.0.61"
const WorkerFlag = "--dingtalk-workspace-worker"

type Request struct {
	Config      string   `json:"config"`
	Fingerprint string   `json:"fingerprint"`
	Args        []string `json:"args"`
}

// Worker must run before the normal Host CLI. Official globals belong only to
// this process; neither os.Args nor edition hooks are changed in the Host.
func Worker(in io.Reader) (int, error) {
	raw, err := io.ReadAll(io.LimitReader(in, 65537))
	var r Request
	if err != nil || len(raw) > 65536 || mcpstdio.Decode(raw, &r) != nil {
		return 1, errors.New("invalid embedded DingTalk worker request")
	}
	c, fp, err := Load(r.Config)
	if err != nil {
		return 1, err
	}
	if r.Fingerprint != fp {
		return 1, errors.New("DingTalk workspace configuration changed")
	}
	if len(r.Args) == 0 {
		return 1, errors.New("missing DingTalk worker command")
	}
	if err := privateDirectory(c.StateDirectory); err != nil {
		return 1, err
	}
	keychain := filepath.Join(c.StateDirectory, "keychain")
	if err := privateDirectory(keychain); err != nil {
		return 1, err
	}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "DWS_") {
			_ = os.Unsetenv(key)
		}
	}
	_ = os.Setenv("DWS_KEYCHAIN_DIR", keychain)
	_ = os.Setenv("DWS_DISABLE_KEYCHAIN", "1")
	// Use the official encrypted file store in this channel's private directory.
	hooks := *edition.Get()
	hooks.Name = "agent-host"
	hooks.ConfigDir = func() string { return c.StateDirectory }
	hooks.IsEmbedded = true
	edition.Override(&hooks)
	official.SetVersion(strings.TrimPrefix(Version, "v"), "embedded", "50eb73a0")
	os.Args = append([]string{"agent-host-dingtalk"}, r.Args...)
	return official.Execute(), nil
}

func privateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("DingTalk state directory must be a clean absolute path")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("cannot create DingTalk state directory")
	}
	actual, err := filepath.EvalSymlinks(path)
	info, statErr := os.Lstat(path)
	if err != nil || actual != path || statErr != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("DingTalk state directory must be private and must not use symlinks")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("DingTalk state directory owner mismatch")
	}
	return nil
}

func encodeRequest(r Request) []byte { b, _ := json.Marshal(r); return b }
