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
	QuietWindow       time.Duration
	PollInterval      time.Duration
	Ticks             <-chan time.Time
	Probe             ActivityProbe
	Run               func(context.Context) ([]state.DreamReceipt, error)
	Now               func() time.Time
}

type Activity struct {
	Busy           bool
	LastForeground time.Time
}

type ActivityProbe func(context.Context) (Activity, error)

const (
	defaultQuietWindow  = time.Minute
	defaultPollInterval = time.Second
)

func (b Background) Serve(ctx context.Context) error {
	if b.QuietWindow == 0 {
		b.QuietWindow = defaultQuietWindow
	}
	if b.PollInterval == 0 {
		b.PollInterval = defaultPollInterval
	}
	if b.Store == nil || b.ConfigID == "" || b.Interval < time.Minute || b.Timeout <= 0 || b.Timeout > b.Interval || b.QuietWindow <= 0 || b.PollInterval <= 0 || b.Probe == nil || b.Run == nil {
		return errors.New("invalid dream background configuration")
	}
	ticks := b.Ticks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(b.PollInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	counter := 0
	quietBoundary := b.now()
	lastAdmitted := time.Time{}
	lastDeferred := ""
	saveCycle := func(cycle state.DreamCycle) error {
		return b.Store.SaveDreamCycle(context.Background(), cycle)
	}
	deferCycle := func(now time.Time, reason string) error {
		if reason == lastDeferred {
			return nil
		}
		lastDeferred = reason
		counter++
		id := hashStrings(b.ConfigID, now.Format(time.RFC3339Nano), string(rune(counter)))
		finished := now
		return saveCycle(state.DreamCycle{ID: "dream-cycle-" + id[:24], ConfigID: b.ConfigID, Status: "deferred", Error: reason, StartedAt: now, FinishedAt: &finished})
	}
	run := func() error {
		now := b.now()
		activity, err := b.Probe(ctx)
		if err != nil {
			quietBoundary = now
			return deferCycle(now, "activity probe failed: "+err.Error())
		}
		if activity.LastForeground.After(quietBoundary) {
			quietBoundary = activity.LastForeground
		}
		if activity.Busy {
			quietBoundary = now
			return deferCycle(now, "foreground work is active")
		}
		if now.Before(quietBoundary.Add(b.QuietWindow)) {
			return deferCycle(now, "foreground quiet window has not elapsed")
		}
		if !lastAdmitted.IsZero() && now.Before(lastAdmitted.Add(b.Interval)) {
			return deferCycle(now, "minimum cycle interval has not elapsed")
		}
		lastDeferred = ""
		lastAdmitted = now
		counter++
		id := hashStrings(b.ConfigID, now.Format(time.RFC3339Nano), string(rune(counter)))
		cycle := state.DreamCycle{ID: "dream-cycle-" + id[:24], ConfigID: b.ConfigID, Status: "running", StartedAt: now}
		if err := b.Store.SaveDreamCycle(ctx, cycle); err != nil {
			return err
		}
		cycleCtx, cancel := context.WithTimeout(ctx, b.Timeout)
		type outcome struct {
			receipts []state.DreamReceipt
			err      error
		}
		result := make(chan outcome, 1)
		go func() {
			receipts, runErr := b.Run(cycleCtx)
			result <- outcome{receipts: receipts, err: runErr}
		}()
		poll := time.NewTicker(b.PollInterval)
		var out outcome
		cancelReason := ""
	monitor:
		for {
			select {
			case out = <-result:
				break monitor
			case <-cycleCtx.Done():
				if cancelReason == "" {
					cancelReason = cycleCtx.Err().Error()
				}
				out = <-result
				break monitor
			case <-poll.C:
				current, probeErr := b.Probe(cycleCtx)
				if probeErr != nil {
					quietBoundary = b.now()
					cancelReason = "activity probe failed: " + probeErr.Error()
					cancel()
				} else if current.Busy || current.LastForeground.After(now) {
					quietBoundary = b.now()
					if current.LastForeground.After(quietBoundary) {
						quietBoundary = current.LastForeground
					}
					cancelReason = "foreground work arrived"
					cancel()
				}
			}
		}
		poll.Stop()
		cancel()
		receipts, err := out.receipts, out.err
		finished := b.now()
		cycle.FinishedAt = &finished
		for _, r := range receipts {
			cycle.Receipts = append(cycle.Receipts, r.ID)
		}
		cycle.Status = "completed"
		if cancelReason != "" {
			cycle.Status = "canceled"
			cycle.Error = cancelReason
		} else if err != nil {
			cycle.Status = "failed"
			cycle.Error = err.Error()
		}
		if saveErr := saveCycle(cycle); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		// Ordinary source/model failures are durable cycle outcomes. The next
		// tick retries normal lifecycle work; only persistence failure is fatal.
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			now := b.now()
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

func (b Background) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}
