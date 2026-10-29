package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDiscoverCLIStateFixedPathOnly(t *testing.T) {
	root := channelDirectoryFixture(t)
	directory := filepath.Join(root, "config")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if cliState, err := discoverCLIState(directory); err != nil || cliState != filepath.Join(root, "cli-state") {
		t.Fatal("absent CLI state must remain mounted for the first chat", cliState, err)
	}
	cliState := filepath.Join(root, "cli-state")
	if err := os.Mkdir(cliState, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliState, "state.db"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := discoverCLIState(directory)
	if err != nil || got != cliState {
		t.Fatal("fixed ROOT/cli-state must be discovered once initialized", got, err)
	}
	// A directory that is not a unified ROOT/config layout never auto-discovers
	// CLI state: only the documented default path qualifies.
	if got, err := discoverCLIState(filepath.Join(root, "channels-dir")); err != nil || got != "" {
		t.Fatal("non-config directory must not discover CLI state", got, err)
	}
	if err := os.Chmod(cliState, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverCLIState(directory); err == nil {
		t.Fatal("group/other accessible CLI state must be rejected")
	}
	if err := os.Chmod(cliState, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(cliState, "state.db")
	if err := os.Rename(database, database+".other"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(database+".other", database); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverCLIState(directory); err == nil {
		t.Fatal("CLI discovery followed a database symlink")
	}
}

func channelDirectoryFixture(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChannelsDiscoverEnabledAndSkipDisabledMissingRuntime(t *testing.T) {
	directory := channelDirectoryFixture(t)
	one, _ := newServiceFixture(t)
	two, _ := newServiceFixture(t)
	writeBotJSON(t, filepath.Join(directory, "channel-feishu.json"), map[string]any{"enabled": true, "runtime_root": one.Root})
	relative, _ := filepath.Rel(directory, two.Root)
	writeBotJSON(t, filepath.Join(directory, "channel-dingtalk.json"), map[string]any{"enabled": true, "runtime_root": relative})
	writeBotJSON(t, filepath.Join(directory, "channel-disabled.json"), map[string]any{"enabled": false, "runtime_root": "/absent/disabled-runtime"})
	// A non-channel configuration must never be decoded as a registration.
	os.WriteFile(filepath.Join(directory, "data-knowledge.json"), []byte(`{"sources":[]}`), 0600)
	os.WriteFile(filepath.Join(directory, "component-agents.json"), []byte(`{"agents":[]}`), 0600)
	channels, err := discoverServiceChannels(directory, "")
	if err != nil || len(channels) != 2 || channels[0].Root != two.Root || channels[1].Root != one.Root {
		t.Fatalf("discovery: %+v %v", channels, err)
	}
}

func TestChannelsRejectUnclassifiedRegistrationFilename(t *testing.T) {
	directory := channelDirectoryFixture(t)
	layout, _ := newServiceFixture(t)
	writeBotJSON(t, filepath.Join(directory, "feishu.json"), map[string]any{"enabled": true, "runtime_root": layout.Root})
	if _, err := discoverServiceChannels(directory, ""); err == nil || !strings.Contains(err.Error(), "channel-*.json") {
		t.Fatal("unclassified registration accepted", err)
	}
}

func TestChannelsRejectAmbiguousInvalidAndRedirectedConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true,"enabled":false,"runtime_root":"/tmp"}`,
		`{"enabled":"true","runtime_root":"/tmp"}`,
		`{"runtime_root":"/tmp"}`,
		`{"enabled":null,"runtime_root":"/tmp"}`,
		`{"enabled":false,"runtime_root":"/tmp","extra":true}`,
		`{"enabled":false,"runtime_root":"/tmp"}`,
	} {
		directory := channelDirectoryFixture(t)
		os.WriteFile(filepath.Join(directory, "channel-test.json"), []byte(raw), 0600)
		if _, err := discoverServiceChannels(directory, "/fallback"); err == nil {
			t.Fatal("invalid or empty channel set used fallback", raw)
		}
	}
	directory := channelDirectoryFixture(t)
	layout, _ := newServiceFixture(t)
	for _, name := range []string{"one", "two"} {
		writeBotJSON(t, filepath.Join(directory, "channel-"+name+".json"), map[string]any{"enabled": true, "runtime_root": layout.Root})
	}
	if _, err := discoverServiceChannels(directory, ""); err == nil {
		t.Fatal("duplicate runtime accepted")
	}
	alias := filepath.Join(channelDirectoryFixture(t), "alias")
	os.Symlink(directory, alias)
	if _, err := discoverServiceChannels(alias, ""); err == nil {
		t.Fatal("symlink directory accepted")
	}
	if err := os.Chmod(filepath.Join(directory, "channel-one.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverServiceChannels(directory, ""); err == nil {
		t.Fatal("public registration accepted")
	}
}

func TestChannelsMissingDirectoryPreservesLegacyRoot(t *testing.T) {
	channels, err := discoverServiceChannels(filepath.Join(channelDirectoryFixture(t), "absent"), "/legacy")
	if err != nil || len(channels) != 1 || channels[0].Root != "/legacy" {
		t.Fatal(channels, err)
	}
}

func TestChannelsCheckDoesNotConnectOrWriteRuntimeState(t *testing.T) {
	directory := channelDirectoryFixture(t)
	layout, _ := newServiceFixture(t)
	writeBotJSON(t, filepath.Join(directory, "channel-test.json"), map[string]any{"enabled": true, "runtime_root": layout.Root})
	if err := Run(context.Background(), []string{"service", "check", "--channels-dir", directory}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state", "native", "workspace"} {
		entries, err := os.ReadDir(filepath.Join(layout.Root, name))
		if err != nil || len(entries) != 0 {
			t.Fatal("check changed runtime state", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Root, "run.lock")); !os.IsNotExist(err) {
		t.Fatal("check created runtime lock")
	}
}

func TestChannelViewerPreparationRetainsStateAndRejectsSymlinkTargets(t *testing.T) {
	layout, _ := newServiceFixture(t)
	source, err := filepath.Abs(filepath.Join("..", "..", "tools", "run-viewer"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(layout.State, "retained")
	os.WriteFile(evidence, []byte("original"), 0600)
	if err := prepareChannelViewer(layout.Root, source); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(evidence)
	if err != nil || string(raw) != "original" {
		t.Fatal("viewer preparation changed state", err)
	}
	for _, name := range channelViewerFiles {
		info, err := os.Stat(filepath.Join(layout.Root, "viewer", name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("viewer file is not private", name, err)
		}
	}
	for _, name := range []string{"executions.html", "markdown-parser.js", "markdown.js"} {
		installed, err := os.ReadFile(filepath.Join(layout.Root, "viewer", name))
		original, readErr := os.ReadFile(filepath.Join(source, name))
		if err != nil || readErr != nil || string(installed) != string(original) {
			t.Fatal("Workbench dependency missing from installed viewer", name, err, readErr)
		}
	}
	target := filepath.Join(layout.Root, "viewer", "server.py")
	os.Remove(target)
	os.Symlink(evidence, target)
	if err := prepareChannelViewer(layout.Root, source); err == nil {
		t.Fatal("viewer preparation accepted symlink target")
	}
}

func TestChannelsPreflightRejectsInvalidPeerBeforeAnyProcess(t *testing.T) {
	directory := channelDirectoryFixture(t)
	valid, _ := newServiceFixture(t)
	invalid, _ := newServiceFixture(t)
	writeBotJSON(t, filepath.Join(directory, "channel-one.json"), map[string]any{"enabled": true, "runtime_root": valid.Root})
	writeBotJSON(t, filepath.Join(directory, "two.json"), map[string]any{"enabled": true, "runtime_root": invalid.Root})
	os.Remove(filepath.Join(invalid.Config, "environment.json"))
	err := runConfiguredChannels(context.Background(), "run", directory, "", "/absent/viewer", "python3", 8765, true, false, false, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("invalid enabled peer accepted")
	}
	for _, root := range []string{valid.Root, invalid.Root} {
		if _, err := os.Stat(filepath.Join(root, "service.pid")); !os.IsNotExist(err) {
			t.Fatal("channel started despite failed preflight")
		}
	}
	lock, err := lockChannelRuntime(valid.Root)
	if err != nil {
		t.Fatal("preflight leaked lock", err)
	}
	defer lock.Close()
	if other, err := lockChannelRuntime(valid.Root); err == nil {
		other.Close()
		t.Fatal("duplicate runtime lock succeeded")
	}
}

func TestChannelSupervisorConcurrentStartupShutdownAndFailure(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "peer failure"}[crash], func(t *testing.T) {
			directory := channelDirectoryFixture(t)
			binary := filepath.Join(directory, "fixture-host")
			script := "#!/bin/sh\nroot=$4\ntrap 'exit 0' TERM INT\ncase \"$*\" in *--no-workbench*) ;; *) exit 8 ;; esac\necho ready\nwhile true; do\n if [ -f \"$root/crash\" ]; then exit 7; fi\n sleep 0.05\ndone\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			one, _ := newServiceFixture(t)
			two, _ := newServiceFixture(t)
			channels := []configuredChannel{{"one", one.Root}, {"two", two.Root}}
			for _, channel := range channels {
				os.Mkdir(filepath.Join(channel.Root, "logs"), 0700)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				finished <- superviseChannelProcesses(ctx, channels, binary, "python3", true, io.Discard, io.Discard)
			}()
			var pids []int
			for _, channel := range channels {
				deadline := time.Now().Add(5 * time.Second)
				for {
					raw, _ := os.ReadFile(filepath.Join(channel.Root, "service.pid"))
					log, _ := os.ReadFile(filepath.Join(channel.Root, "logs/service.ndjson"))
					if len(raw) > 0 && strings.Contains(string(log), "ready") {
						var pid int
						fmt.Sscanf(string(raw), "%d", &pid)
						pids = append(pids, pid)
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("both channels did not start concurrently")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if crash {
				os.WriteFile(filepath.Join(one.Root, "crash"), nil, 0600)
			} else {
				cancel()
			}
			select {
			case err := <-finished:
				if (err != nil) != crash {
					t.Fatalf("crash=%v error=%v", crash, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not stop its children")
			}
			for i, channel := range channels {
				if _, err := os.Stat(filepath.Join(channel.Root, "service.pid")); !os.IsNotExist(err) {
					t.Fatal("stale PID after shutdown")
				}
				if err := syscall.Kill(pids[i], 0); err != syscall.ESRCH {
					t.Fatal("child still alive after shutdown", err)
				}
			}
		})
	}
}
