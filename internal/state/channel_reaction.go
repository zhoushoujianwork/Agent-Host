package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/event"
)

var reactionLabels = map[string]string{
	"received": "已收到", "processing": "处理中", "completed": "已完成", "failed": "打叉",
	"replying": "发送回复",
}

const reactionColumns = "id,channel_message_id,stage,label,status,error,active,removal_status,removal_error,source_run_id,source_event_seq,created_at,updated_at"
const reactionTimeFormat = "2006-01-02T15:04:05.000000000Z"

// queueChannelReaction participates in the message transition transaction.
// Only receipt creates eligibility, so a migration never sends historical markers.
func queueChannelReaction(ctx context.Context, tx *sql.Tx, m ChannelMessage, stage string) error {
	return queueChannelReactionSource(ctx, tx, m, stage, "", 0)
}

func queueChannelReactionSource(ctx context.Context, tx *sql.Tx, m ChannelMessage, stage, runID string, seq int64) error {
	label, ok := reactionLabels[stage]
	if phase, registered := event.Phase(stage); registered && phase.Channel {
		label, ok = phase.Label, true
	}
	if !ok {
		return errors.New("invalid channel reaction stage")
	}
	return queueChannelReactionLabel(ctx, tx, m, stage, label, runID, seq)
}

func queueChannelReactionLabel(ctx context.Context, tx *sql.Tx, m ChannelMessage, stage, label, runID string, seq int64) error {
	if m.DisableReactions {
		return nil
	}
	media := (m.MessageType == "picture" || m.MessageType == "file") && m.Input != nil && len(m.Input.Attachments) > 0
	forwarded := m.MessageType == "markdown" && strings.TrimSpace(m.Text) != "" && m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "markdown" && strings.TrimSpace(m.Input.Quote.Body) != ""
	if !media && !forwarded && m.MessageType != "text" && (m.MessageType != "richText" || strings.TrimSpace(m.Text) == "") {
		return nil
	}
	text := m.Text
	if m.Channel == "dingtalk" {
		text = dingtalk.AddressedText(m.ConversationType, text)
	}
	if m.Input == nil && dingtalk.IsSlashCommand(text) {
		// Supported commands and unsupported slash requests are answered by the
		// Host without native execution, so neither acquires task feedback.
		return nil
	}
	if stage != "received" {
		var eligible bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM channel_reactions WHERE channel_message_id=? AND stage='received')", m.ID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return nil
		}
	}
	if stage == "completed" || stage == "failed" {
		var terminal bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM channel_reactions WHERE channel_message_id=? AND stage IN ('completed','failed'))", m.ID).Scan(&terminal); err != nil {
			return err
		}
		if terminal {
			return nil
		}
	}
	now := time.Now().UTC().Format(reactionTimeFormat)
	profile := m.Profile
	var confirmationProfile string
	err := tx.QueryRowContext(ctx, "SELECT c.profile FROM channel_choices q JOIN channel_messages c ON c.id=q.confirmation_message_id WHERE q.channel_message_id=? AND q.confirmation_message_id<>''", m.ID).Scan(&confirmationProfile)
	if err == nil {
		profile = confirmationProfile
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var occurrence int64
	if phase, ok := event.Phase(stage); ok && phase.Repeatable && runID != "" {
		occurrence = seq
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO channel_reactions(channel_message_id,profile,stage,label,status,source_run_id,source_event_seq,occurrence,created_at,updated_at) VALUES(?,?,?,?,'ready',?,?,?,?,?) ON CONFLICT(channel_message_id,stage,occurrence) DO NOTHING", m.ID, profile, stage, label, runID, seq, occurrence, now, now)
	if err != nil {
		return wrap("queue channel reaction", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 || stage == "received" {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE channel_reactions SET status='skipped',error=?,updated_at=? WHERE channel_message_id=? AND id<? AND status='ready' AND stage NOT IN ('received','completed','failed')", "superseded by a later durable stage", now, m.ID, id)
	return wrap("supersede channel reactions", err)
}

// projectChannelPhase derives feedback solely from an attributed durable host
// event. It neither invokes a transport nor accepts a caller-provided route.
func projectChannelPhase(ctx context.Context, tx *sql.Tx, run Run, e event.Event) error {
	label, render := event.PhaseReaction(e)
	if !render || run.Source == nil {
		return nil
	}
	source := run.Source
	m, err := readChannelMessage(ctx, tx, source.ChannelMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.ConversationType != "" {
		return nil
	}
	scope := m.Scope
	if m.RouteBinding != "" {
		scope = m.ResolvedScope
		if m.ProjectID != run.ProjectID || m.Workspace != run.Workspace || m.RuntimeFingerprint != run.RuntimeFingerprint || !sameProjectRefs(m.Projects, run.Projects) || !sameDirectories(m.AdditionalDirectories, run.AdditionalDirectories) {
			return nil
		}
	}
	if m.Status != "claimed" || m.RunID != run.ID || m.SessionID != run.SessionID || scope != run.Scope ||
		source.Channel != m.Channel || source.AccountID != m.AccountID || source.TenantID != m.TenantID ||
		source.MessageID != m.MessageID || source.ConversationID != m.ConversationID || source.SenderID != m.SenderID {
		return nil
	}
	return queueChannelReactionLabel(ctx, tx, m, e.Phase, label, run.ID, e.Seq)
}

func terminalReactionStage(m ChannelMessage) string {
	if m.Status == "completed" && m.ReplyStatus == "accepted" {
		return "completed"
	}
	return "failed"
}

type reactionScanner interface{ Scan(...any) error }

func scanChannelReaction(row reactionScanner) (ChannelReaction, error) {
	var r ChannelReaction
	var created, updated string
	if err := row.Scan(&r.ID, &r.ChannelMessageID, &r.Stage, &r.Label, &r.Status, &r.Error, &r.Active, &r.RemovalStatus, &r.RemovalError, &r.SourceRunID, &r.SourceEventSeq, &created, &updated); err != nil {
		return r, err
	}
	var err error
	if r.CreatedAt, err = time.Parse(reactionTimeFormat, created); err != nil {
		return r, err
	}
	r.UpdatedAt, err = time.Parse(reactionTimeFormat, updated)
	return r, err
}

// ClaimNextChannelReaction claims an unattempted stage. Earlier stages of the
// same message must finish first, even if more than one worker calls this API.
func (s *Store) ClaimNextChannelReaction(ctx context.Context, profile string) (ChannelReaction, error) {
	var reaction ChannelReaction
	if strings.TrimSpace(profile) == "" {
		return reaction, errors.New("channel reaction profile is required")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT r.id FROM channel_reactions r
WHERE r.profile=? AND r.status='ready'
AND NOT EXISTS(SELECT 1 FROM channel_reactions p WHERE p.channel_message_id=r.channel_message_id AND p.id<r.id AND p.status IN ('ready','sending'))
ORDER BY r.id LIMIT 1`, profile).Scan(&id); err != nil {
			return err
		}
		now := time.Now().UTC().Format(reactionTimeFormat)
		if _, err := tx.ExecContext(ctx, "UPDATE channel_reactions SET status='sending',updated_at=? WHERE id=? AND status='ready'", now, id); err != nil {
			return err
		}
		var err error
		reaction, err = scanChannelReaction(tx.QueryRowContext(ctx, "SELECT "+reactionColumns+" FROM channel_reactions WHERE id=?", id))
		return err
	})
	return reaction, wrap("claim channel reaction", err)
}

func (s *Store) channelReactions(ctx context.Context, messageID string, activeOnly bool) ([]ChannelReaction, error) {
	query := "SELECT " + reactionColumns + " FROM channel_reactions WHERE channel_message_id=?"
	if activeOnly {
		query += " AND active=1"
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id", messageID)
	if err != nil {
		return nil, wrap("list channel reactions", err)
	}
	defer rows.Close()
	reactions := []ChannelReaction{}
	for rows.Next() {
		reaction, err := scanChannelReaction(rows)
		if err != nil {
			return nil, err
		}
		reactions = append(reactions, reaction)
	}
	return reactions, rows.Err()
}

func (s *Store) ChannelReactions(ctx context.Context, messageID string) ([]ChannelReaction, error) {
	return s.channelReactions(ctx, messageID, false)
}

func (s *Store) ActiveChannelReactions(ctx context.Context, messageID string) ([]ChannelReaction, error) {
	return s.channelReactions(ctx, messageID, true)
}

// ClaimChannelReactionRemoval durably records that cleanup is about to be
// attempted. A later stage cannot silently repeat a failed or uncertain remove.
func (s *Store) ClaimChannelReactionRemoval(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "UPDATE channel_reactions SET removal_status='sending',updated_at=? WHERE id=? AND status='accepted' AND active=1 AND removal_status='pending'", time.Now().UTC().Format(reactionTimeFormat), id)
	if err != nil {
		return wrap("claim channel reaction removal", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("only active accepted channel reactions with unattempted removal can be claimed")
	}
	return nil
}

// ClearChannelReaction records acceptance of a previously claimed removal.
func (s *Store) ClearChannelReaction(ctx context.Context, id int64) error {
	return s.FinishChannelReactionRemoval(ctx, id, "accepted", "")
}

// FinishChannelReactionRemoval keeps the original add outcome intact. Active
// remains true unless the platform explicitly accepted the removal.
func (s *Store) FinishChannelReactionRemoval(ctx context.Context, id int64, status, errorText string) error {
	if status != "accepted" && status != "failed" && status != "unknown" {
		return fmt.Errorf("invalid channel reaction removal terminal status %q", status)
	}
	if status == "accepted" && errorText != "" || status != "accepted" && strings.TrimSpace(errorText) == "" {
		return errors.New("channel reaction removal failure or unknown outcome requires an error; acceptance cannot include an error")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE channel_reactions SET removal_status=?,removal_error=?,active=?,updated_at=? WHERE id=? AND status='accepted' AND active=1 AND removal_status='sending'", status, errorText, status != "accepted", time.Now().UTC().Format(reactionTimeFormat), id)
	if err != nil {
		return wrap("finish channel reaction removal", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("only sending channel reaction removals can finish")
	}
	return nil
}

// FinishChannelReaction preserves transport acceptance separately from task
// truth. Failed or uncertain attempts cannot return to the ready queue.
func (s *Store) FinishChannelReaction(ctx context.Context, id int64, status, errorText string) error {
	if status != "accepted" && status != "failed" && status != "unknown" {
		return fmt.Errorf("invalid channel reaction terminal status %q", status)
	}
	if status == "accepted" && errorText != "" || status != "accepted" && strings.TrimSpace(errorText) == "" {
		return errors.New("channel reaction failure or unknown outcome requires an error; acceptance cannot include an error")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE channel_reactions SET status=?,error=?,active=?,updated_at=? WHERE id=? AND status='sending'", status, errorText, status == "accepted", time.Now().UTC().Format(reactionTimeFormat), id)
	if err != nil {
		return wrap("finish channel reaction", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("only sending channel reactions can finish")
	}
	return nil
}

// recoverChannelReactions runs after message recovery, in its transaction.
// It resumes only never-attempted work and skips obsolete intermediate stages.
func recoverChannelReactions(ctx context.Context, tx *sql.Tx) error {
	now := time.Now().UTC().Format(reactionTimeFormat)
	if _, err := tx.ExecContext(ctx, "UPDATE channel_reactions SET status='unknown',error=?,updated_at=? WHERE status='sending'", "host stopped before recording reaction acceptance; reaction was not replayed", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE channel_reactions SET removal_status='unknown',removal_error=?,updated_at=? WHERE removal_status='sending'", "host stopped before recording reaction removal acceptance; removal was not replayed", now); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.payload FROM channel_messages m
WHERE m.status IN ('completed','failed','unknown')
AND EXISTS(SELECT 1 FROM channel_reactions r WHERE r.channel_message_id=m.id AND r.stage='received')`)
	if err != nil {
		return err
	}
	var messages []ChannelMessage
	for rows.Next() {
		var payload []byte
		var m ChannelMessage
		if err = rows.Scan(&payload); err != nil {
			return errors.Join(err, rows.Close())
		}
		if err = json.Unmarshal(payload, &m); err != nil {
			return errors.Join(err, rows.Close())
		}
		messages = append(messages, m)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, m := range messages {
		m, err = channelResultDelivery(ctx, tx, m)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE channel_reactions SET status='skipped',error=?,updated_at=? WHERE channel_message_id=? AND stage NOT IN ('completed','failed') AND status='ready'", "message was already terminal at recovery", now, m.ID); err != nil {
			return err
		}
		if err := queueChannelReaction(ctx, tx, m, terminalReactionStage(m)); err != nil {
			return err
		}
	}
	return nil
}

// SkipChannelReaction withdraws a claimed, undispatched legacy phase marker.
// It cannot alter an accepted or uncertain platform operation.
func (s *Store) SkipChannelReaction(ctx context.Context, id int64, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("skipped channel reaction requires a reason")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE channel_reactions SET status='skipped',active=0,error=?,updated_at=? WHERE id=? AND status='sending'", reason, time.Now().UTC().Format(reactionTimeFormat), id)
	if err != nil {
		return wrap("skip channel reaction", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("only sending channel reactions can be skipped")
	}
	return nil
}
