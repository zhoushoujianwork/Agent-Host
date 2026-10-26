package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// SendText sends exactly the caller's text to one meeting, without adding a
// product-specific prefix. Persist the stable UUID and intent before calling.
// The returned acceptance is a platform acknowledgement, not a read receipt.
func (c *MeetingClient) SendText(ctx context.Context, meetingID, uuid, text string) (MutationResult, error) {
	if !meetingPattern.MatchString(meetingID) || !boundedIdentifier(uuid, 128, false) || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > 48*1024 {
		return MutationResult{Outcome: Failed}, errors.New("Feishu text destination, UUID or content is invalid")
	}
	args := append(c.arguments("+meeting-message-send", meetingID), "--msg-type", "text", "--text", text, "--uuid", uuid, "--format", "json")
	e, outcome, err := c.mutate(ctx, args)
	result := MutationResult{Outcome: outcome, MeetingID: meetingID}
	if err != nil {
		return result, err
	}
	var ack map[string]json.RawMessage
	_ = json.Unmarshal(e.Data, &ack)
	if raw, ok := ack["uuid"]; ok {
		var received string
		if json.Unmarshal(raw, &received) != nil || received != uuid {
			result.Outcome = Unknown
			return result, errors.New("Feishu reply acknowledgement UUID does not match")
		}
	}
	return result, nil
}
