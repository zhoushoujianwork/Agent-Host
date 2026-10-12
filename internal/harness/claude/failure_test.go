//go:build unix

package claude

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
)

func TestFailureCategoriesKeepRawDiagnosticsAndErrorChain(t *testing.T) {
	for _, tc := range []struct {
		mode string
		kind harness.FailureKind
	}{
		{"error", harness.FailureProvider},
		{"api_error_nonzero", harness.FailureProvider},
		{"nonzero", harness.FailureProcess},
		{"missing_final", harness.FailureUnknown},
		{"malformed", harness.FailureUnknown},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, tc.mode)
			result, err := adapter.Run(context.Background(), request, nil)
			var failure *harness.ExecutionError
			if !errors.As(err, &failure) || failure.Kind != tc.kind || failure.Cause == nil || result.Error != err.Error() {
				t.Fatalf("category or raw diagnostic lost: result=%+v err=%v failure=%+v", result, err, failure)
			}
			if tc.mode == "nonzero" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatal("underlying process error lost", err)
				}
			}
		})
	}
}

func TestFailureCategoriesDistinguishTimeoutCancellationAndStartup(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		adapter, request := fixtureAdapter(t, "callback")
		ctx, cancel := context.WithCancel(context.Background())
		kind, cause := harness.FailureCanceled, context.Canceled
		if timeout {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			kind, cause = harness.FailureDeadline, context.DeadlineExceeded
		}
		result, err := adapter.Run(ctx, request, func(o harness.Observation) error {
			if o.Delta != "" && !timeout {
				cancel()
			}
			return nil
		})
		cancel()
		var failure *harness.ExecutionError
		if !errors.As(err, &failure) || failure.Kind != kind || !errors.Is(err, cause) || result.Status != "canceled" {
			t.Fatalf("cancellation cause lost: result=%+v err=%v failure=%+v", result, err, failure)
		}
	}
	adapter, request := fixtureAdapter(t, "normal")
	request.Workspace = "/missing/agent-host-workspace"
	result, err := adapter.Run(context.Background(), request, nil)
	var failure *harness.ExecutionError
	if !errors.As(err, &failure) || failure.Kind != harness.FailureStartup || result.Status != "failed" || !strings.Contains(result.Error, "workspace") {
		t.Fatalf("startup category lost: result=%+v err=%v failure=%+v", result, err, failure)
	}
}
