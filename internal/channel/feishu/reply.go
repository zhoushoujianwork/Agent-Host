package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
)

const (
	Accepted      = "accepted"
	Failed        = "failed"
	Unknown       = "unknown"
	maxReplyBytes = 4000
)

type Replier struct {
	Config Config
	Client feishumeeting.Client
}

// ReplyText makes one bot mutation to the original durable IM target. The Host
// persists sending before this call; ambiguous outcomes must not be replayed.
func (r Replier) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	status, _, err := r.replyPost(ctx, target, text, [][]map[string]string{
		{{"tag": "text", "text": "[AI]"}},
		{{"tag": "md", "text": strings.TrimSpace(text)}},
	})
	return status, err
}

// SendApproval sends one owner mention with a platform-confirmed message ID.
func (r Replier) SendApproval(ctx context.Context, target state.ChannelReplyTarget, ownerID, text string) (string, error) {
	if ownerID != r.Config.OwnerOpenID || !openID.MatchString(ownerID) {
		return "", errors.New("Feishu approval recipient is not the configured owner")
	}
	_, id, err := r.replyPost(ctx, target, text, [][]map[string]string{
		{{"tag": "text", "text": "[AI]"}, {"tag": "at", "user_id": ownerID}},
		{{"tag": "text", "text": strings.TrimSpace(text)}},
	})
	return id, err
}

func (r Replier) replyPost(ctx context.Context, target state.ChannelReplyTarget, text string, rows [][]map[string]string) (string, string, error) {
	if err := r.Config.Validate(); err != nil {
		return Failed, "", err
	}
	if target.Kind != state.ReplyTargetFeishu || !chatID.MatchString(target.ID) || !messageID.MatchString(target.MessageID) || !uuidID.MatchString(target.UUID) {
		return Failed, "", errors.New("Feishu IM reply target or durable UUID is invalid")
	}
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') || len(text)+len("[AI] ") > maxReplyBytes {
		return Failed, "", errors.New("Feishu IM reply text is empty, invalid or exceeds 4000 bytes")
	}
	if err := ctx.Err(); err != nil {
		return Failed, "", err
	}
	// A raw md node renders text without the CLI Markdown shortcut's media
	// resolution. Model output remains one text value, never post structure.
	content, err := json.Marshal(map[string]any{"zh_cn": map[string]any{"content": rows}})
	if err != nil {
		return Failed, "", errors.New("Feishu IM rich text could not be encoded")
	}
	client := r.Client
	if client == nil {
		client = feishumeeting.CLIClient{}
	}
	args := []string{"--profile", r.Config.Profile, "im", "+messages-reply", "--as", "bot", "--message-id", target.MessageID,
		"--msg-type", "post", "--content", string(content), "--idempotency-key", target.UUID, "--format", "json"}
	out, runErr := client.Run(ctx, args...)
	if !out.Started {
		return Failed, "", errors.New("Feishu IM reply command did not start")
	}
	e, fromStdout, err := replyEnvelope(out)
	if err != nil {
		return Unknown, "", errors.New("Feishu IM reply delivery is unconfirmed; do not resend automatically")
	}
	ok, present := e["ok"]
	if !present || string(ok) != "true" && string(ok) != "false" {
		return Unknown, "", errors.New("Feishu IM reply result is invalid")
	}
	accepted := string(ok) == "true"
	identity, err := stringField(e, "identity")
	if err != nil || identity != "" && identity != "bot" {
		return Unknown, "", errors.New("Feishu IM reply identity is unconfirmed")
	}
	if !accepted {
		failure, err := object(e["error"])
		if err == nil {
			kind, err := stringField(failure, "type")
			if err == nil {
				switch kind {
				case "validation", "auth", "authorization", "permission":
					return Failed, "", errors.New("Feishu rejected IM reply validation or authorization")
				}
			}
		}
		return Unknown, "", errors.New("Feishu did not confirm IM reply delivery; do not resend automatically")
	}
	if runErr != nil || identity != "bot" || !fromStdout {
		return Unknown, "", errors.New("Feishu IM reply delivery is unconfirmed; do not resend automatically")
	}
	data, err := object(e["data"])
	if err != nil {
		return Unknown, "", errors.New("Feishu IM reply acknowledgement is invalid")
	}
	id, idErr := stringField(data, "message_id")
	chat, chatErr := stringField(data, "chat_id")
	if idErr != nil || chatErr != nil || !messageID.MatchString(id) || id == target.MessageID || chat != target.ID {
		return Unknown, "", errors.New("Feishu IM reply acknowledgement does not match its destination")
	}
	return Accepted, id, nil
}

func replyEnvelope(out feishumeeting.Output) (map[string]json.RawMessage, bool, error) {
	var found map[string]json.RawMessage
	fromStdout := false
	for index, raw := range [][]byte{out.Stdout, out.Stderr} {
		value, err := object(raw)
		if err == nil && len(value["ok"]) != 0 {
			if found != nil {
				return nil, false, errors.New("multiple Feishu IM reply result envelopes")
			}
			found, fromStdout = value, index == 0
		}
	}
	if found != nil {
		return found, fromStdout, nil
	}
	return nil, false, errors.New("invalid Feishu IM reply envelope")
}
