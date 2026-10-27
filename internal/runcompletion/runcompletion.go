// Package runcompletion carries a request-scoped hook that Host invokes once,
// right after native execution is durably completed and before built-in
// memory and experience closeout starts. The hook belongs to one request; it is
// never stored in shared Host configuration.
package runcompletion

import (
	"context"

	"agent-host/internal/state"
)

// Handler receives the saved run snapshot: execution completed, memory and
// experience still pending. A handler error is journaled by Host and never
// suppresses closeout or replays native execution.
type Handler func(context.Context, state.Run) error

type key struct{}

// With attaches the handler to one request. A nil handler returns ctx unchanged.
func With(ctx context.Context, h Handler) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, key{}, h)
}

// From returns the request handler, or nil when none is attached.
func From(ctx context.Context) Handler {
	h, _ := ctx.Value(key{}).(Handler)
	return h
}

// Without hides the handler from nested work so it cannot fire twice.
func Without(ctx context.Context) context.Context {
	if From(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, key{}, Handler(nil))
}
