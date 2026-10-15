package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type managedServiceProcess struct {
	parent, uid int
	command     string
}

var serviceProcessFields = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(.+?)\s*$`)

func inspectManagedServiceProcess(ctx context.Context, pid int) (managedServiceProcess, error) {
	raw, err := exec.CommandContext(ctx, "ps", "-ww", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "uid=", "-o", "args=").Output()
	if err != nil {
		return managedServiceProcess{}, errors.New("cannot verify the running service process")
	}
	fields := serviceProcessFields.FindStringSubmatch(strings.TrimSpace(string(raw)))
	if len(fields) != 4 {
		return managedServiceProcess{}, errors.New("cannot verify the running service process")
	}
	parent, _ := strconv.Atoi(fields[1])
	uid, _ := strconv.Atoi(fields[2])
	return managedServiceProcess{parent, uid, fields[3]}, nil
}

func sameManagedService(process managedServiceProcess, selector, path string) bool {
	if process.uid != os.Getuid() {
		return false
	}
	binary, err := os.Executable()
	if err != nil {
		return false
	}
	// make invokes ./bin/agent-host, whereas workers use the absolute executable.
	prefixes := []string{binary, os.Args[0]}
	for _, prefix := range prefixes {
		if strings.HasPrefix(process.command, prefix+" service run ") &&
			strings.Contains(" "+process.command+" ", " "+selector+" "+path+" ") {
			return true
		}
	}
	return false
}

func signalManagedRuntime(ctx context.Context, root, directory string) error {
	path := filepath.Join(root, "service.pid")
	if _, err := privateServicePath(path, false); err != nil {
		return errors.New("running runtime has no verifiable service PID; restart aborted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return errors.New("invalid running service PID; restart aborted")
	}
	worker, err := inspectManagedServiceProcess(ctx, pid)
	if err != nil || !sameManagedService(worker, "--root", root) {
		return errors.New("service PID does not identify this runtime; restart aborted")
	}
	// Stop the supervisor so it drains every worker and the workbench normally.
	// Older standalone runners may only provide a verifiable worker PID.
	if worker.parent > 1 && worker.parent != os.Getpid() {
		parent, err := inspectManagedServiceProcess(ctx, worker.parent)
		selector, selected := "--root", root
		if directory != "" {
			selector, selected = "--channels-dir", directory
			if filepath.Base(directory) == "config" {
				selector = "--config-dir"
			}
		}
		if err == nil && sameManagedService(parent, selector, selected) {
			pid = worker.parent
		}
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop running runtime: %w", err)
	}
	return nil
}

func lockChannelRuntimeForStart(ctx context.Context, root, directory string, restart bool, out io.Writer) (*os.File, error) {
	path := filepath.Join(root, "run.lock")
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if _, err := privateServicePath(path, false); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	acquire := func() error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
	if err := acquire(); err == nil {
		return f, nil
	} else if !errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, err
	}
	if !restart {
		f.Close()
		return nil, errors.New("runtime already running; use make run or service run --restart")
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := signalManagedRuntime(wait, root, directory); err != nil {
		// Shutdown may have finished between the failed flock and PID inspection.
		if acquire() == nil {
			return f, nil
		}
		f.Close()
		return nil, err
	}
	fmt.Fprintln(out, "Restarting Agent-Host runtime:", root)
	if err := waitForRuntimeLock(wait, acquire); err != nil {
		f.Close()
		return nil, fmt.Errorf("previous runtime did not release its lock; restart aborted: %w", err)
	}
	return f, nil
}

func waitForRuntimeLock(ctx context.Context, acquire func() error) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := acquire(); err == nil {
			return nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
