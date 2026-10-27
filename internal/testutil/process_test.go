package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestProcessListingDenied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		output string
		want   bool
	}{
		{"operation denied", &os.PathError{Op: "fork/exec", Path: "/bin/ps", Err: syscall.EPERM}, "", true},
		{"permission denied", fmt.Errorf("launch: %w", syscall.EACCES), "", true},
		{"command denial", &exec.ExitError{}, "ps: Operation not permitted", true},
		{"command permission", &exec.ExitError{}, "ps: Permission denied", true},
		{"missing command", exec.ErrNotFound, "", false},
		{"other exit", &exec.ExitError{}, "ps: invalid option", false},
		{"unrelated error", errors.New("operation not permitted"), "", false},
		{"success", nil, "permission denied", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := processListingDenied(tc.err, tc.output); got != tc.want {
				t.Fatalf("processListingDenied(%v, %q) = %v, want %v", tc.err, tc.output, got, tc.want)
			}
		})
	}
}
