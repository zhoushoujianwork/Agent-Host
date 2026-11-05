package channel

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

func (s *Service) workProgress(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, m, err := s.Store.ClaimNextChannelProgress(ctx, s.Profile)
		if errors.Is(err, sql.ErrNoRows) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			continue
		}
		if err != nil {
			return err
		}
		if p.Status != "pending" {
			continue
		}
		if err = s.processProgress(ctx, p, m); err != nil {
			return err
		}
	}
}

// The durable dispatch claim precedes any provider call. This path deliberately
// does not use deliverChannelReply or update the final reply lifecycle.
func (s *Service) processProgress(ctx context.Context, p state.ChannelProgress, m state.ChannelMessage) error {
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	status := "failed"
	sendErr := errors.New("channel progress transport unavailable")
	if m.ReplyTarget != nil {
		if s.TextReplier != nil {
			target := *m.ReplyTarget
			// The final reply owns its own UUID. Derive a separate stable operation
			// identity from this durable attempt without changing the destination.
			target.UUID = Key("channel-progress-v1", m.ID, p.RunID, strconv.FormatInt(p.ID, 10))[:48]
			status, sendErr = s.TextReplier.ReplyText(sendCtx, target, p.Text)
		}
	} else if s.Replier != nil {
		url, err := s.Store.ChannelReplyURL(sendCtx, m.ID)
		if err == nil {
			status, sendErr = s.Replier.Reply(sendCtx, url, m.ReplyExpiresAt, dingtalk.ReplyMessage{Text: p.Text})
		} else {
			sendErr = err
		}
	}
	if status != "accepted" && status != "failed" && status != "unknown" || status == "accepted" && sendErr != nil || status != "accepted" && sendErr == nil {
		status = "unknown"
	}
	// Provider errors may contain credentials and webhook URLs. Persist only the
	// classified outcome; never forward raw provider errors through MCP or logs.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	return s.Store.FinishChannelProgress(finishCtx, p.ID, status)
}
