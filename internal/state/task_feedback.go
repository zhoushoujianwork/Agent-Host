package state

import (
	"context"
	"database/sql"
	"errors"

	"agent-host/internal/taskfeedback"
)

// BindRunFeedback fixes task attribution before execution; it cannot redirect
// a completed task or replace an already bound feedback plan.
func (s *Store) BindRunFeedback(ctx context.Context, id string, plan *taskfeedback.Plan) error {
	if plan == nil {
		return errors.New("feedback plan required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		r, err := s.readRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.ExecutionStatus != "running" || r.Feedback != nil {
			return errors.New("feedback plan is already bound or execution ended")
		}
		r.Feedback = plan
		return writeRun(ctx, tx, r)
	})
}

func (s *Store) PrepareRecoveredChannelReply(ctx context.Context, id, runID, text string) (bool, error) {
	claimed := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		r, err := s.readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if m.RunID != r.ID || r.Source == nil || r.Source.ChannelMessageID != m.ID || r.ExecutionStatus == "running" {
			return errors.New("feedback requires its terminal source run")
		}
		if m.ReplyStatus != "pending" && m.ReplyStatus != "failed" {
			return nil
		}
		if m.ReplyText != "" && m.ReplyText != text {
			return errors.New("feedback retry must preserve the exact reply")
		}
		if m.Status == "claimed" {
			m.Status = "completed"
			if r.ExecutionStatus != "completed" {
				m.Status = "failed"
				m.Error = "task ended without a confirmed successful result"
			}
		}
		m.ReplyStatus, m.ReplyText, m.ReplyError = "sending", text, ""
		claimed = true
		return writeChannelMessage(ctx, tx, m)
	})
	return claimed, err
}
