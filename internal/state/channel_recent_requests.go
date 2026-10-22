package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ChannelRequestContext carries only an earlier authenticated sender's request.
// Supplied quotes remain separate reference material. It conveys no prior
// execution result, attachment, native state, or additional grant.
type ChannelRequestContext struct {
	MessageID      string        `json:"message_id"`
	SenderID       string        `json:"sender_id"`
	Input          string        `json:"input"`
	InputTruncated bool          `json:"input_truncated"`
	Quote          *ChannelQuote `json:"quote,omitempty"`
	InstanceIDs    []string      `json:"instance_ids,omitempty"`
}

// ChannelRecentRequests supplies a bounded request handoff for a fresh channel
// session, including when a model/runtime change prevents native continuation.
// The current receipt fixes the source snapshot and exact project authority.
// Broader conversation transcripts remain available only through history tools.
func (s *Store) ChannelRecentRequests(ctx context.Context, sessionID, currentReceiptID string) ([]ChannelRequestContext, error) {
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
		return nil, errors.New("recent request session identity is invalid")
	}
	current, err := readChannelMessage(ctx, tx, currentReceiptID)
	if err != nil {
		return nil, err
	}
	if !historyIdentityValid(current) || current.SessionID != session.ID || current.RunID == "" {
		return nil, errors.New("recent requests require the current channel task")
	}
	currentRun, err := historyRun(ctx, tx, current)
	if err != nil {
		return nil, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE session_id=?", session.ID).Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 || currentRun == nil || currentRun.ExecutionStatus != "running" || !recoveryRunMatchesSession(*currentRun, session) {
		return nil, errors.New("recent requests require the first running task in the matching new session")
	}
	var snapshot int64
	if err := tx.QueryRowContext(ctx, "SELECT rowid FROM channel_messages WHERE id=?", currentReceiptID).Scan(&snapshot); err != nil {
		return nil, err
	}
	agent, audience := normalizedPartition(current.AgentID, current.AudienceID)
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.payload FROM channel_messages m
		JOIN runs r ON r.id=json_extract(m.payload,'$.run_id')
		WHERE m.rowid<? AND m.channel=? AND m.account_id=?
		AND json_extract(m.payload,'$.tenant_id')=? AND json_extract(m.payload,'$.conversation_id')=?
		AND COALESCE(NULLIF(json_extract(m.payload,'$.agent_id'),''),'default')=?
		AND COALESCE(NULLIF(json_extract(m.payload,'$.audience_id'),''),'owner')=?
		AND COALESCE(NULLIF(json_extract(m.payload,'$.conversation_type'),''),'1')=?
		AND COALESCE(json_extract(m.payload,'$.task_root_id'),'')=?
 AND r.scope=? AND COALESCE(json_extract(r.payload,'$.project_id'),'')=?
		ORDER BY m.rowid DESC LIMIT 8`, snapshot, current.Channel, current.AccountID, current.TenantID,
		current.ConversationID, agent, audience, conversationType(current.ConversationType), current.TaskRootID, currentRun.Scope, currentRun.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []ChannelRequestContext
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var prior ChannelMessage
		if json.Unmarshal(raw, &prior) != nil || prior.ID != id || !historyIdentityValid(prior) || !sameRecoveryConversation(prior, current) {
			return nil, errors.New("recent request source does not match its authenticated conversation")
		}
		priorRun, err := historyRun(ctx, tx, prior)
		if err != nil {
			return nil, err
		}
		if priorRun == nil || priorRun.Scope != currentRun.Scope || priorRun.ProjectID != currentRun.ProjectID || !sameProjectRefs(priorRun.Projects, currentRun.Projects) {
			continue
		}
		input, truncated := recoveryText(prior.Text, 4000)
		requests = append(requests, ChannelRequestContext{MessageID: prior.MessageID, SenderID: prior.SenderID, Input: input, InputTruncated: truncated, Quote: contextQuote(prior.Input)})
		if len(requests) == 3 {
			break
		}
	}
	return requests, rows.Err()
}
