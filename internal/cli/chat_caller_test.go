//go:build darwin || linux

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"agent-host/internal/testutil"
)

// Re-exec real processes so parent loss includes SIGKILL, with no opportunity
// for the parent to forward a cancellation signal to the Host.
func TestChatCallerLifecycle(t *testing.T) {
	testutil.RequireProcessListing(t, "/bin/ps", "-p", strconv.Itoa(os.Getpid()), "-o", "stat=", "-o", "lstart=")
	testutil.RequireProcessListing(t, "/bin/ps", "-axo", "pid=,ppid=")
	const modeKey = "AGENT_HOST_CALLER_TEST_MODE"
	const rootKey = "AGENT_HOST_CALLER_TEST_ROOT"
	const argsKey = "AGENT_HOST_CALLER_TEST_ARGS"
	if mode := os.Getenv(modeKey); mode != "" {
		root := os.Getenv(rootKey)
		if mode == "supervisor" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestChatCallerLifecycle$")
			cmd.Env = append(os.Environ(), modeKey+"=worker")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(root, "worker-pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
			for {
				if _, err := os.Stat(filepath.Join(root, "parent-exit")); err == nil {
					return
				}
				if _, err := os.Stat(filepath.Join(root, "done")); err == nil {
					_ = cmd.Wait()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv(argsKey)), &args); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := Run(ctx, args, nil, io.Discard, io.Discard)
		_ = os.WriteFile(filepath.Join(root, "done"), []byte(fmt.Sprint(err)), 0600)
		return
	}
	for _, mode := range []string{"parent-exit", "parent-killed", "explicit-caller", "alive-owner", "detached"} {
		t.Run(mode, func(t *testing.T) {
			fixture, args, _ := fileResumeFixture(t)
			root := t.TempDir()
			t.Setenv(rootKey, root)
			unrelated := exec.Command("/bin/sleep", "30")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
			if mode == "alive-owner" {
				args = append(args, "--caller-pid", strconv.Itoa(os.Getpid()))
			}
			if mode == "explicit-caller" {
				args = append(args, "--caller-pid", strconv.Itoa(unrelated.Process.Pid))
			}
			if mode == "detached" {
				args = append(args, "--detach")
			}
			for i, arg := range args {
				if arg != "--claude" {
					continue
				}
				binary := args[i+1]
				script, err := os.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				blocked := strings.Replace(string(script), "cat >/dev/null", `cat >/dev/null
/bin/sleep 30 &
descendant=$!
echo "$descendant" > "$AGENT_HOST_CALLER_TEST_ROOT/descendant"
echo $$ > "$AGENT_HOST_CALLER_TEST_ROOT/native"
while [ ! -f "$AGENT_HOST_CALLER_TEST_ROOT/release" ]; do sleep 0.02; done
kill "$descendant"
wait "$descendant" 2>/dev/null || true`, 1)
				if err := os.WriteFile(binary, []byte(blocked), 0700); err != nil {
					t.Fatal(err)
				}
				break
			}
			args = append(args, "-m", "caller lifetime fixture")
			encoded, _ := json.Marshal(args)
			parent := exec.Command(os.Args[0], "-test.run=^TestChatCallerLifecycle$")
			parent.Env = append(os.Environ(), modeKey+"=supervisor", argsKey+"="+string(encoded))
			if err := parent.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(root, "release"), nil, 0600)
				_ = parent.Process.Kill()
				if parent.ProcessState == nil {
					_ = parent.Wait()
				}
			})
			waitCallerFile(t, filepath.Join(root, "native"))
			switch mode {
			case "parent-exit":
				_ = os.WriteFile(filepath.Join(root, "parent-exit"), nil, 0600)
				if err := parent.Wait(); err != nil {
					t.Fatal(err)
				}
			case "parent-killed", "detached":
				_ = parent.Process.Kill()
				_ = parent.Wait()
			case "explicit-caller":
				_ = unrelated.Process.Kill()
				// Leave a zombie until after cancellation: PID existence is not liveness.
			}
			if mode == "detached" || mode == "alive-owner" {
				time.Sleep(2500 * time.Millisecond)
				if _, err := os.Stat(filepath.Join(root, "done")); !os.IsNotExist(err) {
					t.Fatalf("%s worker stopped before release: %v", mode, err)
				}
				_ = os.WriteFile(filepath.Join(root, "release"), nil, 0600)
			}
			done := string(waitCallerFile(t, filepath.Join(root, "done")))
			if mode == "alive-owner" {
				if err := parent.Wait(); err != nil {
					t.Fatalf("owner could not collect wrapper exit: %v", err)
				}
			}
			runs := fileResumeRuns(t, fixture.home)
			if len(runs) != 1 {
				t.Fatalf("unexpected run count: %+v", runs)
			}
			if mode == "detached" || mode == "alive-owner" {
				if done != "<nil>" || runs[0].ExecutionStatus != "completed" {
					t.Fatalf("%s run failed: %s %+v", mode, done, runs[0])
				}
			} else if !strings.Contains(done, "caller exited") || runs[0].ExecutionStatus != "canceled" || !strings.Contains(runs[0].Error, "caller exited") || runs[0].MemoryStatus != "skipped" {
				t.Fatalf("caller loss did not retain canceled evidence: %s %+v", done, runs[0])
			}
			for _, name := range []string{"native", "descendant"} {
				pid, _ := strconv.Atoi(strings.TrimSpace(string(waitCallerFile(t, filepath.Join(root, name)))))
				if _, err := callerIdentity(t.Context(), pid); err == nil {
					t.Fatalf("owned %s process %d survived", name, pid)
				}
			}
			if mode != "explicit-caller" {
				if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
					t.Fatal("unrelated process was stopped", err)
				}
			}
		})
	}
}

func waitCallerFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}

func TestChatCallerRejectsInvalidOwnership(t *testing.T) {
	for _, pid := range []int{-1, 1, os.Getpid()} {
		if _, _, err := chatCallerContext(t.Context(), os.Getppid(), pid, false); err == nil {
			t.Fatalf("accepted caller PID %d", pid)
		}
	}
	if _, _, err := chatCallerContext(t.Context(), os.Getppid(), os.Getppid(), true); err == nil {
		t.Fatal("accepted detach with an explicit caller")
	}
	if _, _, err := chatCallerContext(t.Context(), os.Getppid(), 1<<30, false); err == nil {
		t.Fatal("accepted missing caller")
	}
}

// Fake identities reproduce PID reuse and transient verification failure without
// relying on PID recycling or changing the production fail-closed policy.
func TestChatCallerProbeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, current, reason string
		err                   error
	}{
		{"changed", "new-start", "start identity changed", nil},
		{"timeout", "", "context deadline exceeded", context.DeadlineExceeded},
		{"unverifiable", "", "probe unavailable", errors.New("probe unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx, cancel, err := chatCallerContextWithProbe(t.Context(), os.Getppid(), 1<<29, false, func(context.Context, int) (string, error) {
				calls++
				if calls == 1 {
					return "original-start", nil
				}
				return tc.current, tc.err
			})
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			cause := fmt.Sprint(context.Cause(ctx))
			for _, want := range []string{"supervising process 536870912", "selected by caller-pid", "original-start", tc.reason} {
				if !strings.Contains(cause, want) {
					t.Fatalf("missing %q in %s", want, cause)
				}
			}
			if calls != 2 {
				t.Fatalf("unexpected retry: %d probes", calls)
			}
		})
	}
}

func TestChatCallerStableIdentityAcrossPolls(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel, err := chatCallerContextWithProbe(t.Context(), os.Getppid(), 1<<29, false, func(context.Context, int) (string, error) {
		calls.Add(1)
		return "stable-start", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for calls.Load() < 4 {
		select {
		case <-ctx.Done():
			t.Fatalf("live owner lost: %v", context.Cause(ctx))
		case <-deadline.C:
			t.Fatal("caller was not checked across polls")
		case <-tick.C:
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("live owner lost: %v", context.Cause(ctx))
	}
	cancel()
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatal(context.Cause(ctx))
	}
}
