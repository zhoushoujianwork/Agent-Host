// Package dingtalk connects private and explicitly addressed group conversations
// to the host. Protocol credentials never belong in model context or evidence.
package dingtalk

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	maxFrameBytes   = 1 << 20
	maxTextBytes    = 16 << 10
	maxIDBytes      = 512
	maxWebhookBytes = 8192
	// MaxReplyBytes is the conservative limit for one host-generated Markdown reply.
	// It is a host limit, not a claim about DingTalk's platform quota.
	MaxReplyBytes = 4000
)

type Config struct {
	ClientID          string
	RobotCode         string
	ClientSecret      string `json:"-"`
	CorpID            string
	OwnerUserID       string
	AllowPrivateUsers bool
	WorkspaceConfig   string `json:"-"`
}

func ValidateConfig(cfg Config) error {
	for _, value := range []string{cfg.ClientID, cfg.ClientSecret, cfg.CorpID, cfg.OwnerUserID} {
		if strings.TrimSpace(value) == "" {
			return errors.New("DingTalk requires client ID, client secret, enterprise ID, and owner user ID")
		}
	}
	if len(cfg.ClientID) > maxIDBytes || len(cfg.CorpID) > maxIDBytes || len(cfg.OwnerUserID) > maxIDBytes || len(cfg.ClientSecret) > maxWebhookBytes {
		return errors.New("DingTalk configuration exceeds its field limits")
	}
	return nil
}

type Message struct {
	ReplyToID        string
	ID               string
	ConversationID   string
	ConversationType string
	SenderID         string
	TenantID         string
	MessageType      string
	Text             string
	Attachments      []Attachment
	Quote            *MessageQuote
	// ReplyURL is a short-lived credential. Persist only in the protected
	// capability column; exclude it from public payloads, logs, and model input.
	ReplyURL       string `json:"-"`
	CreatedAt      time.Time
	ReplyExpiresAt time.Time
}

// AddressedText provides a control/routing view of an already authenticated
// group message. Some clients retain a leading @name token in text. Consume
// at most that single whitespace-delimited token; never search ahead for a
// command or guess where a multiword display name ends. This is syntax only,
// not mention authorization. Callers retain the original text as evidence.
func AddressedText(conversationType, text string) string {
	if conversationType != "2" {
		return text
	}
	leading := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(leading, "@") {
		return text
	}
	end := strings.IndexFunc(leading, unicode.IsSpace)
	if end <= 1 {
		return text
	}
	rest := strings.TrimLeftFunc(leading[end:], unicode.IsSpace)
	if rest == "" {
		return text
	}
	return rest
}

// IsSlashCommand recognizes a single slash-prefixed command name, optionally
// followed by arguments. Absolute paths containing another slash or an
// extension, and the filesystem root itself, remain ordinary task content.
func IsSlashCommand(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 || len(parts[0]) < 2 || parts[0][0] != '/' {
		return false
	}
	for _, r := range parts[0][1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

var ErrRejected = errors.New("DingTalk callback rejected")

type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "DingTalk callback rejected: " + e.Reason }
func (e *RejectError) Unwrap() error { return ErrRejected }

func reject(reason string) error { return &RejectError{Reason: reason} }

// Parse accepts the owner's private messages and same-enterprise group mentions.
// Mention authorization comes from the authenticated callback, never text.
// Unsupported message types retain their identity so the host can record and
// explicitly reject them. They never become a substituted text prompt.
func Parse(data []byte, cfg Config, now time.Time) (Message, error) {
	var message Message
	if err := ValidateConfig(cfg); err != nil {
		return message, err
	}
	if len(data) > maxFrameBytes {
		return message, reject("payload exceeds the byte limit")
	}
	var callback struct {
		ID               string `json:"msgId"`
		ConversationID   string `json:"conversationId"`
		ConversationType string `json:"conversationType"`
		IsInAtList       bool   `json:"isInAtList"`
		AtUsers          []struct {
			DingtalkID string `json:"dingtalkId"`
			StaffID    string `json:"staffId"`
		} `json:"atUsers"`
		SenderID       string          `json:"senderId"`
		SenderStaffID  string          `json:"senderStaffId"`
		SenderCorpID   string          `json:"senderCorpId"`
		ChatbotCorpID  string          `json:"chatbotCorpId"`
		ChatbotUserID  string          `json:"chatbotUserId"`
		MessageType    string          `json:"msgtype"`
		CreatedAt      int64           `json:"createAt"`
		ReplyURL       string          `json:"sessionWebhook"`
		ReplyExpiresAt int64           `json:"sessionWebhookExpiredTime"`
		OriginalMsgID  string          `json:"originalMsgId"`
		Content        json.RawMessage `json:"content"`
		Text           struct {
			Content    string          `json:"content"`
			IsReplyMsg bool            `json:"isReplyMsg"`
			RepliedMsg *quotedCallback `json:"repliedMsg"`
		} `json:"text"`
	}
	if json.Unmarshal(data, &callback) != nil {
		return message, reject("payload is not valid callback JSON")
	}
	if callback.ConversationType != "1" && callback.ConversationType != "2" {
		return message, reject("unsupported conversation type")
	}
	if callback.ConversationType == "2" && !callback.IsInAtList {
		return message, reject("group message does not mention the robot")
	}
	if callback.ConversationType == "2" && len(callback.AtUsers) > 0 {
		matched := false
		for _, user := range callback.AtUsers {
			if user.DingtalkID == callback.ChatbotUserID || user.StaffID == callback.ChatbotUserID {
				matched = true
			}
		}
		if !matched {
			return message, reject("group mention identity does not match the robot")
		}
	}
	if callback.ChatbotCorpID != cfg.CorpID || callback.SenderCorpID != cfg.CorpID {
		return message, reject("enterprise identity does not match")
	}
	if callback.ConversationType == "1" && callback.SenderStaffID != cfg.OwnerUserID && !cfg.AllowPrivateUsers {
		return message, reject("sender is not the configured owner")
	}
	if callback.SenderID == "" || callback.SenderID == callback.ChatbotUserID {
		return message, reject("sender is missing or is the robot")
	}
	for _, value := range []string{callback.ID, callback.ConversationID, callback.SenderID, callback.SenderStaffID, callback.ChatbotCorpID, callback.ChatbotUserID, callback.MessageType} {
		if strings.TrimSpace(value) == "" || len(value) > maxIDBytes {
			return message, reject("required message identity is missing or exceeds its limit")
		}
	}
	if callback.CreatedAt <= 0 {
		return message, reject("message creation time is missing or invalid")
	}
	if !validReplyURL(callback.ReplyURL) {
		return message, reject("reply capability is missing or invalid")
	}
	expires := time.UnixMilli(callback.ReplyExpiresAt)
	if callback.ReplyExpiresAt <= 0 || !expires.After(now) {
		return message, reject("reply capability has expired")
	}
	text, supported := "", false
	var attachments []Attachment
	var forwarded *MessageQuote
	switch callback.MessageType {
	case "text":
		text, supported = callback.Text.Content, true
	case "markdown":
		text, forwarded, supported = parseForwardedMarkdown(callback.Text.Content, callback.Content)
	case "richText":
		text, attachments, supported = parseRichText(callback.Content)
	case "picture", "file":
		text, attachments, supported = parseStandaloneMedia(callback.MessageType, callback.Content)
	}
	if supported && (strings.TrimSpace(text) == "" || len(text) > maxTextBytes) {
		return message, reject("text is empty or exceeds its byte limit")
	}
	message = Message{ID: callback.ID, ConversationID: callback.ConversationID, ConversationType: callback.ConversationType, SenderID: callback.SenderStaffID,
		TenantID: callback.ChatbotCorpID, MessageType: callback.MessageType, ReplyURL: callback.ReplyURL,
		CreatedAt: time.UnixMilli(callback.CreatedAt), ReplyExpiresAt: expires}
	if supported {
		message.Text, message.Attachments = text, attachments
		// originalMsgId is not a reliable parent identifier. Bind only the
		// explicit provider reply metadata, never quote body or author text.
		if callback.Text.IsReplyMsg && callback.Text.RepliedMsg != nil {
			id := callback.Text.RepliedMsg.MsgID
			if strings.TrimSpace(id) != "" && strings.TrimSpace(id) == id && len(id) <= maxIDBytes && !strings.ContainsAny(id, "\x00\r\n") && id != callback.ID {
				message.ReplyToID = id
			}
		}
		message.Quote = parseQuote(callback.OriginalMsgID, callback.Text.IsReplyMsg, callback.Text.RepliedMsg)
		if forwarded != nil {
			// The callback authenticates the sender who supplied the Markdown,
			// not its original author. Keep the body out of sender statements
			// and standalone Host-command parsing.
			message.Text, message.Quote = "Forwarded Markdown message", forwarded
		}
	}
	return message, nil
}

func validReplyURL(raw string) bool {
	if len(raw) == 0 || len(raw) > maxWebhookBytes || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "oapi.dingtalk.com" || u.User != nil || u.Fragment != "" || u.Path != "/robot/sendBySession" || u.RawPath != "" {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(query["session"]) == 1 && query.Get("session") != ""
}

// operationError preserves a cause for errors.Is without rendering potentially
// credential-bearing SDK or HTTP errors to terminals and logs.
type operationError struct {
	operation string
	cause     error
}

func (e *operationError) Error() string { return fmt.Sprintf("DingTalk %s failed", e.operation) }
func (e *operationError) Unwrap() error { return e.cause }
