package claude

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// runWithDrainedStdout bounds idle pipe reads after exit, rather than time spent
// delivering observations. A slow durable sink must not discard buffered output.
func runWithDrainedStdout(cmd *exec.Cmd, output io.Writer) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	cmd.Stdout = writer
	exited := make(chan struct{})
	copied := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			select {
			case <-exited:
				// Renew before each read: callback latency does not consume the
				// allowance for descendants that leave a pipe open without data.
				_ = reader.SetReadDeadline(time.Now().Add(cmd.WaitDelay))
			default:
			}
			n, readErr := reader.Read(buf)
			if n > 0 {
				if _, err := output.Write(buf[:n]); err != nil {
					copied <- err
					return
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					readErr = nil
				} else if errors.Is(readErr, os.ErrDeadlineExceeded) {
					readErr = exec.ErrWaitDelay
				}
				copied <- readErr
				return
			}
		}
	}()
	err = cmd.Run()
	_ = writer.Close()
	// Wake a read already blocked on a pipe inherited by a descendant.
	_ = reader.SetReadDeadline(time.Now().Add(cmd.WaitDelay))
	close(exited)
	if copyErr := <-copied; err == nil {
		err = copyErr
	}
	return err
}
