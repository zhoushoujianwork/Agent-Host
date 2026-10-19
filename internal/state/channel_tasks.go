package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func cardTaskBinding(base, root string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("private-card-task-v1:"+base+":"+root)))
}

// bindPrivateCardTask runs only for a new durable receipt. A redelivery never
// changes task identity or replays a previously rejected card.
func bindPrivateCardTask(ctx context.Context, tx *sql.Tx, m *ChannelMessage) error {
	m.TaskRootID, m.ConversationBinding, m.AmbiguousTask = "", "", false
	if m.Channel != "dingtalk" || m.ConversationType != "1" {
		return nil
	}
	base := m.Binding
	bind := func(root string) {
		m.ConversationBinding = base
		m.TaskRootID = root
		m.Binding = cardTaskBinding(base, root)
	}
	if m.MessageType == "interactiveCard" && m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "interactiveCard" && strings.TrimSpace(m.Text) != "" {
		bind(m.ID)
		return nil
	}
	if m.MessageType != "text" || strings.HasPrefix(strings.TrimSpace(m.Text), "/") {
		return nil
	}
	reference := m.ReplyToID
	if m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageID != "" {
		reference = m.Input.Quote.MessageID
	}
	scope := []any{m.Channel, m.AccountID, m.TenantID, m.ConversationID, m.SenderID, m.AgentID, m.AudienceID, base}
	where := `channel=? AND account_id=? AND json_extract(CAST(payload AS TEXT),'$.tenant_id')=? AND json_extract(CAST(payload AS TEXT),'$.conversation_id')=? AND json_extract(CAST(payload AS TEXT),'$.sender_id')=? AND COALESCE(json_extract(CAST(payload AS TEXT),'$.agent_id'),'')=? AND COALESCE(json_extract(CAST(payload AS TEXT),'$.audience_id'),'')=? AND json_extract(CAST(payload AS TEXT),'$.conversation_binding')=?`
	if reference != "" {
		var payload []byte
		args := append(append([]any{}, scope...), reference)
		err := tx.QueryRowContext(ctx, "SELECT payload FROM channel_messages WHERE "+where+" AND message_id=? LIMIT 1", args...).Scan(&payload)
		if err == nil {
			var prior ChannelMessage
			if err := json.Unmarshal(payload, &prior); err != nil {
				return err
			}
			if prior.TaskRootID != "" {
				bind(prior.TaskRootID)
				return nil
			}
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	if !cardContinuation(m.Text) {
		return nil
	}
	// Unquoted confirmations are safe only with exactly one recent card task.
	// /new clears this implicit selection; explicit quoted references still work.
	args := append(append([]any{}, scope...), time.Now().UTC().Add(-24*time.Hour).Format("2006-01-02T15:04:05.000000000Z"), base)
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM channel_messages WHERE "+where+` AND json_extract(CAST(payload AS TEXT),'$.task_root_id')=id AND received_at>=? AND received_at>COALESCE((SELECT MAX(received_at) FROM channel_messages WHERE json_extract(CAST(payload AS TEXT),'$.binding')=? AND TRIM(json_extract(CAST(payload AS TEXT),'$.text'))='/new' AND json_extract(CAST(payload AS TEXT),'$.input_material') IS NULL),'') ORDER BY received_at DESC,rowid DESC LIMIT 2`, args...)
	if err != nil {
		return err
	}
	var roots []string
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		var prior ChannelMessage
		if err := json.Unmarshal(payload, &prior); err != nil {
			rows.Close()
			return err
		}
		roots = append(roots, prior.TaskRootID)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if len(roots) == 1 && reference == "" {
		bind(roots[0])
	} else if len(roots) > 0 {
		m.AmbiguousTask = true
	}
	return nil
}

func cardContinuation(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"同意", "批准", "继续", "确认处理", "处理一下", "帮我处理"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	for _, word := range []string{"ok", "yes", "approve", "continue", "好的", "好"} {
		if text == word {
			return true
		}
	}
	return false
}
