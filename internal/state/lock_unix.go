//go:build darwin || linux

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Acquire takes the OS advisory lock for one owning host. The kernel releases
// it if the process exits; the lock file deliberately stays at a stable inode.
func Acquire(home string) (func() error, error) {
	if home == "" {
		return nil, errors.New("state home is required")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home, "host.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("another host owns this state home: %w", err), f.Close())
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() { releaseErr = errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()) })
		return releaseErr
	}, nil
}
