//go:build unix

package feishu

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

const transportHelperEnv = "AGENT_HOST_FEISHU_TRANSPORT_TEST_HELPER"

func transportHelperBinary(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func TestCLITransportCancelledBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := (CLITransport{Binary: transportHelperBinary(t)}).Run(ctx)
	if !errors.Is(err, context.Canceled) || out.Started {
		t.Fatalf("Started=%v, err=%v; want false and context.Canceled", out.Started, err)
	}
}

func TestCLITransportRunningCancellation(t *testing.T) {
	// Only this exact test is selected in subprocesses. The explicit flag keeps
	// ordinary test runs from executing helper behavior.
	if mode := os.Getenv(transportHelperEnv); mode != "" {
		switch mode {
		case "block":
			_, _ = io.WriteString(os.Stdout, "helper-ready")
			// Bound the helper's lifetime even if transport cancellation regresses.
			<-time.NewTimer(15 * time.Second).C
		case "stdout":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte("private-stdout-diagnostic"), maxResponseBytes/24+2))
		case "stderr":
			_, _ = os.Stderr.Write(bytes.Repeat([]byte("private-stderr-diagnostic"), (64<<10)/24+2))
		default:
			os.Exit(2)
		}
		os.Exit(0)
	}
	t.Setenv(transportHelperEnv, "block")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	out, err := (CLITransport{Binary: transportHelperBinary(t)}).Run(ctx, "-test.run=^TestCLITransportRunningCancellation$")
	if !out.Started || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Started=%v, err=%v; want true and context.DeadlineExceeded", out.Started, err)
	}
	if string(out.Stdout) != "helper-ready" {
		t.Fatal("helper did not reach its blocking operation before cancellation")
	}
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("cancellation took %v; want at most 6s", elapsed)
	}
}

func TestCLITransportOutputLimits(t *testing.T) {
	for _, tc := range []struct {
		stream string
		limit  int
	}{
		{"stdout", maxResponseBytes},
		{"stderr", 64 << 10},
	} {
		t.Run(tc.stream, func(t *testing.T) {
			t.Setenv(transportHelperEnv, tc.stream)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := (CLITransport{Binary: transportHelperBinary(t)}).Run(ctx, "-test.run=^TestCLITransportRunningCancellation$")
			if !out.Started || err == nil {
				t.Fatalf("Started=%v, err=%v, stdout bytes=%d, stderr bytes=%d; want true and an overflow error", out.Started, err, len(out.Stdout), len(out.Stderr))
			}
			if err.Error() != "lark-cli output exceeded its limit" || strings.Contains(err.Error(), "private-") {
				t.Fatal("overflow error did not preserve the generic diagnostic boundary")
			}
			stream, other := out.Stdout, out.Stderr
			if tc.stream == "stderr" {
				stream, other = out.Stderr, out.Stdout
			}
			if len(stream) != tc.limit || len(other) != 0 {
				t.Fatalf("stream lengths=(%d, %d); want (%d, 0)", len(stream), len(other), tc.limit)
			}
		})
	}
}
