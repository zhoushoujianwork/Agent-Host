//go:build darwin || linux

package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var ErrSessionBusy = errors.New("another CLI owns this conversation; independent conversations can run concurrently")

// AcquireChat shares the home with other CLI chats. Exclusive channel workers,
// maintenance commands and older binaries still use the same host.lock inode.
func AcquireChat(home string) (func() error, error) {
	return acquireLock(home, "host.lock", syscall.LOCK_SH)
}

// AcquireChatInitialization serializes only database opening and recovery, never
// native execution. Context cancellation also cancels a waiting initializer.
func AcquireChatInitialization(ctx context.Context, home string) (func() error, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := acquireLock(home, "chat-init.lock", syscall.LOCK_EX)
		if err == nil || !errors.Is(err, syscall.EWOULDBLOCK) {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// AcquireChatSession protects one native conversation across CLI processes.
// Hashing prevents session identifiers from being interpreted as file paths.
func AcquireChatSession(home, sessionID string) (func() error, error) {
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	release, err := acquireLock(home, fmt.Sprintf("chat-session-%x.lock", sha256.Sum256([]byte(sessionID))), syscall.LOCK_EX)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, ErrSessionBusy
	}
	return release, err
}

// Acquire takes the OS advisory lock for one owning host. The kernel releases
// it if the process exits; the lock file deliberately stays at a stable inode.
func Acquire(home string) (func() error, error) {
	return acquireLock(home, "host.lock", syscall.LOCK_EX)
}

func acquireLock(home, name string, mode int) (func() error, error) {
	if home == "" {
		return nil, errors.New("state home is required")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("another host owns this state home: %w", err), f.Close())
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() { releaseErr = errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()) })
		return releaseErr
	}, nil
}
