package feishu

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agent-host/internal/channel"
	"agent-host/internal/state"
)

const maxTextBytes = 16 * 1024

type Parser struct{ Config Config }

// Parse returns an empty message for a well-framed unsupported or unauthorized
// source. It performs no API calls and never derives identity from message text.
func (p Parser) Parse(raw []byte) (channel.Message, error) {
	if err := p.Config.Validate(); err != nil {
		return channel.Message{}, err
	}
	frame, err := object(raw)
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM event envelope is invalid")
	}
	schema, err := stringField(frame, "schema")
	if err != nil || schema != "2.0" {
		return channel.Message{}, errors.New("Feishu IM requires a full schema 2.0 envelope")
	}
	header, err := object(frame["header"])
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM event header is invalid")
	}
	app, appErr := stringField(header, "app_id")
	tenant, tenantErr := stringField(header, "tenant_key")
	eventID, idErr := stringField(header, "event_id")
	kind, typeErr := stringField(header, "event_type")
	if appErr != nil || tenantErr != nil || idErr != nil || typeErr != nil || app != p.Config.AppID || tenant != p.Config.TenantID || !identifier(eventID, 256) {
		return channel.Message{}, errors.New("Feishu IM event identity does not match its configured application and tenant")
	}
	at, err := timestamp(header["create_time"])
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM header timestamp is invalid")
	}
	if kind != "im.message.receive_v1" {
		return channel.Message{}, nil
	}
	event, err := object(frame["event"])
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM event body is invalid")
	}
	sender, err := object(event["sender"])
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM sender is invalid")
	}
	senderType, err := stringField(sender, "sender_type")
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM sender type is invalid")
	}
	if senderType != "user" {
		return channel.Message{}, nil
	}
	identity, err := object(sender["sender_id"])
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM sender identity is invalid")
	}
	from, err := stringField(identity, "open_id")
	if err != nil || !openID.MatchString(from) {
		return channel.Message{}, errors.New("Feishu IM sender Open ID is invalid")
	}
	if from == p.Config.BotOpenID {
		return channel.Message{}, nil
	}
	senderTenant, err := stringField(sender, "tenant_key")
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM sender tenant is invalid")
	}
	message, err := object(event["message"])
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM message is invalid")
	}
	chatType, err := stringField(message, "chat_type")
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM chat type is invalid")
	}
	switch chatType {
	case "p2p":
		if from != p.Config.OwnerOpenID || senderTenant != "" && senderTenant != p.Config.TenantID {
			return channel.Message{}, nil
		}
	case "group":
		if senderTenant != p.Config.TenantID {
			return channel.Message{}, nil
		}
	default:
		return channel.Message{}, nil
	}
	messageType, err := stringField(message, "message_type")
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM message type is invalid")
	}
	if messageType != "text" {
		return channel.Message{}, nil
	}
	id, idErr := stringField(message, "message_id")
	chat, chatErr := stringField(message, "chat_id")
	if idErr != nil || chatErr != nil || !messageID.MatchString(id) || !chatID.MatchString(chat) {
		return channel.Message{}, errors.New("Feishu IM message or chat identifier is invalid")
	}
	// Only the immediate parent binds an approval reply. root_id can refer to
	// an earlier ancestor and must never authorize a different action.
	replyTo, replyErr := stringField(message, "parent_id")
	if replyErr != nil || replyTo != "" && (!messageID.MatchString(replyTo) || replyTo == id) {
		return channel.Message{}, errors.New("Feishu IM reply parent is invalid")
	}
	if created, present := message["create_time"]; present {
		at, err = timestamp(created)
		if err != nil {
			return channel.Message{}, errors.New("Feishu IM message timestamp is invalid")
		}
	}
	content, err := stringField(message, "content")
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM text content is invalid")
	}
	body, err := object([]byte(content))
	if err != nil {
		return channel.Message{}, errors.New("Feishu IM text body is invalid")
	}
	text, err := stringField(body, "text")
	if err != nil || len(body["text"]) == 0 || len(text) > maxTextBytes || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return channel.Message{}, errors.New("Feishu IM text is invalid or exceeds its limit")
	}
	conversationType := "1"
	if chatType == "group" {
		keys, err := p.mentionKeys(message["mentions"])
		if err != nil {
			return channel.Message{}, err
		}
		if len(keys) == 0 && !(from == p.Config.OwnerOpenID && replyTo != "" && approvalDecisionText(text)) {
			return channel.Message{}, nil
		}
		text = stripLeadingMentions(text, keys)
		conversationType = "2"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return channel.Message{}, nil
	}
	return channel.Message{ID: id, ReplyToID: replyTo, ConversationID: chat, ConversationType: conversationType,
		SenderID: from, TenantID: tenant, MessageType: "text", Text: text, CreatedAt: at,
		ReplyTarget: state.ChannelReplyTarget{Kind: state.ReplyTargetFeishu, ID: chat, MessageID: id}}, nil
}

var mentionKey = regexp.MustCompile(`^@_user_[0-9]+`)

func (p Parser) mentionKeys(raw json.RawMessage) (map[string]bool, error) {
	keys := map[string]bool{}
	if len(raw) == 0 || string(raw) == "null" {
		return keys, nil
	}
	var mentions []map[string]json.RawMessage
	if json.Unmarshal(raw, &mentions) != nil || len(mentions) > 100 {
		return nil, errors.New("Feishu IM mention metadata is invalid")
	}
	for _, mention := range mentions {
		identity, err := object(mention["id"])
		if err != nil {
			continue
		}
		id, err := stringField(identity, "open_id")
		if err != nil || id != p.Config.BotOpenID {
			continue
		}
		key, err := stringField(mention, "key")
		if err != nil || len(key) > 32 || mentionKey.FindString(key) != key {
			return nil, errors.New("Feishu IM bot mention key is invalid")
		}
		keys[key] = true
	}
	return keys, nil
}

func stripLeadingMentions(text string, keys map[string]bool) string {
	text = strings.TrimSpace(text)
	for {
		key := mentionKey.FindString(text)
		if key == "" || !keys[key] {
			return text
		}
		text = strings.TrimSpace(text[len(key):])
	}
}

func timestamp(raw json.RawMessage) (time.Time, error) {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return time.Time{}, errors.New("invalid Feishu timestamp")
		}
	}
	if value == "" || len(value) > 15 || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return time.Time{}, errors.New("invalid Feishu timestamp")
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n > 253402300799999 {
		return time.Time{}, errors.New("invalid Feishu timestamp")
	}
	return time.UnixMilli(n).UTC(), nil
}

// Approval replies without a bot mention are control messages only. Keep the
// vocabulary exact so a quoted ordinary conversation cannot enter task intake.
func approvalDecisionText(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "ok", "approve", "no", "reject":
		return true
	default:
		return false
	}
}
