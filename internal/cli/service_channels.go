package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"agent-host/internal/backup"
)

type channelRegistration struct {
	Enabled     *bool  `json:"enabled"`
	RuntimeRoot string `json:"runtime_root"`
}

type configuredChannel struct{ Name, Root string }

func discoverServiceChannels(directory, fallback string) ([]configuredChannel, error) {
	if filepath.Base(directory) == "config" {
		return discoverUnifiedChannels(directory)
	}
	if directory == "" {
		return []configuredChannel{{"runtime", fallback}}, nil
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("channels-dir must be a clean absolute path")
	}
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		// Preserve the single-runtime setup only when no channel directory exists.
		return []configuredChannel{{"runtime", fallback}}, nil
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil || canonical != directory {
		return nil, errors.New("channel directory must not contain symlinks")
	}
	if _, err := privateServicePath(directory, true); err != nil {
		return nil, fmt.Errorf("channel directory: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var channels []configuredChannel
	roots := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "channel-") {
			if strings.HasPrefix(entry.Name(), "data-") || strings.HasPrefix(entry.Name(), "component-") || strings.HasPrefix(entry.Name(), "credential-") {
				continue
			}
			return nil, errors.New("channel registration filenames must use channel-*.json")
		}
		if entry.Name() == "channel-.json" {
			return nil, errors.New("channel registration requires a name after channel-")
		}
		var config channelRegistration
		if err := readServiceJSON(filepath.Join(directory, entry.Name()), &config); err != nil {
			return nil, fmt.Errorf("channel %s: %w", entry.Name(), err)
		}
		if config.Enabled == nil || strings.TrimSpace(config.RuntimeRoot) == "" || strings.ContainsAny(config.RuntimeRoot, "\x00\r\n") {
			return nil, errors.New("channel configuration requires enabled and runtime_root")
		}
		if !*config.Enabled {
			continue
		}
		root := config.RuntimeRoot
		if !filepath.IsAbs(root) {
			root = filepath.Join(directory, root)
		}
		root = filepath.Clean(root)
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil || canonical != root {
			return nil, errors.New("enabled channel runtime must exist without symlinks")
		}
		if roots[root] {
			return nil, errors.New("enabled channels must use distinct runtime roots")
		}
		roots[root] = true
		channels = append(channels, configuredChannel{strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "channel-"), ".json"), root})
	}
	if len(channels) == 0 {
		return nil, errors.New("no enabled channel configurations")
	}
	return channels, nil
}

func lockChannelRuntime(root string) (*os.File, error) {
	return lockChannelRuntimeForStart(context.Background(), root, "", false, io.Discard)
}

var channelViewerFiles = []string{"server.py", "memory_view.py", "index.html", "executions.html", "app.js", "style.css", "memory.html", "memory.js", "memory.css", "settings_store.py", "settings.html", "settings.js", "settings.css", "agent_profiles.py", "agent-profiles.js", "agent-profiles.css", "platform_skills.py", "platform-skills.js", "platform-skills.css", "chat_runtime.py", "session_chat.py", "session_client.py", "markdown-parser.js", "markdown.js", "chat.js", "workspace.js", "workspace.css", "theme.js", "theme.css", "pages.css", "embedded.css", "logo.svg", "shortcuts.js", "settings-fields.js", "settings-simple.css"}

func validateChannelViewerSource(source string) error {
	if !filepath.IsAbs(source) {
		return errors.New("viewer-source must be an absolute path")
	}
	for _, name := range channelViewerFiles {
		if _, err := os.ReadFile(filepath.Join(source, name)); err != nil {
			return err
		}
	}
	return nil
}

func prepareChannelViewer(root, source string) error {
	if !filepath.IsAbs(source) {
		return errors.New("viewer-source must be an absolute path")
	}
	for _, name := range []string{"logs", "viewer"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if _, err := privateServicePath(path, true); err != nil {
			return err
		}
	}
	for _, name := range channelViewerFiles {
		target := filepath.Join(root, "viewer", name)
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			if _, err := privateServicePath(target, false); err != nil {
				return err
			}
		}
		raw, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(target), ".viewer-")
		if err != nil {
			return err
		}
		_, writeErr := f.Write(raw)
		closeErr := f.Close()
		if err = errors.Join(writeErr, closeErr); err == nil {
			err = os.Rename(f.Name(), target)
		}
		os.Remove(f.Name())
		if err != nil {
			return err
		}
	}
	return nil
}

type synchronizedChannelOutput struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w synchronizedChannelOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// discoverCLIState finds the ordinary CLI chat state home for the fixed,
// documented ROOT/cli-state path beside a unified ROOT/config directory. It
// is read-only discovery: an absent directory is still mounted so the viewer
// can observe the first chat without a restart. A custom --home used by an ad
// hoc "agent-host chat" is never auto-discovered; only the default path is.
func discoverCLIState(directory string) (string, error) {
	if filepath.Base(directory) != "config" {
		return "", nil
	}
	cliState := filepath.Join(filepath.Dir(directory), "cli-state")
	if _, err := os.Lstat(cliState); errors.Is(err, os.ErrNotExist) {
		return cliState, nil
	} else if err != nil {
		return "", fmt.Errorf("inspect CLI chat state: %w", err)
	}
	if _, err := privateServicePath(cliState, true); err != nil {
		return "", fmt.Errorf("CLI chat state directory: %w", err)
	}
	if actual, err := filepath.EvalSymlinks(cliState); err != nil || actual != cliState {
		return "", errors.New("CLI chat state must not contain symlinks")
	}
	if _, err := os.Lstat(filepath.Join(cliState, "state.db")); !errors.Is(err, os.ErrNotExist) {
		if _, err := privateServicePath(filepath.Join(cliState, "state.db"), false); err != nil {
			return "", fmt.Errorf("CLI chat database: %w", err)
		}
	}
	return cliState, nil
}

func runConfiguredChannels(ctx context.Context, command, directory, fallback, viewerSource, viewerPython string, viewerPort int, noBrowser, restart bool, out, errout io.Writer) error {
	channels, err := discoverServiceChannels(directory, fallback)
	if err != nil {
		return err
	}
	if command == "check" {
		for _, channel := range channels {
			if err := runService(ctx, []string{"check", "--root", channel.Root}, out, errout); err != nil {
				return fmt.Errorf("channel %s: %w", channel.Name, err)
			}
		}
		return nil
	}
	if filepath.Base(directory) == "config" {
		for _, channel := range channels {
			if err := prepareUnifiedRuntime(channel.Root); err != nil {
				return err
			}
		}
	}
	// Validate every enabled configuration before starting any connection.
	var locks []*os.File
	defer func() {
		for _, lock := range locks {
			lock.Close()
		}
	}()
	for _, channel := range channels {
		if err := runService(ctx, []string{"check", "--root", channel.Root}, io.Discard, errout); err != nil {
			return fmt.Errorf("channel %s: %w", channel.Name, err)
		}
	}
	// Read assets before stopping the old instance; write only after locking.
	if err := validateChannelViewerSource(viewerSource); err != nil {
		return err
	}
	for _, channel := range channels {
		lock, err := lockChannelRuntimeForStart(ctx, channel.Root, directory, restart, errout)
		if err != nil {
			return fmt.Errorf("channel %s: %w", channel.Name, err)
		}
		locks = append(locks, lock)
	}
	for _, channel := range channels {
		if err := prepareChannelViewer(channel.Root, viewerSource); err != nil {
			return err
		}
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	layout, _, _, err := loadService(channels[0].Root)
	if err != nil {
		return err
	}
	cliState, err := discoverCLIState(directory)
	if err != nil {
		return err
	}
	stop, err := startServiceWorkbenchSources(ctx, layout, viewerPython, viewerPort, !noBrowser, errout, openWorkbenchURL, channels, cliState)
	if err != nil {
		fmt.Fprintf(errout, "Workbench unavailable: %v; channel startup will continue.\n", err)
	} else {
		defer stop()
	}
	if filepath.Base(directory) == "config" {
		backupCtx, cancelBackup := context.WithCancel(ctx)
		backupDone := make(chan struct{})
		go func() {
			defer close(backupDone)
			backup.Run(backupCtx, filepath.Dir(directory), func(err error) {
				fmt.Fprintf(errout, "Automatic backup failed: %v\n", err)
			})
		}()
		defer func() { cancelBackup(); <-backupDone }()
	}
	return superviseChannelProcesses(ctx, channels, binary, viewerPython, noBrowser, out, errout)
}

func superviseChannelProcesses(ctx context.Context, channels []configuredChannel, binary, viewerPython string, noBrowser bool, out, errout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(channels))
	started := 0
	var mu sync.Mutex
	defer func() {
		cancel()
		for i := 0; i < started; i++ {
			<-results
		}
	}()
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return nil
		}
		logPath := filepath.Join(channel.Root, "logs", "service.ndjson")
		pidPath := filepath.Join(channel.Root, "service.pid")
		for _, path := range []string{logPath, pidPath} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				if _, err := privateServicePath(path, false); err != nil {
					return err
				}
			}
		}
		log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		args := []string{"service", "run", "--root", channel.Root, "--viewer-python", viewerPython, "--no-workbench"}
		if noBrowser {
			args = append(args, "--no-browser")
		}
		child := exec.CommandContext(ctx, binary, args...)
		child.Stdout = io.MultiWriter(log, synchronizedChannelOutput{&mu, out})
		child.Stderr = io.MultiWriter(log, synchronizedChannelOutput{&mu, errout})
		child.Cancel = func() error { return child.Process.Signal(syscall.SIGTERM) }
		child.WaitDelay = 15 * time.Second
		if err := child.Start(); err != nil {
			log.Close()
			return err
		}
		started++
		pidValue := fmt.Sprintf("%d\n", child.Process.Pid)
		pidReady := make(chan struct{})
		// Register cleanup immediately, including failures writing the PID file.
		go func() {
			err := child.Wait()
			log.Close()
			<-pidReady
			if raw, readErr := os.ReadFile(pidPath); readErr == nil && bytes.Equal(raw, []byte(pidValue)) {
				os.Remove(pidPath)
			}
			results <- result{channel.Name, err}
		}()
		pid, err := os.OpenFile(pidPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			close(pidReady)
			return err
		}
		_, writeErr := io.WriteString(pid, pidValue)
		closeErr := pid.Close()
		close(pidReady)
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
		fmt.Fprintf(synchronizedChannelOutput{&mu, errout}, "Channel %s started: %s\n", channel.Name, channel.Root)
	}
	select {
	case <-ctx.Done():
		return nil
	case result := <-results:
		started--
		if ctx.Err() != nil {
			return nil
		}
		if result.err == nil {
			result.err = errors.New("unexpected clean exit")
		}
		return fmt.Errorf("channel %s stopped: %w", result.name, result.err)
	}
}
