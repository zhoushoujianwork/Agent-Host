//go:build darwin || linux

package state

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// HostActive probes the existing ownership lock without creating a state home,
// deleting a lock, or modifying execution records. A free lock never proves
// business side effects stopped; callers must report an uncertain outcome.
func HostActive(home string) (bool, error) {
	path := filepath.Join(home, "host.lock")
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("invalid Host lock file")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	return false, syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
