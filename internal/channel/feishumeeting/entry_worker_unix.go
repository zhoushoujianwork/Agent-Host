//go:build unix

package feishumeeting

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// A separate worker owns the subscriber process group. Only the Host retains
// the pipe's writer: even SIGKILL releases it, without PID scans or lock removal.
func entryCommand(ctx context.Context, binary string, args ...string) (*exec.Cmd, func(), error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	binary, err = exec.LookPath(binary)
	if err != nil {
		return nil, nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	closePipe := func() { _ = writer.Close(); _ = reader.Close() }
	workerArgs := append([]string{EntryWorkerFlag, strconv.Itoa(os.Getpid()), binary}, args...)
	cmd := exec.CommandContext(ctx, executable, workerArgs...)
	cmd.ExtraFiles = []*os.File{reader}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { closePipe(); return nil }
	cmd.WaitDelay = 3 * time.Second
	return cmd, closePipe, nil
}

// EntryWorker receives the owner-lifetime pipe on fd 3, separate from the
// subscriber's stdin. It never adopts existing processes or retries a command.
func EntryWorker(args []string) int {
	if len(args) < 2 {
		return 125
	}
	parent, err := strconv.Atoi(args[0])
	if err != nil || parent <= 1 || os.Getppid() != parent {
		return 125
	}
	owner := os.NewFile(3, "entry-owner")
	if owner == nil {
		return 125
	}
	defer owner.Close()
	info, err := owner.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return 125
	}
	// ExtraFiles deliberately clears close-on-exec in this worker. Restore it
	// before launching the subscriber so the lifetime channel stays private.
	syscall.CloseOnExec(int(owner.Fd()))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() { _, _ = io.Copy(io.Discard, owner); cancel() }()
	cmd := exec.CommandContext(ctx, args[1], args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.WaitDelay = 2 * time.Second
	if err := bindCLIProcessGroup(cmd); err != nil {
		return 125
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, entryExitPrefix+"125")
		return 125
	}
	err = cmd.Wait()
	// A wrapper can exit before its lock-holding child. Stop that same owned
	// group on every outcome, not only cancellation, before reporting the exit.
	stopCLIProcessGroup(cmd)
	code := 0
	if err != nil {
		code = cmd.ProcessState.ExitCode()
		if code < 0 {
			code = 125
		}
	}
	fmt.Fprintln(os.Stderr, entryExitPrefix+strconv.Itoa(code))
	return code
}
