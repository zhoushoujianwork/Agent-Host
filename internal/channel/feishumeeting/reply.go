package feishumeeting

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"agent-host/internal/state"
	feishulib "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"
)

const (
	Accepted = feishulib.Accepted
	Failed   = feishulib.Failed
	Unknown  = feishulib.Unknown
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
	client, err := meetingClient(r.Config, r.Client)
	if err != nil {
		return Failed, err
	}
	result, err := client.SendText(ctx, target.ID, target.UUID, "[AI] "+text)
	return result.Outcome, err
}
