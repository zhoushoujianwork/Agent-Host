package state

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"slices"
)

// ChannelTicketContext supplies the selected card even when intake failed
// before a run existed. It never imports a result or grants from another run.
func (s *Store) ChannelTicketContext(ctx context.Context, receiptID string) (*ChannelRequestContext, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := readChannelMessage(ctx, tx, receiptID)
	if err != nil {
		return nil, err
	}
	if current.TaskRootID == "" {
		return nil, nil
	}
	root, err := readChannelMessage(ctx, tx, current.TaskRootID)
	if err != nil {
		return nil, err
	}
	if !historyIdentityValid(current) || !historyIdentityValid(root) ||
		current.Channel != root.Channel || current.AccountID != root.AccountID || current.TenantID != root.TenantID ||
		current.ConversationID != root.ConversationID || current.SenderID != root.SenderID ||
		!samePartition(current.AgentID, current.AudienceID, root.AgentID, root.AudienceID) ||
		conversationType(current.ConversationType) != conversationType(root.ConversationType) ||
		root.Input == nil || root.Input.Quote == nil || root.Input.Quote.MessageType != "interactiveCard" {
		return nil, errors.New("ticket reference does not match its authenticated source")
	}
	input, truncated := recoveryText(root.Text, 4000)
	quote := contextQuote(root.Input)
	ids := ticketInstanceIDs(root.Input.Quote.Body)
	return &ChannelRequestContext{MessageID: root.MessageID, SenderID: root.SenderID, Input: input, InputTruncated: truncated, Quote: quote, InstanceIDs: ids}, nil
}

var ticketInstanceParameter = regexp.MustCompile(`(?:[?&]|\b)(?:procInstId|processInstanceId)=([A-Za-z0-9_-]{1,256})`)

func ticketInstanceIDs(text string) []string {
	var ids []string
	for depth := 0; depth < 4; depth++ {
		for _, match := range ticketInstanceParameter.FindAllStringSubmatch(text, -1) {
			if !slices.Contains(ids, match[1]) {
				ids = append(ids, match[1])
			}
		}
		decoded, err := url.PathUnescape(text)
		if err != nil || decoded == text {
			break
		}
		text = decoded
	}
	return ids
}
