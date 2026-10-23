package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The direct parent relationship cannot be confused by PID reuse. Launchers can
// also name their own caller, whose start time pins the observed process identity.
func chatCallerContext(ctx context.Context, parentPID, callerPID int, detached bool) (context.Context, context.CancelFunc, error) {
	if callerPID < 0 || callerPID == 1 || callerPID == os.Getpid() || (detached && callerPID != 0) {
		return nil, nil, errors.New("caller-pid must identify another process (>1) and cannot be combined with detach")
	}
	if detached {
		return ctx, func() {}, nil
	}
	var identity string
	if callerPID != 0 && callerPID != parentPID {
		var err error
		identity, err = callerIdentity(ctx, callerPID)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect supervising caller before execution: %w", err)
		}
	}
	child, cancel := context.WithCancelCause(ctx)
	check := func() bool {
		if parentPID <= 1 || os.Getppid() != parentPID {
			cancel(fmt.Errorf("caller exited: parent process %d is gone", parentPID))
			return false
		}
		if identity != "" {
			current, err := callerIdentity(child, callerPID)
			if child.Err() != nil {
				return false
			}
			if err != nil || current != identity {
				cancel(fmt.Errorf("caller exited or became unverifiable: supervising process %d", callerPID))
				return false
			}
		}
		return true
	}
	if check() {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-child.Done():
					return
				case <-ticker.C:
					if !check() {
						return
					}
				}
			}
		}()
	}
	return child, func() { cancel(context.Canceled) }, nil
}

func callerIdentity(ctx context.Context, pid int) (string, error) {
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "stat=", "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	raw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("caller process %d unavailable: %w", pid, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 6 || strings.Contains(fields[0], "Z") {
		return "", fmt.Errorf("caller process %d exited or has no start identity", pid)
	}
	return strings.Join(fields[1:], " "), nil
}
