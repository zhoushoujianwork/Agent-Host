package channel

import (
	"context"
	"errors"
	"strings"
	"time"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

// Message is authenticated, explicitly addressed text from a channel adapter.
// The first non-webhook adapter is meeting text; it grants no media capabilities.
type Message struct {
	ID               string
	ConversationID   string
	ConversationType string
	SenderID         string
	TenantID         string
	MessageType      string
	Text             string
	CreatedAt        time.Time
	ReplyTarget      state.ChannelReplyTarget
}

type TextReplier interface {
	ReplyText(context.Context, state.ChannelReplyTarget, string) (string, error)
}

// ReceiveMessage persists an authorized meeting request before its transport
// advances a cursor. It never starts execution and never derives a destination
// or authorization from message content.
func (s *Service) ReceiveMessage(ctx context.Context, m Message) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Channel != state.ReplyTargetFeishuMeeting || m.ReplyTarget.Kind != s.Channel ||
		m.ReplyTarget.ID != m.ConversationID || m.MessageType != "text" || m.ConversationType != "2" {
		return errors.New("meeting input or reply target does not match its configured channel")
	}
	selection := AgentSelection{AgentID: "default", AudienceID: Key("channel-audience-v1", s.Channel, s.Profile, s.ClientID, m.TenantID, m.ConversationID)}
	if s.SelectMessageAgent != nil {
		var err error
		selection, err = s.SelectMessageAgent(ctx, m)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(selection.AgentID) == "" || strings.TrimSpace(selection.AudienceID) == "" || selection.AudienceID == state.OwnerAudienceID {
		return errors.New("meeting input requires an explicit non-owner agent audience")
	}
	// The stable source identity deliberately excludes runtime profile/agent:
	// changing configuration must not turn a redelivery into another send.
	target := m.ReplyTarget
	target.UUID = Key("channel-reply-v1", s.Channel, s.ClientID, m.TenantID, m.ConversationID, m.ID)
	message := state.ChannelMessage{
		ID:      Key(s.Channel, s.ClientID, m.TenantID, m.ConversationID, m.ID),
		Channel: s.Channel, AccountID: s.ClientID, TenantID: m.TenantID,
		MessageID: m.ID, ConversationID: m.ConversationID, SenderID: m.SenderID,
		Profile: s.Profile, ConversationType: m.ConversationType,
		AgentID: selection.AgentID, AudienceID: selection.AudienceID,
		Binding: Key("channel-conversation-v1", s.Channel, s.Profile, s.ClientID, m.TenantID, m.ConversationID, selection.AgentID, selection.AudienceID),
		Scope:   s.Scope, MessageType: m.MessageType, Text: m.Text,
		SentAt: m.CreatedAt, ReceivedAt: time.Now().UTC(), ReplyTarget: &target,
		DisableReactions: true,
	}
	return s.enqueue(ctx, message, "", nil)
}

func addressedText(m state.ChannelMessage) string {
	if m.Channel == "dingtalk" {
		return dingtalk.AddressedText(m.ConversationType, m.Text)
	}
	return m.Text
}

// Meeting replies have no DingTalk mention syntax or platform-specific quote.
func meetingReplyText(text string) string {
	const maxBytes = 4000
	if len(text) <= maxBytes {
		return text
	}
	const notice = "\n\n正文过长，以上为节选。"
	return fitMarkdownBody(text, maxBytes-len(notice)) + notice
}
