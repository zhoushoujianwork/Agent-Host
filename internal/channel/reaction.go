package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	if m.DisableReactions || m.Channel == state.ReplyTargetFeishuMeeting {
		return s.skipReaction(ctx, reaction, "receipt does not permit reaction feedback", started)
	}
	if m.Channel == state.ReplyTargetFeishu {
		return s.processAPIReaction(ctx, m, reaction, started)
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

func (s *Service) skipReaction(ctx context.Context, reaction state.ChannelReaction, reason string, started time.Time) error {
	if err := s.Store.SkipChannelReaction(ctx, reaction.ID, reason); err != nil {
		return err
	}
	reaction.Status, reaction.Error, reaction.Active = "skipped", reason, false
	return s.observeReaction("channel.reaction.finished", reaction, time.Since(started))
}

func (s *Service) processAPIReaction(ctx context.Context, m state.ChannelMessage, reaction state.ChannelReaction, started time.Time) error {
	previous, err := s.Store.ChannelReactions(ctx, m.ID)
	if err != nil {
		return fmt.Errorf("read API reaction history: %w", err)
	}
	for _, old := range previous {
		if old.ID < reaction.ID && old.Status == "unknown" {
			return s.skipReaction(ctx, reaction, "previous API reaction outcome is unknown; later feedback was not attempted", started)
		}
		if old.Active && (old.RemovalStatus != "pending" || !state.ValidProviderReactionID(old.ProviderReactionID)) {
			return s.skipReaction(ctx, reaction, "previous API reaction removal is unresolved; replacement was not attempted", started)
		}
	}
	if err := s.observeReaction("channel.reaction.started", reaction, 0); err != nil {
		return err
	}
	// Cleanup and addition share one bounded request lifetime. Finalization uses
	// a fresh context so shutdown cannot discard an acknowledged platform result.
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	providerID, status := "", "failed"
	var sendErr error
	if s.APIReactor == nil || m.ReplyTarget == nil || m.ReplyTarget.Kind != state.ReplyTargetFeishu || m.ReplyTarget.ID != m.ConversationID || m.ReplyTarget.MessageID != m.MessageID {
		sendErr = errors.New("Feishu reaction adapter or durable target is unavailable")
	} else {
		for _, old := range previous {
			if !old.Active {
				continue
			}
			if err := s.Store.ClaimChannelReactionRemoval(sendCtx, old.ID); err != nil {
				return fmt.Errorf("claim API reaction removal: %w", err)
			}
			removalStatus, removalErr := s.APIReactor.RemoveReaction(sendCtx, *m.ReplyTarget, old.ProviderReactionID)
			removalStatus, removalErr = apiReactionOutcome(removalStatus, removalErr)
			cause := ""
			if removalErr != nil {
				cause = removalErr.Error()
			}
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = s.Store.FinishChannelReactionRemoval(finishCtx, old.ID, removalStatus, cause)
			finishCancel()
			if err != nil {
				return fmt.Errorf("record API reaction removal: %w", err)
			}
			if removalErr != nil {
				// The removal has its own failed/unknown receipt. This add was
				// definitely withheld, so it must not claim platform acceptance.
				sendErr = fmt.Errorf("remove: %w", removalErr)
				break
			}
		}
		if sendErr == nil {
			providerID, status, sendErr = s.APIReactor.AddReaction(sendCtx, *m.ReplyTarget, reaction.Label)
			status, sendErr = apiReactionOutcome(status, sendErr)
			if status == "accepted" && !state.ValidProviderReactionID(providerID) || status != "accepted" && providerID != "" {
				status, sendErr = "unknown", errors.New("API reaction returned inconsistent provider identity")
			}
			if status != "accepted" {
				providerID = ""
			}
		}
	}
	cause := ""
	if sendErr != nil {
		cause = sendErr.Error()
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err := s.Store.FinishChannelAPIReaction(finishCtx, reaction.ID, providerID, status, cause); err != nil {
		return fmt.Errorf("record API channel reaction: %w", err)
	}
	reaction.ProviderReactionID = providerID
	reaction.Status, reaction.Error, reaction.Active = status, cause, status == "accepted"
	return s.observeReaction("channel.reaction.finished", reaction, time.Since(started))
}

// An adapter must provide explicit, consistent evidence. An invalid contract
// cannot become acceptance or a confidently failed operation after dispatch.
func apiReactionOutcome(status string, err error) (string, error) {
	if status == "accepted" && err == nil || (status == "failed" || status == "unknown") && err != nil && strings.TrimSpace(err.Error()) != "" {
		return status, err
	}
	return "unknown", errors.New("API reaction returned inconsistent delivery evidence")
}

func (s *Service) observeReaction(kind string, reaction state.ChannelReaction, duration time.Duration) error {
	if s.ObserveReaction != nil {
		return s.ObserveReaction(kind, reaction, duration)
	}
	return s.observe(kind, fmt.Sprintf("message=%s stage=%s label=%s status=%s duration_ms=%d error=%q", reaction.ChannelMessageID, reaction.Stage, reaction.Label, reaction.Status, duration.Milliseconds(), reaction.Error))
}
