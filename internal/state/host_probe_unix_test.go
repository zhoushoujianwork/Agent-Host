//go:build darwin || linux

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostProbeDoesNotCreateOrReplaceOwnership(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if _, err := HostActive(home); err == nil {
		t.Fatal("missing lock treated as established ownership")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("probe created state home")
	}
	release, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if active, err := HostActive(home); err != nil || !active {
		t.Fatal(active, err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	if active, err := HostActive(home); err != nil || active {
		t.Fatal(active, err)
	}
}
