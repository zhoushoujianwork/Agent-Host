//go:build unix

package nativepolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/businessruntime"
)

func TestBusinessRuntimeNativeBoundary(t *testing.T) {
	if _, err := os.Stat("/bin/zsh"); err != nil {
		t.Skip("zsh unavailable")
	}
	adapter, req := fixtureAdapter(t, "business_runtime")
	profile, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profile, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(profile, "synthetic-source")
	if err := os.WriteFile(source, []byte("synthetic-native-auth"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(profile, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$HOME\" != " + shellQuote(os.Getenv("HOME")) + " ] || exit 21\n[ \"$(cat \"$HOME/.tccli/test.credential\")\" = synthetic-native-auth ] || exit 22\n[ ! -e \"$HOME/.claude/skills\" ] || exit 23\n[ ! -e \"$HOME/.agents/skills\" ] || exit 24\n[ -z \"${POP_TOKEN-}\" ] || exit 25\nprintf BUSINESS_BOUNDARY_OK\n"
	if err := os.WriteFile(filepath.Join(bin, "fixture-business"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := businessruntime.Policy{HomeFiles: []businessruntime.HomeFile{{Source: source, Target: ".tccli/test.credential"}}, BinDirectory: bin}
	if err := p.Prepare(profile); err != nil {
		t.Fatal(err)
	}
	req.NativeProfile, req.HostSessionID = profile, "boundary-business"
	req.PermissionMode = "bypassPermissions"
	t.Setenv("POP_TOKEN", "ambient-must-not-inherit")
	result, err := adapter.Run(t.Context(), req, nil)
	if err != nil || result.Status != "completed" {
		t.Fatal("isolated fake native boundary failed", result.Status, err)
	}
}
