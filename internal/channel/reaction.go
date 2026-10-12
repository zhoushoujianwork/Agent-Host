package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

func (s *Service) workReactions(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		reaction, err := s.Store.ClaimNextChannelReaction(ctx, s.Profile)
		if errors.Is(err, sql.ErrNoRows) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("claim channel reaction: %w", err)
		}
		if err := s.processReaction(ctx, reaction); err != nil {
			return err
		}
	}
}

// Each stage gets one attempt. Platform failures are distinct recorded outcomes;
// storage and observation failures stop the worker rather than losing evidence.
func (s *Service) processReaction(ctx context.Context, reaction state.ChannelReaction) error {
	started := time.Now()
	switch reaction.Stage {
	case "received", "processing", "completed", "failed":
	default:
		// A pre-upgrade outbox may still contain internal phases. Do not send
		// those labels under the public policy or rewrite historical delivery.
		const reason = "internal phase is not public progress"
		if err := s.Store.SkipChannelReaction(ctx, reaction.ID, reason); err != nil {
			return err
		}
		reaction.Status, reaction.Error, reaction.Active = "skipped", reason, false
		return s.observeReaction("channel.reaction.finished", reaction, time.Since(started))
	}
	m, err := s.Store.ChannelMessage(ctx, reaction.ChannelMessageID)
	if err != nil {
		return fmt.Errorf("read reaction target: %w", err)
	}
	previous, err := s.Store.ActiveChannelReactions(ctx, m.ID)
	if err != nil {
		return fmt.Errorf("read previous reactions: %w", err)
	}
	if err := s.observeReaction("channel.reaction.started", reaction, 0); err != nil {
		return err
	}
	// One bounded context includes token acquisition, cleanup, and addition.
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var sendErr error
	operation := "add"
	for _, old := range previous {
		operation = "remove"
		if old.RemovalStatus != "pending" {
			sendErr = errors.New("previous reaction removal is unresolved; replacement was not attempted")
			break
		}
		if err := s.Store.ClaimChannelReactionRemoval(sendCtx, old.ID); err != nil {
			return fmt.Errorf("claim reaction removal: %w", err)
		}
		sendErr = s.Reactor.RemoveReaction(sendCtx, m.ConversationID, m.MessageID, old.Label)
		// Record confirmed cleanup even if shutdown arrived with its response.
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		removalError := ""
		if sendErr != nil {
			removalError = sendErr.Error()
		}
		err = s.Store.FinishChannelReactionRemoval(finishCtx, old.ID, dingtalk.ReactionOutcome(sendErr), removalError)
		finishCancel()
		if err != nil {
			return fmt.Errorf("record reaction removal: %w", err)
		}
		if sendErr != nil {
			break
		}
	}
	if sendErr == nil {
		operation = "add"
		sendErr = s.Reactor.AddReaction(sendCtx, m.ConversationID, m.MessageID, reaction.Label)
	}
	status, cause := dingtalk.ReactionOutcome(sendErr), ""
	if sendErr != nil {
		cause = operation + ": " + sendErr.Error()
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err := s.Store.FinishChannelReaction(finishCtx, reaction.ID, status, cause); err != nil {
		return fmt.Errorf("record channel reaction: %w", err)
	}
	reaction.Status, reaction.Error, reaction.Active = status, cause, status == "accepted"
	return s.observeReaction("channel.reaction.finished", reaction, time.Since(started))
}

func (s *Service) observeReaction(kind string, reaction state.ChannelReaction, duration time.Duration) error {
	if s.ObserveReaction != nil {
		return s.ObserveReaction(kind, reaction, duration)
	}
	return s.observe(kind, fmt.Sprintf("message=%s stage=%s label=%s status=%s duration_ms=%d error=%q", reaction.ChannelMessageID, reaction.Stage, reaction.Label, reaction.Status, duration.Milliseconds(), reaction.Error))
}
