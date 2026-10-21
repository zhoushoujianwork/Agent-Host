package cli

import (
	"context"
	"errors"

	"agent-host/internal/state"
)

func openExecutionState(ctx context.Context, home string, chat bool) (*state.Store, func() error, error) {
	acquire := state.Acquire
	if chat {
		acquire = state.AcquireChat
	}
	release, err := acquire(home)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*state.Store, func() error, error) {
		return nil, nil, errors.Join(err, release())
	}
	if chat {
		initialized, err := state.AcquireChatInitialization(ctx, home)
		if err != nil {
			return fail(err)
		}
		defer initialized()
	}
	store, err := state.Open(home, false)
	if err != nil {
		return fail(err)
	}
	if chat {
		if err := store.RecoverIdleChatRuns(ctx); err != nil {
			return fail(errors.Join(err, store.Close()))
		}
	}
	return store, release, nil
}
