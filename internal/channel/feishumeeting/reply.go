package feishumeeting

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"agent-host/internal/state"
)

const (
	Accepted = "accepted"
	Failed   = "failed"
	Unknown  = "unknown"
)

type Replier struct {
	Config Config
	Client Client
}

// ReplyText makes one attempt. The Host persists UUID and the sending state
// before calling; unknown outcomes must be reconciled, never automatically retried.
func (r Replier) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	if err := r.Config.ValidateMeeting(); err != nil {
		return Failed, err
	}
	if target.Kind != "feishu-meeting" || target.ID != r.Config.MeetingID || len(target.UUID) == 0 || len(target.UUID) > 128 || strings.ContainsAny(target.UUID, "\x00\r\n") {
		return Failed, errors.New("Feishu reply target or durable UUID is invalid")
	}
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || len(text) > 48*1024-len("[AI] ") {
		return Failed, errors.New("Feishu reply text is empty, invalid, or too long")
	}
	if err := ctx.Err(); err != nil {
		return Failed, err
	}
	args := append(r.Config.arguments("+meeting-message-send"), "--msg-type", "text", "--text", "[AI] "+text, "--uuid", target.UUID, "--format", "json")
	out, runErr := selectedClient(r.Client).Run(ctx, args...)
	if runErr != nil && !out.Started {
		return Failed, errors.New("Feishu reply command did not start")
	}
	e, err := decodeEnvelope(out)
	if err != nil {
		return Unknown, errors.New("Feishu reply delivery is unconfirmed; do not resend automatically")
	}
	if !*e.OK {
		// Only explicit local/auth rejection establishes that the write failed.
		// Network and unclassified server errors retain an unknown outcome.
		switch e.Error.Type {
		case "validation", "auth", "authorization", "permission":
			return Failed, errors.New("Feishu rejected reply validation or authorization")
		}
		return Unknown, errors.New("Feishu did not confirm reply delivery; do not resend automatically")
	}
	if runErr != nil {
		return Unknown, errors.New("Feishu reply delivery is unconfirmed; do not resend automatically")
	}
	var ack map[string]json.RawMessage
	if json.Unmarshal(e.Data, &ack) != nil || ack == nil {
		return Unknown, errors.New("Feishu reply acknowledgement is invalid")
	}
	if raw, ok := ack["uuid"]; ok {
		var uuid string
		if json.Unmarshal(raw, &uuid) != nil || uuid != target.UUID {
			return Unknown, errors.New("Feishu reply acknowledgement UUID does not match")
		}
	}
	return Accepted, nil
}
