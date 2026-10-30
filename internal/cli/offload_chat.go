package cli

import (
	"context"
	"errors"
	"io"
	"strings"

	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

// This first Chat entry is deliberately one-shot. Web rooms require durable
// request deduplication and fork handling before they can use this entry.
func runOffloadChat(ctx context.Context, o options, out io.Writer) (runErr error) {
	if !o.oneShot || o.room != "" || o.requestID != "" || o.receivedAt != "" || o.takeoverSession != "" || o.renewRuntime {
		return errors.New("Offload Chat currently requires -m with optional --resume; interactive, room, takeover and runtime renewal are not supported")
	}
	if o.requestFor != "" || o.notifyTo != "" || o.notifyChannel != "" || o.feedbackConfigDir != "" || o.feedbackOtherUsersOnly || o.knowledgeChannel != "" {
		return errors.New("Offload Chat does not support requester notifications or Host knowledge injection yet")
	}
	if strings.TrimSpace(o.message) == "" || len(o.message) > 16*1024 {
		return errors.New("provide a nonempty message up to 16 KiB")
	}
	if err := resolveOffloadChannelOptions(&o); err != nil {
		return err
	}
	store, release, err := openExecutionState(ctx, o.home, true)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close(), release()) }()
	var releaseSession func() error
	defer func() {
		if releaseSession != nil {
			runErr = errors.Join(runErr, releaseSession())
		}
	}()
	if o.resume != "" {
		releaseSession, err = state.AcquireChatSession(o.home, o.resume)
		if err != nil {
			return err
		}
	}
	renderer := terminal.Renderer{Out: out, JSON: o.format == "json"}
	h := composeOffloadChannelHost(store, o, renderer.Render).(*offloadchannel.Host)
	session, err := h.Session(ctx, o.resume)
	if err != nil {
		return err
	}
	if releaseSession == nil {
		releaseSession, err = state.AcquireChatSession(o.home, session.ID)
		if err != nil {
			return err
		}
	}
	turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	_, err = h.Run(turnCtx, session, o.message)
	return err
}
