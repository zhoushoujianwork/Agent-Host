// Package testutil provides environment checks for integration tests.
package testutil

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// RequireProcessListing skips only when the environment denies the ps command.
// Missing executables and other failures remain test failures.
func RequireProcessListing(t *testing.T, command string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if processListingDenied(err, string(output)) {
		t.Skipf("process listing denied: %s %s: %v: %s", command, strings.Join(args, " "), err, output)
	}
	t.Fatalf("process listing failed: %s %s: %v: %s", command, strings.Join(args, " "), err, output)
}

func processListingDenied(err error, output string) bool {
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	output = strings.ToLower(output)
	return strings.Contains(output, "operation not permitted") || strings.Contains(output, "permission denied")
}
