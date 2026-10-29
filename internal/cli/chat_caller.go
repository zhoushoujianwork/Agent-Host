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
	return chatCallerContextWithProbe(ctx, parentPID, callerPID, detached, callerIdentity)
}

func chatCallerContextWithProbe(ctx context.Context, parentPID, callerPID int, detached bool, probe func(context.Context, int) (string, error)) (context.Context, context.CancelFunc, error) {
	if callerPID < 0 || callerPID == 1 || callerPID == os.Getpid() || (detached && callerPID != 0) {
		return nil, nil, errors.New("caller-pid must identify another process (>1) and cannot be combined with detach")
	}
	if detached {
		return ctx, func() {}, nil
	}
	var identity string
	if callerPID != 0 && callerPID != parentPID {
		var err error
		identity, err = probe(ctx, callerPID)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect supervising caller before execution: %w", err)
		}
	}
	child, cancel := context.WithCancelCause(ctx)
	check := func() bool {
		if observedParent := os.Getppid(); parentPID <= 1 || observedParent != parentPID {
			cancel(fmt.Errorf("caller exited: parent process %d selected by direct parent relationship; observed parent=%d", parentPID, observedParent))
			return false
		}
		if identity != "" {
			current, err := probe(child, callerPID)
			if child.Err() != nil {
				return false
			}
			if err != nil {
				cancel(fmt.Errorf("caller exited or became unverifiable: supervising process %d selected by caller-pid; initial_start=%q; probe failed: %w", callerPID, identity, err))
				return false
			}
			if current != identity {
				cancel(fmt.Errorf("caller exited or became unverifiable: supervising process %d selected by caller-pid; start identity changed: initial=%q observed=%q", callerPID, identity, current))
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
		if probe.Err() != nil {
			return "", fmt.Errorf("caller process %d ps probe interrupted: %w", pid, probe.Err())
		}
		return "", fmt.Errorf("caller process %d ps probe failed: %w", pid, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) > 0 && strings.Contains(fields[0], "Z") {
		return "", fmt.Errorf("caller process %d is a zombie", pid)
	}
	if len(fields) < 6 {
		return "", fmt.Errorf("caller process %d has no parseable start identity", pid)
	}
	return strings.Join(fields[1:], " "), nil
}
