package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type ChannelSelection struct {
	Mode       string   `json:"mode"`
	ProjectIDs []string `json:"project_ids"`
	PrimaryID  string   `json:"primary_id,omitempty"`
}

// ChannelChoice keeps the original input and the later user authorization apart.
type ChannelChoice struct {
	ID                    string          `json:"id"`
	ChannelMessageID      string          `json:"channel_message_id"`
	Binding               string          `json:"binding"`
	Status                string          `json:"status"`
	Question              string          `json:"question"`
	CandidateIDs          []string        `json:"candidate_ids,omitempty"`
	Decision              json.RawMessage `json:"decision,omitempty"`
	ConfirmationMessageID string          `json:"confirmation_message_id,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
}

func (s *Store) ChannelSelection(ctx context.Context, binding string) (ChannelSelection, error) {
	var selection ChannelSelection
	var payload []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM channel_selections WHERE binding=?", binding).Scan(&payload)
	if err == nil {
		err = json.Unmarshal(payload, &selection)
	}
	return selection, wrap("read channel selection", err)
}

func (s *Store) SaveChannelRoutingDecision(ctx context.Context, messageID string, decision json.RawMessage) error {
	if !json.Valid(decision) || len(decision) > 512<<10 {
		return errors.New("channel routing requires valid decision JSON within its byte limit")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, messageID)
		if err != nil {
			return err
		}
		if m.Status != "claimed" || m.RunID != "" {
			return errors.New("routing decision requires an unexecuted claimed message")
		}
		if len(m.RoutingDecision) > 0 && string(m.RoutingDecision) != string(decision) {
			return errors.New("persisted routing decision cannot be changed")
		}
		m.RoutingDecision = append(json.RawMessage(nil), decision...)
		return writeChannelMessage(ctx, tx, m)
	})
}

func (s *Store) SetChannelSelection(ctx context.Context, binding string, selection ChannelSelection) error {
	if strings.TrimSpace(binding) == "" || selection.Mode != "manual" && selection.Mode != "auto" {
		return errors.New("channel selection requires a binding and manual or auto mode")
	}
	if selection.Mode == "auto" && (len(selection.ProjectIDs) > 0 || selection.PrimaryID != "") {
		return errors.New("automatic routing cannot retain a manual project pin")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		seen := map[string]bool{}
		ids := append([]string(nil), selection.ProjectIDs...)
		for _, id := range ids {
			if id == "" || seen[id] {
				return errors.New("selected projects must be nonempty and unique")
			}
			if _, err := readProject(ctx, tx, id); err != nil {
				return err
			}
			seen[id] = true
		}
		if selection.PrimaryID != "" && !seen[selection.PrimaryID] {
			return errors.New("primary project must be selected")
		}
		sort.Strings(ids)
		selection.ProjectIDs = ids
		payload, err := json.Marshal(selection)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO channel_selections(binding,payload) VALUES(?,?) ON CONFLICT(binding) DO UPDATE SET payload=excluded.payload", binding, payload); err != nil {
			return err
		}
		legacyID := selection.PrimaryID
		if len(ids) == 1 {
			legacyID = ids[0]
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO channel_projects(binding,project_id) VALUES(?,?) ON CONFLICT(binding) DO UPDATE SET project_id=excluded.project_id", binding, legacyID)
		return err
	})
}

func readChannelChoice(ctx context.Context, q rowQuerier, id string) (ChannelChoice, error) {
	var choice ChannelChoice
	var payload []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM channel_choices WHERE id=?", id).Scan(&payload)
	if err == nil {
		err = json.Unmarshal(payload, &choice)
	}
	return choice, wrap("read channel choice", err)
}

func writeChannelChoice(ctx context.Context, tx *sql.Tx, choice ChannelChoice) error {
	payload, err := json.Marshal(choice)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE channel_choices SET status=?,confirmation_message_id=?,payload=? WHERE id=?", choice.Status, choice.ConfirmationMessageID, payload, choice.ID)
	return err
}

func (s *Store) ChannelChoice(ctx context.Context, id string) (ChannelChoice, error) {
	return readChannelChoice(ctx, s.db, id)
}

func (s *Store) PendingChannelChoices(ctx context.Context, binding string, senderID ...string) ([]ChannelChoice, error) {
	if len(senderID) > 1 {
		return nil, errors.New("pending choices accept at most one sender")
	}
	query := "SELECT c.payload FROM channel_choices c"
	args := []any{binding}
	if len(senderID) == 1 {
		query += " JOIN channel_messages m ON m.id=c.channel_message_id"
	}
	query += " WHERE c.binding=? AND c.status='pending'"
	if len(senderID) == 1 {
		// Filter before the limit: other group members' questions must not hide
		// the requesting member's still-pending clarification.
		query += " AND json_extract(m.payload,'$.sender_id')=?"
		args = append(args, senderID[0])
	}
	query += " ORDER BY c.rowid DESC LIMIT 10"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var choices []ChannelChoice
	for rows.Next() {
		var payload []byte
		var choice ChannelChoice
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &choice); err != nil {
			return nil, err
		}
		choices = append(choices, choice)
	}
	return choices, rows.Err()
}

// WaitChannelChoice is durable waiting, not an interrupted native execution.
func (s *Store) WaitChannelChoice(ctx context.Context, messageID string, choice ChannelChoice) error {
	if choice.ID == "" || choice.Question == "" || len(choice.Question) > 4096 || len(choice.Decision) > 0 && !json.Valid(choice.Decision) {
		return errors.New("channel clarification requires an ID, bounded question and valid decision")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := readChannelMessage(ctx, tx, messageID)
		if err != nil {
			return err
		}
		if m.Status != "claimed" || m.RunID != "" || m.RouteBinding != "" {
			return errors.New("only an unexecuted claimed input can await clarification")
		}
		choice.ChannelMessageID, choice.Binding, choice.Status, choice.ConfirmationMessageID = m.ID, m.Binding, "pending", ""
		choice.CreatedAt = time.Now().UTC()
		payload, err := json.Marshal(choice)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO channel_choices(id,channel_message_id,binding,status,payload) VALUES(?,?,?,'pending',?)", choice.ID, m.ID, m.Binding, payload)
		if err != nil {
			return err
		}
		m.Status = "awaiting"
		return writeChannelMessage(ctx, tx, m)
	})
}

func choiceMessages(ctx context.Context, tx *sql.Tx, choiceID, confirmationID string) (ChannelChoice, ChannelMessage, ChannelMessage, error) {
	choice, err := readChannelChoice(ctx, tx, choiceID)
	if err != nil {
		return choice, ChannelMessage{}, ChannelMessage{}, err
	}
	original, err := readChannelMessage(ctx, tx, choice.ChannelMessageID)
	if err != nil {
		return choice, original, ChannelMessage{}, err
	}
	confirmation, err := readChannelMessage(ctx, tx, confirmationID)
	if err != nil {
		return choice, original, confirmation, err
	}
	if choice.Status != "pending" || original.Status != "awaiting" || original.RunID != "" || confirmation.Status != "claimed" {
		return choice, original, confirmation, errors.New("clarification is no longer pending or its confirmation is not claimed")
	}
	if choice.Binding != confirmation.Binding || original.Channel != confirmation.Channel || original.AccountID != confirmation.AccountID || original.TenantID != confirmation.TenantID || original.ConversationID != confirmation.ConversationID || original.SenderID != confirmation.SenderID {
		return choice, original, confirmation, errors.New("clarification confirmation does not match verified conversation identity")
	}
	return choice, original, confirmation, nil
}

// ClaimChannelChoice authorizes exactly one attempt of the original task. The
// confirmation remains a separate receipt and owns its fresh reply capability.
func (s *Store) ClaimChannelChoice(ctx context.Context, choiceID, confirmationID, supplement string) (ChannelMessage, error) {
	var original ChannelMessage
	if len(supplement) > 16<<10 {
		return original, errors.New("clarification supplement exceeds 16 KiB")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		choice, m, confirmation, err := choiceMessages(ctx, tx, choiceID, confirmationID)
		if err != nil {
			return err
		}
		if supplement != "" {
			if !strings.HasSuffix(strings.TrimSpace(confirmation.Text), supplement) {
				return errors.New("clarification supplement must retain text from the confirmation")
			}
			m.Supplements = append(m.Supplements, UserSupplement{MessageID: confirmation.MessageID, Text: supplement})
		}
		m.Status = "claimed"
		choice.Status, choice.ConfirmationMessageID = "claimed", confirmation.ID
		// A fresh explicit confirmation authorizes the current runtime profile.
		// Obsolete unattempted feedback must not block its result feedback.
		if _, err := tx.ExecContext(ctx, "UPDATE channel_reactions SET status='skipped',error=?,updated_at=? WHERE channel_message_id=? AND profile<>? AND status='ready' AND stage NOT IN ('completed','failed')", "explicit confirmation resumed with a different runtime profile", time.Now().UTC().Format(reactionTimeFormat), m.ID, confirmation.Profile); err != nil {
			return err
		}
		if err := writeChannelMessage(ctx, tx, m); err != nil {
			return err
		}
		if err := writeChannelChoice(ctx, tx, choice); err != nil {
			return err
		}
		original = m
		return nil
	})
	return original, wrap("claim channel clarification", err)
}

func (s *Store) CancelChannelChoice(ctx context.Context, choiceID, confirmationID string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		choice, original, confirmation, err := choiceMessages(ctx, tx, choiceID, confirmationID)
		if err != nil {
			return err
		}
		original.Status, original.Error = "failed", "request canceled by explicit clarification command"
		choice.Status, choice.ConfirmationMessageID = "canceled", confirmation.ID
		if _, err := tx.ExecContext(ctx, "UPDATE channel_reactions SET status='skipped',error=?,updated_at=? WHERE channel_message_id=? AND profile<>? AND status='ready' AND stage NOT IN ('completed','failed')", "explicit cancellation used a different runtime profile", time.Now().UTC().Format(reactionTimeFormat), original.ID, confirmation.Profile); err != nil {
			return err
		}
		if err := writeChannelMessage(ctx, tx, original); err != nil {
			return err
		}
		return writeChannelChoice(ctx, tx, choice)
	})
}

func finishChoiceReply(ctx context.Context, tx *sql.Tx, confirmation ChannelMessage) error {
	var choiceID string
	err := tx.QueryRowContext(ctx, "SELECT id FROM channel_choices WHERE confirmation_message_id=? AND status IN ('claimed','canceled')", confirmation.ID).Scan(&choiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	choice, err := readChannelChoice(ctx, tx, choiceID)
	if err != nil {
		return err
	}
	original, err := readChannelMessage(ctx, tx, choice.ChannelMessageID)
	if err != nil {
		return err
	}
	if original.Status != "completed" && original.Status != "failed" {
		return errors.New("clarification result reply precedes execution completion")
	}
	if choice.Status == "claimed" {
		choice.Status = "resolved"
		if err := writeChannelChoice(ctx, tx, choice); err != nil {
			return err
		}
	}
	// Final feedback follows the result delivery, rather than the earlier question.
	original.ReplyStatus = confirmation.ReplyStatus
	return queueChannelReaction(ctx, tx, original, terminalReactionStage(original))
}

func channelResultDelivery(ctx context.Context, q rowQuerier, original ChannelMessage) (ChannelMessage, error) {
	var confirmationID string
	err := q.QueryRowContext(ctx, "SELECT confirmation_message_id FROM channel_choices WHERE channel_message_id=? AND confirmation_message_id<>''", original.ID).Scan(&confirmationID)
	if errors.Is(err, sql.ErrNoRows) {
		return original, nil
	}
	if err != nil {
		return original, err
	}
	confirmation, err := readChannelMessage(ctx, q, confirmationID)
	if err != nil {
		return original, err
	}
	original.ReplyStatus = confirmation.ReplyStatus
	return original, nil
}

func recoverChannelChoices(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM channel_choices WHERE status='claimed'")
	if err != nil {
		return err
	}
	var choices []ChannelChoice
	for rows.Next() {
		var payload []byte
		var choice ChannelChoice
		if err = rows.Scan(&payload); err != nil {
			return errors.Join(err, rows.Close())
		}
		if err = json.Unmarshal(payload, &choice); err != nil {
			return errors.Join(err, rows.Close())
		}
		choice.Status = "unknown"
		choices = append(choices, choice)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, choice := range choices {
		if err := writeChannelChoice(ctx, tx, choice); err != nil {
			return err
		}
	}
	return nil
}

// RecentChannelRoutes follows execution order, including a delayed confirmation.
func (s *Store) RecentChannelRoutes(ctx context.Context, binding string, limit int) ([]ChannelMessage, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("recent channel route limit must be 1–100")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM channel_messages WHERE json_extract(payload,'$.binding')=? AND COALESCE(json_extract(payload,'$.route_binding'),'')<>'' AND COALESCE(json_extract(payload,'$.run_id'),'')<>'' ORDER BY route_order DESC,received_at DESC,rowid DESC LIMIT ?", binding, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []ChannelMessage
	for rows.Next() {
		var payload []byte
		var m ChannelMessage
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// RecentChannelRoutingContext excludes tasks whose native context was explicitly
// reset. A newest reset has no prior task to continue, while the display APIs
// retain the last actual execution and its project selection.
func (s *Store) RecentChannelRoutingContext(ctx context.Context, binding string, limit int) ([]ChannelMessage, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("recent channel routing limit must be 1–100")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM channel_messages WHERE json_extract(payload,'$.binding')=? AND COALESCE(json_extract(payload,'$.route_binding'),'')<>'' ORDER BY route_order DESC,received_at DESC,rowid DESC", binding)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []ChannelMessage
	reset := map[string]bool{}
	first := true
	for rows.Next() {
		var payload []byte
		var m ChannelMessage
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(m.Projects))
		for _, project := range m.Projects {
			ids = append(ids, project.ID)
		}
		if len(ids) == 0 && m.ProjectID != "" {
			ids = append(ids, m.ProjectID)
		}
		sort.Strings(ids)
		set, _ := json.Marshal(ids)
		if m.RunID == "" {
			if first {
				return nil, rows.Err()
			}
			reset[string(set)] = true
		} else if !reset[string(set)] {
			messages = append(messages, m)
			if len(messages) == limit {
				return messages, rows.Err()
			}
		}
		first = false
	}
	return messages, rows.Err()
}
