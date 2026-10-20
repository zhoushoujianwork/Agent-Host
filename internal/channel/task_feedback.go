package channel

import (
	"context"
	"errors"

	"agent-host/internal/state"
)

// TaskFeedbackText shares the normal channel formatter, including observed
// usage and sanitized terminal failures, with background recipient delivery.
func TaskFeedbackText(run state.Run) string { return resultTextWithNotice(run, nil, "", 3995) }

// DeliverRecoveredFeedback repairs only an unsent/confirmed-failed original
// reply. It never dispatches a task, changes the audience or retries unknown sends.
func (s *Service) DeliverRecoveredFeedback(ctx context.Context, m state.ChannelMessage, run state.Run) error {
	if m.AccountID != s.ClientID || run.Source == nil || run.Source.ChannelMessageID != m.ID || m.RunID != run.ID {
		return errors.New("feedback source mismatch")
	}
	store := s.Store.WithPartition(m.AgentID, m.AudienceID)
	text := m.ReplyText
	if text == "" {
		text = resultTextWithNotice(run, nil, "", replyBodyBudget(m))
	}
	message := replyForMessage(m, text)
	if m.ReplyTarget != nil {
		message.Text = apiReplyText(text, replyBodyBudget(m))
	}
	// ReplyText is already the exact final transport body after a failed send.
	if m.ReplyText != "" {
		message.Text = m.ReplyText
	}
	claimed, err := store.PrepareRecoveredChannelReply(ctx, m.ID, run.ID, message.Text)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	child := *s
	child.Store = store
	return child.deliverChannelReply(ctx, m, message)
}
