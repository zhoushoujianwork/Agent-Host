package dream

import (
	"context"
	"errors"
	"time"

	"agent-host/internal/state"
)

type Background struct {
	Store             *state.Store
	ConfigID          string
	Interval, Timeout time.Duration
	Ticks             <-chan time.Time
	Run               func(context.Context) ([]state.DreamReceipt, error)
	Now               func() time.Time
}

func (b Background) Serve(ctx context.Context) error {
	if b.Store == nil || b.ConfigID == "" || b.Interval < time.Minute || b.Timeout <= 0 || b.Timeout > b.Interval || b.Run == nil {
		return errors.New("invalid dream background configuration")
	}
	ticks := b.Ticks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(b.Interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	counter := 0
	run := func() error {
		counter++
		now := time.Now().UTC()
		if b.Now != nil {
			now = b.Now().UTC()
		}
		id := hashStrings(b.ConfigID, now.Format(time.RFC3339Nano), string(rune(counter)))
		cycle := state.DreamCycle{ID: "dream-cycle-" + id[:24], ConfigID: b.ConfigID, Status: "running", StartedAt: now}
		if err := b.Store.SaveDreamCycle(ctx, cycle); err != nil {
			return err
		}
		cycleCtx, cancel := context.WithTimeout(ctx, b.Timeout)
		receipts, err := b.Run(cycleCtx)
		cancel()
		finished := time.Now().UTC()
		cycle.FinishedAt = &finished
		for _, r := range receipts {
			cycle.Receipts = append(cycle.Receipts, r.ID)
		}
		cycle.Status = "completed"
		if err != nil {
			cycle.Status = "failed"
			cycle.Error = err.Error()
		}
		if saveErr := b.Store.SaveDreamCycle(context.Background(), cycle); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		// Ordinary source/model failures are durable cycle outcomes. The next
		// tick retries normal lifecycle work; only persistence failure is fatal.
		return nil
	}
	if err := run(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			now := time.Now().UTC()
			if b.Now != nil {
				now = b.Now().UTC()
			}
			finished := now
			id := hashStrings(b.ConfigID, "stopped", now.Format(time.RFC3339Nano))
			saveErr := b.Store.SaveDreamCycle(context.Background(), state.DreamCycle{ID: "dream-cycle-" + id[:24], ConfigID: b.ConfigID, Status: "canceled", Error: ctx.Err().Error(), StartedAt: now, FinishedAt: &finished})
			return errors.Join(ctx.Err(), saveErr)
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
			if err := run(); err != nil {
				return err
			}
		}
	}
}
