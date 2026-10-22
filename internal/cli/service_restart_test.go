package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRestartWaitIsBoundedWithoutReleasingExistingLock(t *testing.T) {
	root := channelDirectoryFixture(t)
	owner, err := lockChannelRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	waiter, err := os.OpenFile(filepath.Join(root, "run.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	acquire := func() error { return syscall.Flock(int(waiter.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
	if err := waitForRuntimeLock(ctx, acquire); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("restart wait ignored its deadline", err)
	}
	if err := acquire(); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatal("timed-out restart released another owner's lock", err)
	}
}

func TestRestartRefusesUnrelatedPIDAndRetainsLock(t *testing.T) {
	root := channelDirectoryFixture(t)
	lock, err := lockChannelRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	if err := os.WriteFile(filepath.Join(root, "service.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if acquired, err := lockChannelRuntimeForStart(t.Context(), root, "", true, io.Discard); err == nil {
		acquired.Close()
		t.Fatal("unrelated PID accepted")
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated process was stopped", err)
	}
	if acquired, err := lockChannelRuntime(root); err == nil {
		acquired.Close()
		t.Fatal("existing lock was removed")
	}
}

func TestManagedServiceIdentityRequiresExecutableOwnerAndExactRoot(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		uid     int
		want    bool
	}{
		{binary + " service run --root /private/runtime --no-workbench", os.Getuid(), true},
		{binary + " service run --root /private/runtime-other --no-workbench", os.Getuid(), false},
		{binary + " service check --root /private/runtime", os.Getuid(), false},
		{"sleep 30", os.Getuid(), false},
		{binary + " service run --root /private/runtime", os.Getuid() + 1, false},
	} {
		if got := sameManagedService(managedServiceProcess{uid: tc.uid, command: tc.command}, "--root", "/private/runtime"); got != tc.want {
			t.Fatalf("command %q: matched=%v", tc.command, got)
		}
	}
}

func TestCheckoutRestartRecognizesOnlySelectedDeployedBinary(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	directory, root := filepath.Join(base, "config"), filepath.Join(base, "channels", "dingtalk")
	for _, path := range []string{directory, filepath.Join(base, "bin")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(base, "bin", "agent-host")
	if err := os.WriteFile(binary, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	// Resolve macOS temporary-directory aliases just as production config does.
	base, _ = filepath.EvalSymlinks(base)
	directory, root, binary = filepath.Join(base, "config"), filepath.Join(base, "channels", "dingtalk"), filepath.Join(base, "bin", "agent-host")
	got := deployedRuntimeBinary(root, directory)
	if got != binary {
		t.Fatal("deployed executable not recognized", got)
	}
	for _, selector := range []string{"--root", "--config-dir"} {
		selected := root
		if selector == "--config-dir" {
			selected = directory
		}
		process := managedServiceProcess{uid: os.Getuid(), command: binary + " service run " + selector + " " + selected + " --no-browser"}
		if !sameManagedService(process, selector, selected, got) {
			t.Fatal("deployed process rejected", process)
		}
		if sameManagedService(process, selector, selected+"-other", got) {
			t.Fatal("other runtime matched")
		}
		process.uid++
		if sameManagedService(process, selector, selected, got) {
			t.Fatal("other owner matched")
		}
	}
	if deployedRuntimeBinary(filepath.Join(base, "other", "dingtalk"), directory) != "" {
		t.Fatal("unrelated root gained executable alias")
	}
	if err := os.Chmod(binary, 0777); err != nil {
		t.Fatal(err)
	}
	if deployedRuntimeBinary(root, directory) != "" {
		t.Fatal("writable executable trusted")
	}
	if err := os.Chmod(binary, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(binary, binary+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary+".real", binary); err != nil {
		t.Fatal(err)
	}
	if deployedRuntimeBinary(root, directory) != "" {
		t.Fatal("symlink executable trusted")
	}
}

func TestManagedServiceRestartReplacesSupervisorAfterPreflight(t *testing.T) {
	root, directory := unifiedFixture(t)
	writeBotJSON(t, filepath.Join(directory, "channels.dingtalk.json"), map[string]any{
		"version": 1, "enabled": true, "settings": map[string]any{
			"dws": map[string]any{"enabled": true, "profile": "fixture:owner", "services": []string{"contact"}},
		},
	})
	binary := filepath.Join(root, "bin", "agent-host")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/agent-host")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build restart fixture: %v\n%s", err, raw)
	}
	viewer, err := filepath.Abs("../../tools/run-viewer")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"service", "run", "--config-dir", directory, "--viewer-source", viewer, "--viewer-python", "/absent-fixture-python", "--no-browser"}
	start := func() *exec.Cmd {
		cmd := exec.Command(binary, args...)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				cmd.Process.Signal(syscall.SIGTERM)
				cmd.Wait()
			}
		})
		return cmd
	}
	pidPath := filepath.Join(root, "channels", "dingtalk", "service.pid")
	waitPID := func(previous, readyCount int) int {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			raw, _ := os.ReadFile(pidPath)
			pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			log, _ := os.ReadFile(filepath.Join(filepath.Dir(pidPath), "logs", "service.ndjson"))
			if pid > 1 && pid != previous && strings.Count(string(log), `"type":"channel.ready"`) >= readyCount {
				return pid
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("replacement worker did not start")
		return 0
	}
	first := start()
	oldWorker := waitPID(0, 1)
	evidence := filepath.Join(filepath.Dir(pidPath), "state", "retained")
	if err := os.WriteFile(evidence, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	// A failed new configuration must leave the running instance alone.
	hostPath := filepath.Join(directory, "host.json")
	host, _ := os.ReadFile(hostPath)
	os.WriteFile(hostPath, []byte("invalid"), 0600)
	invalid := exec.Command(binary, append(args, "--restart")...)
	if err := invalid.Run(); err == nil {
		t.Fatal("invalid configuration restarted the service")
	}
	if err := first.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("failed preflight stopped the supervisor", err)
	}
	if err := syscall.Kill(oldWorker, 0); err != nil {
		t.Fatal("failed preflight stopped the worker", err)
	}
	os.WriteFile(hostPath, host, 0600)
	// Direct starts retain duplicate detection; make run supplies --restart.
	duplicate := exec.Command(binary, args...)
	if raw, err := duplicate.CombinedOutput(); err == nil || !bytes.Contains(raw, []byte("already running")) {
		t.Fatal("duplicate direct start did not reject the lock", err, string(raw))
	}
	args = append(args, "--restart")
	// Reproduce make run taking over a separately deployed runtime. The
	// replacement executable is a copy at a different checkout path.
	compiled, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(root, "checkout", "agent-host")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, compiled, 0700); err != nil {
		t.Fatal(err)
	}
	second := start()
	newWorker := waitPID(oldWorker, 2)
	if raw, err := os.ReadFile(evidence); err != nil || string(raw) != "original" {
		t.Fatal("restart changed retained state", err)
	}
	if err := first.Wait(); err != nil {
		t.Fatal("old supervisor did not stop normally", err)
	}
	if err := syscall.Kill(oldWorker, 0); err != syscall.ESRCH {
		t.Fatal("old worker survived restart", err)
	}
	second.Process.Signal(syscall.SIGTERM)
	if err := second.Wait(); err != nil {
		t.Fatal("replacement supervisor did not stop normally", err)
	}
	if err := syscall.Kill(newWorker, 0); err != syscall.ESRCH {
		t.Fatal("replacement worker survived shutdown", err)
	}
	lock, err := lockChannelRuntime(filepath.Dir(pidPath))
	if err != nil {
		t.Fatal("restart leaked its runtime lock", err)
	}
	lock.Close()
}

func TestRestartRequiresManagedStartup(t *testing.T) {
	err := runService(context.Background(), []string{"run", "--restart", "--root", "/absent"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "managed startup") {
		t.Fatal("unmanaged restart accepted", err)
	}
}
