package dream

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-host/internal/state"
)

type backgroundClock struct {
	mu    sync.Mutex
	now   time.Time
	ready chan struct{}
}

func (c *backgroundClock) Now() time.Time {
	c.mu.Lock()
	now := c.now
	c.mu.Unlock()
	select {
	case c.ready <- struct{}{}:
	default:
	}
	return now
}

func (c *backgroundClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	c.mu.Unlock()
	return now
}

func awaitSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for " + label)
	}
}

func awaitServe(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("Serve error = %v, want %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background shutdown")
	}
}

func sendTick(t *testing.T, ticks chan<- time.Time, at time.Time) {
	t.Helper()
	select {
	case ticks <- at:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending scheduler tick")
	}
}

func backgroundFixture(t *testing.T) (*state.Store, chan time.Time, *backgroundClock) {
	t.Helper()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ticks := make(chan time.Time, 4)
	clock := &backgroundClock{now: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC), ready: make(chan struct{}, 1)}
	return store, ticks, clock
}

func TestBackgroundHasNoStartupCallAndAdmitsAfterContinuousQuiet(t *testing.T) {
	store, ticks, clock := backgroundFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	probed := make(chan struct{}, 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	background := Background{Store: store, ConfigID: "quiet", Interval: time.Minute, Timeout: time.Second, QuietWindow: time.Minute, Ticks: ticks,
		Probe: func(context.Context) (Activity, error) { probed <- struct{}{}; return Activity{}, nil },
		Run: func(context.Context) ([]state.DreamReceipt, error) {
			started <- struct{}{}
			<-release
			return nil, nil
		}, Now: clock.Now}
	go func() { done <- background.Serve(ctx) }()
	awaitSignal(t, clock.ready, "scheduler startup")
	select {
	case <-started:
		t.Fatal("background ran unconditionally at startup")
	default:
	}
	sendTick(t, ticks, clock.Now())
	awaitSignal(t, probed, "initial quiet probe")
	sendTick(t, ticks, clock.Advance(time.Minute))
	awaitSignal(t, probed, "admission probe")
	awaitSignal(t, started, "quiet background admission")
	close(release)
	sendTick(t, ticks, clock.Now())
	awaitSignal(t, probed, "post-completion scheduler probe")
	cancel()
	awaitServe(t, done, context.Canceled)
	cycles, err := store.DreamCycles(context.Background(), "quiet", 10)
	if err != nil || !hasCycle(cycles, "deferred", "quiet window") || !hasCycle(cycles, "completed", "") || !hasCycle(cycles, "canceled", "context canceled") {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
}

func TestBackgroundForegroundArrivalCancelsCycleAndSchedulerRecovers(t *testing.T) {
	store, ticks, clock := backgroundFixture(t)
	var busy atomic.Bool
	var calls atomic.Int32
	firstStarted := make(chan struct{}, 1)
	secondStarted := make(chan struct{}, 1)
	firstCanceled := make(chan struct{}, 1)
	idleProbed := make(chan struct{}, 3)
	busyObserved := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	background := Background{Store: store, ConfigID: "cancel", Interval: time.Minute, Timeout: time.Second, QuietWindow: time.Nanosecond, PollInterval: time.Millisecond, Ticks: ticks,
		Probe: func(context.Context) (Activity, error) {
			isBusy := busy.Load()
			if isBusy {
				select {
				case busyObserved <- struct{}{}:
				default:
				}
			} else {
				idleProbed <- struct{}{}
			}
			return Activity{Busy: isBusy}, nil
		},
		Run: func(ctx context.Context) ([]state.DreamReceipt, error) {
			call := calls.Add(1)
			if call == 2 {
				secondStarted <- struct{}{}
				return []state.DreamReceipt{{ID: "recovered"}}, nil
			}
			firstStarted <- struct{}{}
			<-ctx.Done()
			firstCanceled <- struct{}{}
			return nil, ctx.Err()
		}, Now: clock.Now}
	go func() { done <- background.Serve(ctx) }()
	awaitSignal(t, clock.ready, "scheduler startup")
	sendTick(t, ticks, clock.Advance(time.Minute))
	awaitSignal(t, idleProbed, "first admission probe")
	awaitSignal(t, firstStarted, "first cycle start")
	busy.Store(true)
	awaitSignal(t, busyObserved, "busy foreground probe")
	awaitSignal(t, firstCanceled, "first cycle cancellation")
	busy.Store(false)
	sendTick(t, ticks, clock.Advance(time.Minute))
	awaitSignal(t, idleProbed, "recovery admission probe")
	awaitSignal(t, secondStarted, "recovery cycle start")
	sendTick(t, ticks, clock.Now())
	awaitSignal(t, idleProbed, "post-recovery scheduler probe")
	cancel()
	awaitServe(t, done, context.Canceled)
	cycles, err := store.DreamCycles(context.Background(), "cancel", 10)
	if err != nil || !hasCycle(cycles, "canceled", "foreground work arrived") || !hasCycle(cycles, "completed", "") {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
}

func TestBackgroundProbeFailureDefersAndDoesNotOverlap(t *testing.T) {
	store, ticks, clock := backgroundFixture(t)
	var probes atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	probeFailed := make(chan struct{}, 1)
	idleProbed := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	background := Background{Store: store, ConfigID: "probe", Interval: time.Minute, Timeout: time.Second, QuietWindow: time.Nanosecond, Ticks: ticks,
		Probe: func(context.Context) (Activity, error) {
			if probes.Add(1) == 1 {
				probeFailed <- struct{}{}
				return Activity{}, errors.New("unreadable state")
			}
			idleProbed <- struct{}{}
			return Activity{}, nil
		},
		Run: func(context.Context) ([]state.DreamReceipt, error) {
			current := active.Add(1)
			if current > maximum.Load() {
				maximum.Store(current)
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return nil, nil
		}, Now: clock.Now}
	go func() { done <- background.Serve(ctx) }()
	awaitSignal(t, clock.ready, "scheduler startup")
	sendTick(t, ticks, clock.Advance(time.Minute))
	awaitSignal(t, probeFailed, "failed activity probe")
	sendTick(t, ticks, clock.Advance(time.Minute))
	awaitSignal(t, idleProbed, "recovery activity probe")
	awaitSignal(t, started, "cycle start after probe recovery")
	sendTick(t, ticks, clock.Now())
	close(release)
	awaitSignal(t, idleProbed, "post-cycle scheduler probe")
	cancel()
	awaitServe(t, done, context.Canceled)
	cycles, err := store.DreamCycles(context.Background(), "probe", 10)
	if err != nil || !hasCycle(cycles, "deferred", "activity probe failed") || !hasCycle(cycles, "completed", "") || maximum.Load() != 1 {
		t.Fatalf("cycles=%+v max=%d err=%v", cycles, maximum.Load(), err)
	}
}

func hasCycle(cycles []state.DreamCycle, status, reason string) bool {
	for _, cycle := range cycles {
		if cycle.Status == status && strings.Contains(cycle.Error, reason) {
			return true
		}
	}
	return false
}

func TestBackgroundClosedTickChannelStopsWithoutCycle(t *testing.T) {
	store, ticks, _ := backgroundFixture(t)
	close(ticks)
	background := Background{Store: store, ConfigID: "closed", Interval: time.Minute, Timeout: time.Second, Ticks: ticks,
		Probe: func(context.Context) (Activity, error) { return Activity{}, nil }, Run: func(context.Context) ([]state.DreamReceipt, error) { return nil, nil }}
	if err := background.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	cycles, err := store.DreamCycles(context.Background(), "closed", 10)
	if err != nil || len(cycles) != 0 {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
}
