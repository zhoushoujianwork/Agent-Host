package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Accepted         = "accepted"
	Failed           = "failed"
	Unknown          = "unknown"
	maxResponseBytes = 64 << 10
	replyTimeout     = 15 * time.Second
)

type Replier struct {
	HTTPClient *http.Client
	Now        func() time.Time
}

// ReplyMessage keeps the authenticated reply recipient separate from content.
// Text is already formatted, including any visible mention, before delivery.
type ReplyMessage struct {
	Text     string
	Title    string
	AtUserID string
}

// ValidateAtUserID accepts an optional single staff identifier that can also be
// rendered in Markdown without introducing mention or formatting syntax. The
// caller must obtain this identifier from the authenticated callback, not text.
func ValidateAtUserID(id string) error {
	if len(id) > maxIDBytes {
		return errors.New("DingTalk reply mention target is invalid")
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return errors.New("DingTalk reply mention target is invalid")
	}
	return nil
}

func (r Replier) Reply(ctx context.Context, target string, expiresAt time.Time, message ReplyMessage) (string, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if !validReplyURL(target) {
		return Failed, errors.New("DingTalk reply capability is invalid")
	}
	if !expiresAt.After(now()) {
		return Failed, errors.New("DingTalk reply capability has expired")
	}
	if strings.TrimSpace(message.Text) == "" || len(message.Text) > MaxReplyBytes || !utf8.ValidString(message.Text) {
		return Failed, errors.New("DingTalk reply text is empty, invalid, or exceeds its byte limit")
	}
	if err := ValidateAtUserID(message.AtUserID); err != nil {
		return Failed, err
	}
	title := message.Title
	if title == "" {
		title = "Agent-Host 回复"
	}
	payload := map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": title, "text": message.Text}}
	if message.AtUserID != "" {
		payload["at"] = map[string]any{"atUserIds": []string{message.AtUserID}, "isAtAll": false}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Failed, &operationError{operation: "reply encoding", cause: err}
	}
	requestCtx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Failed, &operationError{operation: "reply request construction", cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: replyTimeout}
	if r.HTTPClient != nil {
		client = *r.HTTPClient
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return Unknown, &operationError{operation: "reply request; delivery outcome is unknown", cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 300 && response.StatusCode < 500 {
			return Failed, errors.New("DingTalk rejected the reply HTTP request")
		}
		return Unknown, errors.New("DingTalk reply HTTP response did not confirm delivery")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return Unknown, errors.New("DingTalk reply response could not be read within its limit")
	}
	var result struct {
		ErrorCode *int `json:"errcode"`
	}
	if json.Unmarshal(raw, &result) != nil || result.ErrorCode == nil {
		return Unknown, errors.New("DingTalk reply response did not contain a valid delivery result")
	}
	if *result.ErrorCode != 0 {
		return Failed, errors.New("DingTalk rejected the reply")
	}
	return Accepted, nil
}
