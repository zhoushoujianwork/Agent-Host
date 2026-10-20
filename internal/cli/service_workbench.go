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
func startServiceWorkbench(ctx context.Context, layout serviceLayout, python string, port int, browser bool, notice io.Writer, openURL func(context.Context, string) error) (func(), error) {
	return startServiceWorkbenchSources(ctx, layout, python, port, browser, notice, openURL, nil, "")
}

// An observer-only entry shares the documented CLI state home, without loading
// channel credentials or taking the chat writer's state lock.
func runCLIWorkbench(ctx context.Context, directory, source, python string, port int, noBrowser bool, notice io.Writer) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || filepath.Base(directory) != "config" {
		return errors.New("config-dir must be a clean absolute ROOT/config path")
	}
	if _, err := privateServicePath(directory, true); err != nil {
		return err
	}
	if actual, err := filepath.EvalSymlinks(directory); err != nil || actual != directory {
		return errors.New("configuration directory must not contain symlinks")
	}
	cliState, err := discoverCLIState(directory)
	if err != nil {
		return err
	}
	if err := validateChannelViewerSource(source); err != nil {
		return err
	}
	root := filepath.Dir(directory)
	if err := prepareChannelViewer(root, source); err != nil {
		return err
	}
	layout := serviceLayout{Root: root, Config: directory, State: cliState}
	stop, done, err := launchServiceWorkbenchSources(ctx, layout, python, port, !noBrowser, notice, openWorkbenchURL, nil, cliState)
	if err != nil {
		return err
	}
	defer stop()
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("workbench exited unexpectedly; inspect logs/viewer.log")
	}
}

// cliState, when non-empty, is the ordinary CLI chat state home. It is an
// additional read-only source, not a channel: it has no runtime lock, no
// supervised process and no nested "state" subdirectory, so it is forwarded
// to the viewer directly rather than through the channel Root+"/state" rule.
func startServiceWorkbenchSources(ctx context.Context, layout serviceLayout, python string, port int, browser bool, notice io.Writer, openURL func(context.Context, string) error, sources []configuredChannel, cliState string) (func(), error) {
	stop, _, err := launchServiceWorkbenchSources(ctx, layout, python, port, browser, notice, openURL, sources, cliState)
	return stop, err
}

func launchServiceWorkbenchSources(ctx context.Context, layout serviceLayout, python string, port int, browser bool, notice io.Writer, openURL func(context.Context, string) error, sources []configuredChannel, cliState string) (func(), <-chan struct{}, error) {
	script := filepath.Join(layout.Root, "viewer", "server.py")
	if _, err := privateServicePath(script, false); err != nil {
		return nil, nil, errors.New("installed viewer is missing or invalid; rerun make run")
	}
	if _, err := privateServicePath(filepath.Join(layout.Root, "logs"), true); err != nil {
		return nil, nil, errors.New("viewer logs directory must be private")
	}
	logPath := filepath.Join(layout.Root, "logs", "viewer.log")
	if _, err := os.Lstat(logPath); err == nil {
		if _, err = privateServicePath(logPath, false); err != nil {
			return nil, nil, errors.New("viewer log must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, errors.New("cannot inspect viewer log")
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, nil, errors.New("cannot open private viewer log")
	}
	childCtx, cancel := context.WithCancel(ctx)
	args := []string{"-B", script, "--home", layout.State, "--no-reports", "--all-chats", "--port", fmt.Sprint(port)}
	if directory := unifiedDirectory(layout.Root); directory != "" {
		args = append(args, "--backup-root", filepath.Dir(directory))
	}
	if binary, err := os.Executable(); err == nil {
		args = append(args, "--host-binary", binary)
	} else {
		cancel()
		_ = log.Close()
		return nil, nil, errors.New("cannot locate Host executable for memory inspection")
	}
	for _, source := range sources {
		args = append(args, "--channel-home", source.Name+"="+filepath.Join(source.Root, "state"))
	}
	if cliState != "" {
		args = append(args, "--channel-home", "cli="+cliState)
	}
	cmd := exec.CommandContext(childCtx, python, args...)
	// Let the viewer cancel and reap its owned Chat execution before exit.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 20 * time.Second
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
		return nil, nil, errors.New("cannot start Python workbench; inspect Python 3.10+ availability")
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
		return nil, nil, ctx.Err()
	case <-time.After(5 * time.Second):
		stop()
		return nil, nil, errors.New("workbench startup timed out; inspect logs/viewer.log")
	}
	address := strings.TrimPrefix(line, "Execution viewer: ")
	u, err := url.Parse(address)
	if err != nil || !strings.HasPrefix(line, "Execution viewer: ") || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		stop()
		return nil, nil, errors.New("workbench did not report a loopback URL; inspect logs/viewer.log")
	}
	fmt.Fprintf(notice, "Execution workbench: %s/ (memory: %s/memory.html)\n", address, address)
	if browser {
		if err := openURL(ctx, address+"/"); err != nil {
			fmt.Fprintln(notice, "Default browser could not be opened; use the printed workbench URL.")
		}
	}
	return stop, done, nil
}

func openWorkbenchURL(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		return exec.CommandContext(ctx, "/usr/bin/open", address).Run()
	}
	return exec.CommandContext(ctx, "xdg-open", address).Run()
}
