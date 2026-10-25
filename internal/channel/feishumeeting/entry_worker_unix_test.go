//go:build unix

package feishumeeting

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEntryWorkerDoesNotPassLifetimePipeToSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	binary := entryScript(t, "if [ -p /dev/fd/3 ]; then exit 99; fi\nexit 71")
	err := (EntryReceiver{Config: fixtureConfig(), Binary: binary}).Run(ctx, func(context.Context, EntryEvent) error { return nil }, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exit 71") {
		t.Fatalf("subscriber inherited the ownership pipe or failed: %v", err)
	}
}

func TestEntryWorkerRejectsMissingOwnershipBeforeStarting(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker := filepath.Join(root, "unexpected-start")
	binary := entryScript(t, "touch "+entryShellQuote(marker))
	file, err := os.CreateTemp(root, "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, parent := range []string{"1", "invalid", strconv.Itoa(os.Getpid())} {
		cmd := exec.Command(executable, EntryWorkerFlag, parent, binary)
		cmd.ExtraFiles = []*os.File{file}
		if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 125 {
			t.Fatalf("invalid worker ownership was not rejected: %v", err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker executed without verified ownership", err)
	}
}
