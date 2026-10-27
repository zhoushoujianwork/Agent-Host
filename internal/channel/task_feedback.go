package channel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agent-host/internal/state"
)

// TaskFeedbackText shares the normal channel formatter, including observed
// usage and sanitized terminal failures, with background recipient delivery.
func TaskFeedbackText(run state.Run) string { return resultTextWithNotice(run, nil, "", 3995) }

// DeliverRecoveredFeedback repairs only an unsent/confirmed-failed original
// reply. It never dispatches a task, changes the audience or retries unknown sends.
func (s *Service) DeliverRecoveredFeedback(ctx context.Context, m state.ChannelMessage, run state.Run) error {
	if m.ReplyKind == "recovery_notice" {
		return nil
	}
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

// Recovery notices close the communication gap without resolving execution.
func (s *Service) deliverRecoveryNotices(ctx context.Context) error {
	kind := s.Channel
	if kind == "" {
		kind = "dingtalk"
	}
	messages, err := s.Store.PendingRecoveryNotices(ctx, kind, s.ClientID, s.Profile)
	if err != nil {
		return err
	}
	for _, m := range messages {
		body := fmt.Sprintf("【中断恢复通知】Host 在处理此请求时停止，执行结果仍未知，未自动重放。部分操作可能已经发生，请先核查结果，再决定是否提交新请求。消息编号：%s", m.MessageID)
		message := replyForMessage(m, body)
		if m.ReplyTarget != nil {
			message.Text = apiReplyText(body, replyBodyBudget(m))
		}
		if err := s.Store.PrepareChannelReply(ctx, m.ID, message.Text); err != nil {
			return err
		}
		if err := s.observe("channel.recovery_notice", fmt.Sprintf("message=%s execution=unknown", m.ID)); err != nil {
			return err
		}
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if m.ReplyTarget == nil && !m.ReplyExpiresAt.After(time.Now()) {
			err = s.Store.FinishChannelReply(sendCtx, m.ID, "failed", "original reply destination expired; recovery notice was not sent")
		} else {
			err = s.deliverChannelReply(sendCtx, m, message)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
