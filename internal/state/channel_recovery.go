package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"agent-host/internal/event"
)

// ChannelRecoveryContext is linked task receipt evidence, not a native
// transcript or a conversation-history grant. Original authorship is retained;
// a repeated recovery can retain up to three already-supplied linked tasks.
type ChannelRecoveryContext struct {
	SessionID        string                  `json:"session_id"`
	RunID            string                  `json:"run_id"`
	MessageID        string                  `json:"message_id"`
	SenderID         string                  `json:"sender_id"`
	Input            string                  `json:"input"`
	Quote            *ChannelQuote           `json:"quote,omitempty"`
	ExecutionStatus  string                  `json:"execution_status"`
	MemoryStatus     string                  `json:"memory_status"`
	Reply            string                  `json:"reply"`
	DeliveryStatus   string                  `json:"delivery_status"`
	InputTruncated   bool                    `json:"input_truncated"`
	ReplyTruncated   bool                    `json:"reply_truncated"`
	Earlier          *ChannelRecoveryContext `json:"earlier,omitempty"`
	EarlierTruncated bool                    `json:"earlier_truncated,omitempty"`
}

// ChannelRecoveryContext reads only the task that caused this session's
// recovery. Evidence remains available until a completed run has received it,
// including sessions recovered before this bridge existed. Unresolved project
// attribution still prevents broader conversation-history access.
func (s *Store) ChannelRecoveryContext(ctx context.Context, sessionID, currentReceiptID string) (*ChannelRecoveryContext, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	session, err := s.readSession(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ID != sessionID {
		return nil, errors.New("recovery session identity is invalid")
	}
	if session.PreviousSessionID == "" && session.RecoveryRunID == "" {
		return nil, nil
	}
	if session.PreviousSessionID == "" || session.RecoveryRunID == "" || session.PreviousSessionID == session.ID {
		return nil, errors.New("recovery session links are invalid")
	}
	current, err := readChannelMessage(ctx, tx, currentReceiptID)
	if err != nil {
		return nil, err
	}
	if !historyIdentityValid(current) || current.SessionID != session.ID || current.RunID == "" {
		return nil, errors.New("recovery context requires the current channel task")
	}
	currentRun, err := historyRun(ctx, tx, current)
	if err != nil {
		return nil, err
	}
	if currentRun == nil || currentRun.ExecutionStatus != "running" || !recoveryRunMatchesSession(*currentRun, session) {
		return nil, errors.New("recovery context requires a running task in the matching session")
	}
	var supplied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM runs prior JOIN events e ON e.run_id=prior.id
		WHERE prior.session_id=? AND prior.execution_status='completed'
		AND prior.rowid<(SELECT rowid FROM runs WHERE id=?)
		AND json_extract(e.payload,'$.type')='conversation.recovery.assembled'
		AND json_extract(e.payload,'$.data.quote_context_included')=1)`, session.ID, currentRun.ID).Scan(&supplied); err != nil {
		return nil, err
	}
	if supplied {
		return nil, nil
	}
	return s.channelRecoveryEvidence(ctx, tx, session, current, 3, map[string]bool{})
}

func (s *Store) channelRecoveryEvidence(ctx context.Context, tx *sql.Tx, session Session, current ChannelMessage, remaining int, seen map[string]bool) (*ChannelRecoveryContext, error) {
	if session.PreviousSessionID == "" || session.RecoveryRunID == "" || seen[session.ID] || seen[session.PreviousSessionID] || session.PreviousSessionID == session.ID {
		return nil, errors.New("recovery evidence links are incomplete or cyclic")
	}
	seen[session.ID] = true
	previous, err := s.readSession(ctx, tx, session.PreviousSessionID)
	if err != nil {
		return nil, err
	}
	if previous.ID != session.PreviousSessionID || !sameRecoveryRuntime(previous, session) {
		return nil, errors.New("recovery context cannot cross runtime boundaries")
	}
	priorRun, err := s.readRun(ctx, tx, session.RecoveryRunID)
	if err != nil {
		return nil, err
	}
	if priorRun.ID != session.RecoveryRunID || !recoveryRunMatchesSession(priorRun, previous) {
		return nil, errors.New("recovery task does not match the linked previous session")
	}
	var latestID, executionStatus, memoryStatus string
	if err := tx.QueryRowContext(ctx, "SELECT id,execution_status,memory_status FROM runs WHERE session_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1", previous.ID).Scan(&latestID, &executionStatus, &memoryStatus); err != nil {
		return nil, err
	}
	if latestID != priorRun.ID || executionStatus != priorRun.ExecutionStatus || memoryStatus != priorRun.MemoryStatus || !recoveryTaskTerminal(executionStatus) {
		return nil, errors.New("recovery task outcome does not match its saved evidence")
	}
	if priorRun.Source == nil {
		// Legacy local runs have no authenticated channel receipt to share.
		return nil, nil
	}
	prior, err := readChannelMessage(ctx, tx, priorRun.Source.ChannelMessageID)
	if err != nil {
		return nil, err
	}
	if !historyIdentityValid(prior) || !sameRecoveryConversation(prior, current) {
		return nil, errors.New("recovery context cannot cross authenticated conversation boundaries")
	}
	validated, err := historyRun(ctx, tx, prior)
	if err != nil {
		return nil, err
	}
	if validated == nil || validated.ID != priorRun.ID {
		return nil, errors.New("recovery task is not linked to its original receipt")
	}
	input, inputTruncated := recoveryText(prior.Text, 4000)
	reply, replyTruncated := recoveryText(prior.ReplyText, 2000)
	evidence := &ChannelRecoveryContext{
		SessionID: previous.ID, RunID: priorRun.ID, MessageID: prior.MessageID, SenderID: prior.SenderID,
		Input: input, Quote: contextQuote(prior.Input), ExecutionStatus: executionStatus, MemoryStatus: memoryStatus,
		Reply: reply, DeliveryStatus: prior.ReplyStatus, InputTruncated: inputTruncated, ReplyTruncated: replyTruncated,
	}
	assembled, err := recoveryWasAssembled(ctx, tx, priorRun)
	if err != nil {
		return nil, err
	}
	if assembled && (previous.PreviousSessionID != "" || previous.RecoveryRunID != "") {
		if remaining <= 1 {
			evidence.EarlierTruncated = true
		} else {
			evidence.Earlier, err = s.channelRecoveryEvidence(ctx, tx, previous, prior, remaining-1, seen)
			if err != nil {
				return nil, err
			}
		}
	}
	return evidence, nil
}

func recoveryWasAssembled(ctx context.Context, tx *sql.Tx, run Run) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM events WHERE run_id=? AND json_extract(payload,'$.type')='conversation.recovery.assembled'", run.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	assembled := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var e event.Event
		if json.Unmarshal(raw, &e) != nil || e.RunID != run.ID || e.SessionID != run.SessionID {
			return false, errors.New("recovery assembly event does not match its task")
		}
		assembled = true
	}
	return assembled, rows.Err()
}

func sameRecoveryRuntime(a, b Session) bool {
	return samePartition(a.AgentID, a.AudienceID, b.AgentID, b.AudienceID) &&
		a.Scope == b.Scope && a.ProjectID == b.ProjectID && a.WorkingDirectory() == b.WorkingDirectory() &&
		a.RuntimeFingerprint == b.RuntimeFingerprint && a.Model == b.Model &&
		sameProjectRefs(a.Projects, b.Projects) && sameDirectories(a.AdditionalDirectories, b.AdditionalDirectories)
}

func recoveryRunMatchesSession(run Run, session Session) bool {
	return run.SessionID == session.ID && run.NativeSessionID == session.NativeSessionID &&
		samePartition(run.AgentID, run.AudienceID, session.AgentID, session.AudienceID) &&
		run.Scope == session.Scope && run.ProjectID == session.ProjectID && run.WorkingDirectory() == session.WorkingDirectory() &&
		run.RuntimeFingerprint == session.RuntimeFingerprint && run.RequestedModel == session.Model &&
		sameProjectRefs(run.Projects, session.Projects) && sameDirectories(run.AdditionalDirectories, session.AdditionalDirectories)
}

func sameRecoveryConversation(a, b ChannelMessage) bool {
	return a.Channel == b.Channel && a.AccountID == b.AccountID && a.TenantID == b.TenantID &&
		a.ConversationID == b.ConversationID && conversationType(a.ConversationType) == conversationType(b.ConversationType) &&
		samePartition(a.AgentID, a.AudienceID, b.AgentID, b.AudienceID)
}

func recoveryTaskTerminal(status string) bool {
	return status == "failed" || status == "canceled" || status == "unknown" || status == "completed"
}

func recoveryText(text string, limit int) (string, bool) {
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}

// Quotes retain their own source identity and preview flags. Never include
// attachment capabilities or local paths when carrying earlier reference text.
func contextQuote(input *ChannelInput) *ChannelQuote {
	if input == nil || input.Quote == nil {
		return nil
	}
	quote := *input.Quote
	for _, field := range []struct {
		text  *string
		limit int
	}{
		{&quote.Body, 4000}, {&quote.Title, 256},
		{&quote.MessageID, 512}, {&quote.MessageType, 128},
	} {
		text, truncated := recoveryText(*field.text, field.limit)
		*field.text = text
		quote.Truncated = quote.Truncated || truncated
	}
	return &quote
}
