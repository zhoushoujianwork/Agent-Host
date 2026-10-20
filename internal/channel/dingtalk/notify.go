package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// NotifyUser uses the application's bot identity, not an owner's personal DWS
// session. The caller has already fixed a verified enterprise user ID durably.
func (r *Reactor) NotifyUser(ctx context.Context, userID, deliveryID, text string) (string, string, error) {
	if r == nil || ValidateConfig(r.Config) != nil || r.RobotCode == "" || userID == "" || ValidateAtUserID(userID) != nil || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > MaxReplyBytes {
		return Failed, "", errors.New("DingTalk notification requires a configured bot, verified user and bounded text")
	}
	token, err := r.accessToken(ctx)
	if err != nil {
		return Failed, "", err
	}
	content, _ := json.Marshal(map[string]string{"title": "Agent-Host 任务结果", "text": text})
	body, _ := json.Marshal(map[string]any{"robotCode": r.RobotCode, "userIds": []string{userID}, "msgKey": "sampleMarkdown", "msgParam": string(content)})
	raw, _, err := r.post(ctx, "/v1.0/robot/oToMessages/batchSend", token, body)
	if err != nil {
		return ReactionOutcome(err), "", err
	}
	var result struct {
		ProcessQueryKey           string   `json:"processQueryKey"`
		InvalidStaffIDList        []string `json:"invalidStaffIdList"`
		FlowControlledStaffIDList []string `json:"flowControlledStaffIdList"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return Unknown, "", errors.New("DingTalk notification response is unconfirmed")
	}
	for _, id := range append(result.InvalidStaffIDList, result.FlowControlledStaffIDList...) {
		if id == userID {
			return Failed, "", errors.New("DingTalk rejected or flow-controlled the notification recipient")
		}
	}
	if result.ProcessQueryKey == "" {
		return Unknown, "", errors.New("DingTalk notification has no platform acceptance receipt")
	}
	return Accepted, result.ProcessQueryKey, nil
}
