package cli

import (
	"context"
	"errors"

	"agent-host/internal/host"
	"agent-host/internal/state"
)

// Web opts into the interactive CLI's pre-dispatch renewal behavior. This is
// not a retry: no task has been dispatched and an unfinished run stays blocked.
func renewChatRuntime(ctx context.Context, h *host.Host, previous state.Session, o *options) (state.Session, error) {
	agent, audience := previous.AgentID, previous.AudienceID
	if agent == "" {
		agent = state.DefaultAgentID
	}
	if audience == "" {
		audience = state.OwnerAudienceID
	}
	if agent != o.agentID || audience != o.audienceID {
		return previous, errors.New("session belongs to a different Agent or audience")
	}
	if err := h.Store.SessionCanResume(ctx, previous.ID); err != nil {
		return previous, err
	}
	if previous.RuntimeFingerprint == o.runtimeFingerprint && previous.Model == o.model {
		return h.Session(ctx, previous.ID)
	}
	o.renewedFrom = previous.ID
	return h.Session(ctx, "")
}
