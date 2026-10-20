package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"agent-host/internal/channel/feishumeeting"
)

func (r Replier) NotifyUser(ctx context.Context, userID, deliveryID, text string) (string, string, error) {
	if r.Config.Validate() != nil || !openID.MatchString(userID) || !uuidID.MatchString(deliveryID) || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') || len(text)+len("[AI] ") > maxReplyBytes {
		return Failed, "", errors.New("Feishu notification requires a configured bot, verified user, durable ID and bounded text")
	}
	if err := ctx.Err(); err != nil {
		return Failed, "", err
	}
	content, _ := json.Marshal(map[string]any{"zh_cn": map[string]any{"content": [][]map[string]string{{{"tag": "text", "text": "[AI]"}}, {{"tag": "md", "text": text}}}}})
	client := r.Client
	if client == nil {
		client = feishumeeting.CLIClient{}
	}
	out, runErr := client.Run(ctx, "--profile", r.Config.Profile, "im", "+messages-send", "--as", "bot", "--user-id", userID, "--msg-type", "post", "--content", string(content), "--idempotency-key", deliveryID, "--format", "json")
	if !out.Started {
		return Failed, "", errors.New("Feishu notification command did not start")
	}
	e, stdout, err := replyEnvelope(out)
	if err != nil || !stdout {
		return Unknown, "", errors.New("Feishu notification delivery is unconfirmed")
	}
	identity, _ := stringField(e, "identity")
	if string(e["ok"]) == "false" {
		failure, e := object(e["error"])
		if e == nil {
			kind, _ := stringField(failure, "type")
			if kind == "validation" || kind == "auth" || kind == "authorization" || kind == "permission" {
				return Failed, "", errors.New("Feishu rejected notification validation or authorization")
			}
		}
	}
	if string(e["ok"]) != "true" || identity != "bot" || runErr != nil {
		return Unknown, "", errors.New("Feishu notification delivery is unconfirmed")
	}
	data, err := object(e["data"])
	if err != nil {
		return Unknown, "", errors.New("Feishu notification acknowledgement is invalid")
	}
	id, err := stringField(data, "message_id")
	if err != nil || !messageID.MatchString(id) {
		return Unknown, "", errors.New("Feishu notification lacks a platform message receipt")
	}
	return Accepted, id, nil
}
