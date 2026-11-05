package dream

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-host/internal/state"
)

func TestBackgroundTickCancelAndRestartRecordsStatus(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ticks := make(chan time.Time, 2)
	called := make(chan struct{}, 4)
	run := func(context.Context) ([]state.DreamReceipt, error) {
		called <- struct{}{}
		return []state.DreamReceipt{{ID: "same-receipt"}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	background := Background{Store: store, ConfigID: "fixture", Interval: time.Minute, Timeout: time.Second, Ticks: ticks, Run: run}
	go func() { done <- background.Serve(ctx) }()
	<-called
	ticks <- time.Now()
	<-called
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cycles, err := store.DreamCycles(context.Background(), "fixture", 10)
	if err != nil || len(cycles) != 3 || cycles[0].Status != "completed" || cycles[1].Status != "completed" || cycles[2].Status != "canceled" {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	background.Ticks = make(chan time.Time)
	go func() { done2 <- background.Serve(ctx2) }()
	<-called
	cancel2()
	<-done2
	cycles, err = store.DreamCycles(context.Background(), "fixture", 20)
	if err != nil || len(cycles) != 5 {
		t.Fatalf("restart cycles=%+v err=%v", cycles, err)
	}
}

func TestBackgroundClosedTickChannelStops(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ticks := make(chan time.Time)
	close(ticks)
	background := Background{Store: store, ConfigID: "closed", Interval: time.Minute, Timeout: time.Second, Ticks: ticks, Run: func(context.Context) ([]state.DreamReceipt, error) { return nil, nil }}
	if err = background.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	cycles, err := store.DreamCycles(context.Background(), "closed", 10)
	if err != nil || len(cycles) != 1 {
		t.Fatalf("closed ticks did not stop after initial cycle: %+v %v", cycles, err)
	}
}

func TestBackgroundReceiptPersistenceFailureIsFatal(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	background := Background{Store: store, ConfigID: "closed-store", Interval: time.Minute, Timeout: time.Second, Ticks: make(chan time.Time), Run: func(context.Context) ([]state.DreamReceipt, error) { return nil, nil }}
	if err = background.Serve(context.Background()); err == nil {
		t.Fatal("cycle receipt persistence failure was swallowed")
	}
}

func TestBackgroundTransientRunFailureContinuesNextTick(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ticks := make(chan time.Time, 1)
	called := make(chan int, 2)
	calls := 0
	background := Background{Store: store, ConfigID: "transient", Interval: time.Minute, Timeout: time.Second, Ticks: ticks, Run: func(context.Context) ([]state.DreamReceipt, error) {
		calls++
		called <- calls
		if calls == 1 {
			return nil, errors.New("transient model failure")
		}
		return []state.DreamReceipt{{ID: "recovered"}}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- background.Serve(ctx) }()
	if call := <-called; call != 1 {
		t.Fatalf("first call=%d", call)
	}
	ticks <- time.Now()
	if call := <-called; call != 2 {
		t.Fatalf("second call=%d", call)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cycles, err := store.DreamCycles(context.Background(), "transient", 10)
	if err != nil || len(cycles) != 3 || cycles[0].Status != "failed" || cycles[1].Status != "completed" || cycles[2].Status != "canceled" {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
}
