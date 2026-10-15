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
// API text adapters grant no media capabilities.
type Message struct {
	ReplyToID        string
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

// ReceiveMessage persists an authorized text request before its transport
// advances. It never starts execution and never derives a destination
// or authorization from message content.
func (s *Service) ReceiveMessage(ctx context.Context, m Message) error {
	if err := s.Validate(); err != nil {
		return err
	}
	actualChannel := m.ReplyTarget.Kind
	if !s.acceptsTextChannel(actualChannel) || m.ReplyTarget.ID != m.ConversationID || m.MessageType != "text" {
		return errors.New("text input or reply target does not match its configured channel")
	}
	if actualChannel == state.ReplyTargetFeishuMeeting {
		if m.ConversationType != "2" || m.ReplyTarget.MessageID != "" {
			return errors.New("meeting input requires a shared conversation and meeting reply target")
		}
	} else if (m.ConversationType != "1" && m.ConversationType != "2") || !strings.HasPrefix(m.ID, "om_") || len(m.ID) <= len("om_") || m.ReplyTarget.MessageID != m.ID {
		return errors.New("Feishu IM input requires its original message reply target")
	}
	selection := AgentSelection{AgentID: "default", AudienceID: Key("channel-audience-v1", actualChannel, s.Profile, s.ClientID, m.TenantID, m.ConversationID)}
	if actualChannel == state.ReplyTargetFeishu {
		selection.AudienceID = Key("channel-audience-v1", actualChannel, s.Profile, s.ClientID, m.TenantID, m.ConversationID, m.ConversationType)
	}
	if s.SelectMessageAgent != nil {
		var err error
		selection, err = s.SelectMessageAgent(ctx, m)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(selection.AgentID) == "" || strings.TrimSpace(selection.AudienceID) == "" || selection.AudienceID == state.OwnerAudienceID && (actualChannel != state.ReplyTargetFeishu || m.ConversationType != "1") {
		return errors.New("text input requires an explicit audience; only private IM may use owner")
	}
	// The stable source identity deliberately excludes runtime profile/agent:
	// changing configuration must not turn a redelivery into another send.
	target := m.ReplyTarget
	target.UUID = Key("channel-reply-v1", actualChannel, s.ClientID, m.TenantID, m.ConversationID, m.ID)
	if actualChannel == state.ReplyTargetFeishu {
		// The IM API accepts at most 50 characters in its idempotency key.
		target.UUID = target.UUID[:48]
	}
	binding := Key("channel-conversation-v1", actualChannel, s.Profile, s.ClientID, m.TenantID, m.ConversationID, selection.AgentID, selection.AudienceID)
	if actualChannel == state.ReplyTargetFeishu {
		binding = Key("channel-conversation-v1", actualChannel, s.Profile, s.ClientID, m.TenantID, m.ConversationID, m.ConversationType, selection.AgentID, selection.AudienceID)
	}
	message := state.ChannelMessage{
		ReplyToID: m.ReplyToID,
		ID:        Key(actualChannel, s.ClientID, m.TenantID, m.ConversationID, m.ID),
		Channel:   actualChannel, AccountID: s.ClientID, TenantID: m.TenantID,
		MessageID: m.ID, ConversationID: m.ConversationID, SenderID: m.SenderID,
		Profile: s.Profile, ConversationType: m.ConversationType,
		AgentID: selection.AgentID, AudienceID: selection.AudienceID,
		Binding: binding,
		Scope:   s.Scope, MessageType: m.MessageType, Text: m.Text,
		SentAt: m.CreatedAt, ReceivedAt: time.Now().UTC(), ReplyTarget: &target,
		DisableReactions: actualChannel == state.ReplyTargetFeishuMeeting || s.DisableReactions,
	}
	return s.enqueue(ctx, message, "", nil)
}

func (s *Service) acceptsTextChannel(channel string) bool {
	return s.Channel == state.ReplyTargetFeishu && (channel == state.ReplyTargetFeishu || channel == state.ReplyTargetFeishuMeeting) ||
		s.Channel == state.ReplyTargetFeishuMeeting && channel == state.ReplyTargetFeishuMeeting
}

func addressedText(m state.ChannelMessage) string {
	if m.Channel == "dingtalk" {
		return dingtalk.AddressedText(m.ConversationType, m.Text)
	}
	return m.Text
}

// Meeting replies have no DingTalk mention syntax or platform-specific quote.
func meetingReplyText(text string) string {
	return apiReplyText(text, 4000)
}

func apiReplyText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	const notice = "\n\n正文过长，以上为节选。"
	return fitMarkdownBody(text, maxBytes-len(notice)) + notice
}
