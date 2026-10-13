package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The observer has a separate log and inherits no managed provider credentials.
// Its lifetime follows the service; an observer failure does not replay work.
func startServiceWorkbench(ctx context.Context, layout serviceLayout, python string, browser bool, notice io.Writer, openURL func(context.Context, string) error) (func(), error) {
	script := filepath.Join(layout.Root, "viewer", "server.py")
	if _, err := privateServicePath(script, false); err != nil {
		return nil, errors.New("installed viewer is missing or invalid; rerun make run")
	}
	if _, err := privateServicePath(filepath.Join(layout.Root, "logs"), true); err != nil {
		return nil, errors.New("viewer logs directory must be private")
	}
	logPath := filepath.Join(layout.Root, "logs", "viewer.log")
	if _, err := os.Lstat(logPath); err == nil {
		if _, err = privateServicePath(logPath, false); err != nil {
			return nil, errors.New("viewer log must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect viewer log")
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, errors.New("cannot open private viewer log")
	}
	childCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(childCtx, python, "-B", script, "--home", layout.State, "--no-reports", "--port", "0")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		provider := false
		for _, excluded := range append(serviceEnvironmentKeys, serviceClearedEnvironmentKeys...) {
			provider = provider || key == excluded
		}
		if !provider {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stderr = log
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		log.Close()
		return nil, errors.New("cannot start Python workbench; inspect Python 3.10+ availability")
	}
	ready, done := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(done)
		defer log.Close()
		r := bufio.NewReader(stdout)
		line, _ := r.ReadString('\n')
		ready <- strings.TrimSpace(line)
		io.Copy(log, r)
		if err := cmd.Wait(); err != nil && childCtx.Err() == nil {
			fmt.Fprintln(log, "Workbench process exited unexpectedly:", err)
		}
	}()
	stop := func() { cancel(); <-done }
	var line string
	select {
	case line = <-ready:
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		stop()
		return nil, errors.New("workbench startup timed out; inspect logs/viewer.log")
	}
	address := strings.TrimPrefix(line, "Execution viewer: ")
	u, err := url.Parse(address)
	if err != nil || !strings.HasPrefix(line, "Execution viewer: ") || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		stop()
		return nil, errors.New("workbench did not report a loopback URL; inspect logs/viewer.log")
	}
	fmt.Fprintf(notice, "Execution workbench: %s/ (memory: %s/memory.html)\n", address, address)
	if browser {
		if err := openURL(ctx, address+"/"); err != nil {
			fmt.Fprintln(notice, "Default browser could not be opened; use the printed workbench URL.")
		}
	}
	return stop, nil
}

func openWorkbenchURL(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		return exec.CommandContext(ctx, "/usr/bin/open", address).Run()
	}
	return exec.CommandContext(ctx, "xdg-open", address).Run()
}
