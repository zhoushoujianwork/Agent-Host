package owneranswering

import (
	"context"
	"sync"
)

type earlyOutcome struct {
	mu   sync.Mutex
	done bool
	err  error
}

type outcomeReadyKey struct{}

// OutcomeReady lets a Handle implementation hand over its parsed outcome as
// soon as native execution completes, before Host memory closeout. It is nil
// when the coordinator did not offer the request-scoped callback. The callback
// runs the coordinator's normal save/validate/deliver tail at most once.
func OutcomeReady(ctx context.Context) func(Outcome, error) error {
	f, _ := ctx.Value(outcomeReadyKey{}).(func(Outcome, error) error)
	return f
}

func withOutcomeReady(ctx context.Context, f func(Outcome, error) error) context.Context {
	return context.WithValue(ctx, outcomeReadyKey{}, f)
}
