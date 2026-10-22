package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
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
	if m.MessageType != "text" && m.MessageType != "richText" || strings.HasPrefix(strings.TrimSpace(m.Text), "/") {
		return nil
	}
	reference := m.ReplyToID
	if m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageID != "" {
		reference = m.Input.Quote.MessageID
	}
	scope := []any{m.Channel, m.AccountID, m.TenantID, m.ConversationID, m.SenderID, m.AgentID, m.AudienceID, base}
	where := `channel=? AND account_id=? AND json_extract(CAST(payload AS TEXT),'$.tenant_id')=? AND json_extract(CAST(payload AS TEXT),'$.conversation_id')=? AND json_extract(CAST(payload AS TEXT),'$.sender_id')=? AND COALESCE(json_extract(CAST(payload AS TEXT),'$.agent_id'),'')=? AND COALESCE(json_extract(CAST(payload AS TEXT),'$.audience_id'),'')=? AND COALESCE(NULLIF(json_extract(CAST(payload AS TEXT),'$.conversation_binding'),''),json_extract(CAST(payload AS TEXT),'$.binding'))=?`
	// Webhook replies do not return a provider message ID. Carry a receipt
	// reference in their title so a later title-only quotation is resolvable.
	var titleReference string
	if m.Input != nil && m.Input.Quote != nil {
		q := m.Input.Quote
		for _, text := range []string{q.Title, q.Body} {
			if match := cardReplyReference.FindStringSubmatch(text); match != nil {
				titleReference = match[1]
			}
		}
	}
	if reference != "" {
		var payload []byte
		args := append(append([]any{}, scope...), reference)
		err := tx.QueryRowContext(ctx, "SELECT payload FROM channel_messages WHERE "+where+" AND message_id=? LIMIT 1", args...).Scan(&payload)
		if err == nil {
			var prior ChannelMessage
			if err := json.Unmarshal(payload, &prior); err != nil {
				return err
			}
			if titleReference != "" && prior.ID[:min(16, len(prior.ID))] != titleReference {
				m.AmbiguousTask = true
				return nil
			}
			if prior.TaskRootID != "" || prior.Input != nil && prior.Input.Quote != nil && prior.Input.Quote.MessageType == "interactiveCard" {
				root := prior.TaskRootID
				if root == "" {
					root = prior.ID
				}
				bind(root)
				return nil
			}
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	if titleReference != "" {
		args := append(append([]any{}, scope...), titleReference)
		rows, err := tx.QueryContext(ctx, "SELECT payload FROM channel_messages WHERE "+where+" AND substr(id,1,16)=? AND json_extract(payload,'$.reply_status')='accepted' LIMIT 2", args...)
		if err != nil {
			return err
		}
		var matches []ChannelMessage
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var prior ChannelMessage
			if err := json.Unmarshal(raw, &prior); err != nil {
				rows.Close()
				return err
			}
			matches = append(matches, prior)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(matches) == 1 && matches[0].TaskRootID != "" {
			bind(matches[0].TaskRootID)
			return nil
		}
		m.AmbiguousTask = true
		return nil
	}
	if reference != "" && m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "interactiveCard" {
		// A card may first arrive as quoted material. Repeated references to
		// its exact provider ID select that receipt, never a similar title.
		args := append(append([]any{}, scope...), reference)
		rows, err := tx.QueryContext(ctx, "SELECT id FROM channel_messages WHERE "+where+" AND json_extract(payload,'$.task_root_id')=id AND json_extract(payload,'$.input_material.quote.message_id')=? LIMIT 2", args...)
		if err != nil {
			return err
		}
		var roots []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			roots = append(roots, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(roots) == 1 {
			bind(roots[0])
			return nil
		}
		if len(roots) == 0 && strings.TrimSpace(m.Input.Quote.Body) != "" {
			bind(m.ID)
			return nil
		}
		m.AmbiguousTask = true
		return nil
	}
	quotedTicketReply := m.Input != nil && m.Input.Quote != nil &&
		(m.Input.Quote.Body == "Agent-Host 回复" || m.Input.Quote.Title == "Agent-Host 回复")
	if !cardContinuation(m.Text) && !quotedTicketReply {
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
	} else if len(roots) > 0 || strings.Contains(m.Text, "工单") || strings.Contains(m.Text, "评论") || m.Input != nil && m.Input.Quote != nil && m.Input.Quote.MessageType == "interactiveCard" {
		m.AmbiguousTask = true
	}
	return nil
}

var cardReplyReference = regexp.MustCompile(`^Agent-Host 回复 \[agh:([A-Za-z0-9_-]{1,16})\]$`)

// CardReplyTitle links a title-only DingTalk quote to this receipt, not to the
// latest conversation turn. The reference conveys no execution permission.
func CardReplyTitle(m ChannelMessage) string {
	if m.TaskRootID == "" {
		return ""
	}
	id := m.ID
	if len(id) > 16 {
		id = id[:16]
	}
	return "Agent-Host 回复 [agh:" + id + "]"
}

func cardContinuation(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"同意", "批准", "继续", "确认处理", "处理一下", "帮我处理", "再次看下工单", "再看下工单", "能看到评论"} {
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
