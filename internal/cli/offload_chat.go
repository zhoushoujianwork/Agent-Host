package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

// Local one-shot and Web room entries share the same host. Room execution owns
// durable request deduplication and a serialized native continuation lease.
func runOffloadChat(ctx context.Context, o options, out io.Writer) (runErr error) {
	if !o.oneShot || o.takeoverSession != "" || o.renewRuntime {
		return errors.New("Offload Chat requires -m with --resume or a room/request ID; interactive, takeover and runtime renewal are unsupported")
	}
	if o.room != "" {
		if o.requestID == "" || o.resume != "" || len(o.room) > 200 || len(o.requestID) > 200 {
			return errors.New("room Chat requires a bounded request ID without --resume")
		}
	} else if o.requestID != "" || o.receivedAt != "" {
		return errors.New("request ID and receipt time require a room")
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
	if o.room != "" {
		var received time.Time
		if o.receivedAt != "" {
			received, err = time.Parse(time.RFC3339Nano, o.receivedAt)
			if err != nil {
				return err
			}
		}
		turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		_, err = h.RunRoom(turnCtx, state.RoomInput{Key: "web-cli:" + o.room, RequestID: o.requestID, SenderID: "local-owner", SenderName: "Local owner", Text: o.message, ReceivedAt: received})
		return err
	}
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
