//go:build darwin || linux

package projectruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// The advisory lock spans database commit and atomic view exports, keeping
// concurrent entry processes from exporting an older state over a newer one.
func (c *Catalog) lock(ctx context.Context) (func(), error) {
	if c.Root == "" {
		return nil, errors.New("project runtime root is required")
	}
	if err := privateDirectory(c.directory()); err != nil {
		return nil, err
	}
	path := filepath.Join(c.directory(), "registry.lock")
	if err := regularRuntimeFile(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = f.Close()
			return nil, fmt.Errorf("shared project registry is busy: %w", err)
		case <-ticker.C:
		}
	}
}
