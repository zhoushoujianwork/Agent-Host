//go:build unix

package feishumeeting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestEntryReceiverOwnerDeathReleasesSubscriptionLock(t *testing.T) {
	if os.Getenv("AGENT_HOST_ENTRY_OWNER_HELPER") == "1" {
		err := (EntryReceiver{Config: fixtureConfig(), Binary: os.Getenv("AGENT_HOST_ENTRY_OWNER_BINARY")}).Run(context.Background(), func(context.Context, EntryEvent) error { return nil }, func(string, string) error { return nil })
		fmt.Fprintln(os.Stderr, err)
		os.Exit(74)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	lockPath, pidPath := filepath.Join(root, "subscriber.lock"), filepath.Join(root, "subscriber.pid")
	binary := entryScript(t, "AGENT_HOST_ENTRY_LOCK_HELPER=1 AGENT_HOST_ENTRY_TEST_LOCK="+entryShellQuote(lockPath)+" AGENT_HOST_ENTRY_TEST_PID="+entryShellQuote(pidPath)+" "+entryShellQuote(executable)+" -test.run='^TestEntrySubscriberLockHelper$' &\nwait")
	owner := exec.Command(executable, "-test.run=^TestEntryReceiverOwnerDeathReleasesSubscriptionLock$")
	owner.Env = append(os.Environ(), "AGENT_HOST_ENTRY_OWNER_HELPER=1", "AGENT_HOST_ENTRY_OWNER_BINARY="+binary)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidPath)
		childPID, _ = strconv.Atoi(string(raw))
		if childPID > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 1 {
		t.Fatal("subscriber did not acquire its lock")
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("subscriber lock was not held before Host termination")
	}
	unrelated := exec.Command("/bin/sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	deadline = time.Now().Add(4 * time.Second)
	for syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if time.Now().After(deadline) {
			t.Fatal("subscriber survived abrupt Host death and retained the app lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated process was stopped", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ready := false
	err = (EntryReceiver{Config: fixtureConfig(), Binary: binary}).Run(ctx, func(context.Context, EntryEvent) error { return nil }, func(kind, _ string) error {
		if kind == "connected" {
			ready = true
			cancel()
		}
		return nil
	})
	if !ready || !errors.Is(err, context.Canceled) {
		t.Fatalf("replacement subscriber did not recover: ready=%v err=%v", ready, err)
	}
}

// This test binary acts as the wrapper's long-lived subscriber child. Holding
// an OS lock models the official CLI's app lock; readiness follows acquisition.
func TestEntrySubscriberLockHelper(t *testing.T) {
	if os.Getenv("AGENT_HOST_ENTRY_LOCK_HELPER") != "1" {
		return
	}
	lock, err := os.OpenFile(os.Getenv("AGENT_HOST_ENTRY_TEST_LOCK"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		os.Exit(72)
	}
	if os.WriteFile(os.Getenv("AGENT_HOST_ENTRY_TEST_PID"), []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
		os.Exit(73)
	}
	if os.Getenv("AGENT_HOST_ENTRY_TEST_QUICK") == "1" {
		fmt.Fprintln(os.Stdout, `{"ok":true,"data":{"fixture":"reloaded"}}`)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "[SDK Info] connected to wss://fixture.invalid")
	if event := os.Getenv("AGENT_HOST_ENTRY_TEST_EVENT"); event != "" {
		fmt.Fprintln(os.Stdout, event)
	}
	if os.Getenv("AGENT_HOST_ENTRY_TEST_CLOSE_OUTPUT") == "1" {
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
	}
	time.Sleep(30 * time.Second)
	// Avoid the testing framework writing a trailing PASS into the event stream.
	os.Exit(0)
}

func TestCLIClientCancellationStopsWrapperDescendant(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	lockPath, pidPath := filepath.Join(root, "request.lock"), filepath.Join(root, "request.pid")
	body := "if [ \"$1\" = quick ]; then export AGENT_HOST_ENTRY_TEST_QUICK=1; fi\n" +
		"AGENT_HOST_ENTRY_LOCK_HELPER=1 AGENT_HOST_ENTRY_TEST_LOCK=" + entryShellQuote(lockPath) + " AGENT_HOST_ENTRY_TEST_PID=" + entryShellQuote(pidPath) + " " + entryShellQuote(executable) + " -test.run='^TestEntrySubscriberLockHelper$' &\nwait\n"
	client := CLIClient{Binary: entryScript(t, body)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completed struct {
		out Output
		err error
	}
	done := make(chan completed, 1)
	go func() { out, err := client.Run(ctx, "slow"); done <- completed{out, err} }()
	deadline := time.Now().Add(3 * time.Second)
	var pid int
	for {
		raw, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			pid, err = strconv.Atoi(string(raw))
			if err == nil && pid > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("REST wrapper child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("REST child was not alive: %v", err)
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("REST child did not acquire its lock")
	}
	cancel()
	select {
	case result := <-done:
		if !result.out.Started || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("REST cancellation lost started/uncertain evidence: %+v %v", result.out, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("REST wrapper did not stop on cancellation")
	}
	deadline = time.Now().Add(2 * time.Second)
	for syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if time.Now().After(deadline) {
			t.Fatal("REST descendant survived context cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	reloadCtx, stopReload := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopReload()
	out, err := client.Run(reloadCtx, "quick")
	if err != nil || !out.Started || string(out.Stdout) != "{\"ok\":true,\"data\":{\"fixture\":\"reloaded\"}}\n" {
		t.Fatalf("fresh REST child could not acquire released lock: started=%v err=%v", out.Started, err)
	}
}

func TestEntryReceiverStopsWrapperDescendantAndReleasesLockForReload(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"cancel", "handler failure", "wrapper exit"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			lockPath := filepath.Join(root, "subscriber.lock")
			pidPath := filepath.Join(root, "subscriber.pid")
			child := "AGENT_HOST_ENTRY_LOCK_HELPER=1 AGENT_HOST_ENTRY_TEST_LOCK=" + entryShellQuote(lockPath) + " AGENT_HOST_ENTRY_TEST_PID=" + entryShellQuote(pidPath)
			if outcome == "handler failure" {
				child += " AGENT_HOST_ENTRY_TEST_EVENT=" + entryShellQuote(string(entryFixture(t, "vc.bot.meeting_invited_v1", invitedEntryFixture())))
			}
			if outcome == "wrapper exit" {
				child += " AGENT_HOST_ENTRY_TEST_CLOSE_OUTPUT=1"
			}
			child += " " + entryShellQuote(executable) + " -test.run='^TestEntrySubscriberLockHelper$' &\n"
			if outcome != "wrapper exit" {
				child += "wait\n"
			} else {
				// Exit only after the test has observed the child holding its
				// lock. The worker can otherwise reap it before it even starts.
				child += "while [ ! -f " + entryShellQuote(filepath.Join(root, "exit-wrapper")) + " ]; do sleep 0.01; done\n"
			}
			binary := entryScript(t, child)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reached := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			done := make(chan error, 1)
			go func() {
				done <- (EntryReceiver{Config: fixtureConfig(), Binary: binary}).Run(ctx, func(context.Context, EntryEvent) error {
					close(reached)
					<-release
					return errors.New("fixture durable handler failed")
				}, func(kind, _ string) error {
					if kind == "connected" && outcome != "handler failure" {
						close(reached)
						<-release
					}
					return nil
				})
			}()
			select {
			case <-reached:
			case err := <-done:
				t.Fatalf("wrapper child failed before acquiring lock: %v", err)
			case <-ctx.Done():
				t.Fatal("wrapper child did not become ready")
			}
			pidRaw, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidRaw))
			if err != nil || pid <= 0 {
				t.Fatal("invalid fixture child PID")
			}
			// Cleanup also covers a deliberately broken implementation; the test
			// must never leave its own lock-holding orphan behind after failing.
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			if err := syscall.Kill(pid, 0); err != nil {
				t.Fatalf("subscriber was not alive before cleanup: %v", err)
			}
			lock, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				t.Fatal("subscriber did not hold its application lock")
			}
			if outcome == "cancel" {
				cancel()
			}
			if outcome == "wrapper exit" {
				if err := os.WriteFile(filepath.Join(root, "exit-wrapper"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			select {
			case err := <-done:
				if err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("subscriber did not stop promptly: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("wrapper descendant blocked cleanup")
			}
			// A PID alone can still identify a dead, unreaped zombie on Unix.
			// The kernel-held lock proves the long-lived child actually stopped.
			deadline := time.Now().Add(2 * time.Second)
			for syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
				if time.Now().After(deadline) {
					t.Fatal("subscriber child survived cleanup and still holds the app lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
				t.Fatal(err)
			}
			if outcome == "wrapper exit" {
				if err := os.Remove(filepath.Join(root, "exit-wrapper")); err != nil {
					t.Fatal(err)
				}
			}
			// Reload the same wrapper against the same OS lock. A real second
			// subscriber must reach readiness, not just pass a marker-file check.
			reloadCtx, stopReload := context.WithTimeout(context.Background(), 3*time.Second)
			defer stopReload()
			reloaded := false
			err = (EntryReceiver{Config: fixtureConfig(), Binary: binary}).Run(reloadCtx, func(context.Context, EntryEvent) error { return nil }, func(kind, _ string) error {
				if kind == "connected" {
					reloaded = true
					stopReload()
				}
				return nil
			})
			// The wrapper-exit fixture can close its stream while cancellation
			// becomes ready. Either exit proves cleanup after successful reload.
			if !reloaded || err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("reload could not acquire released subscriber lock: ready=%v err=%v", reloaded, err)
			}
		})
	}
}
