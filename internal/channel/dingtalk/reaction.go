package dingtalk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	reactionAPIBase   = "https://api.dingtalk.com"
	reactionTokenPath = "/v1.0/oauth2/accessToken"
	reactionAddPath   = "/v1.0/robot/emotion/reply"
	reactionDelPath   = "/v1.0/robot/emotion/recall"
)

// Reactor sends stage markers on the original user message. Keep its public
// configuration immutable while it is in use and share one pointer so token
// refreshes are serialized. Reaction acceptance is independent of task success.
type Reactor struct {
	Config     Config
	RobotCode  string
	HTTPClient *http.Client
	Now        func() time.Time

	tokenMu          sync.Mutex
	tokenGateOnce    sync.Once
	tokenGate        chan struct{}
	token            string
	tokenCredentials [32]byte
	tokenExpiresAt   time.Time
}

type reactionError struct {
	outcome string
	message string
	cause   error
}

func (e *reactionError) Error() string { return e.message }
func (e *reactionError) Unwrap() error { return e.cause }

// ReactionOutcome separates a confirmed rejection from a mutation that may
// have reached the platform. Unrecognized errors are deliberately unknown.
func ReactionOutcome(err error) string {
	if err == nil {
		return Accepted
	}
	var outcome *reactionError
	if errors.As(err, &outcome) {
		return outcome.outcome
	}
	return Unknown
}

func reactionFailure(message string, cause error) error {
	return &reactionError{outcome: Failed, message: message, cause: cause}
}

func (r *Reactor) AddReaction(ctx context.Context, conversationID, messageID, label string) error {
	return r.reaction(ctx, reactionAddPath, conversationID, messageID, label)
}

func (r *Reactor) RemoveReaction(ctx context.Context, conversationID, messageID, label string) error {
	return r.reaction(ctx, reactionDelPath, conversationID, messageID, label)
}

func (r *Reactor) reaction(ctx context.Context, path, conversationID, messageID, label string) error {
	if r == nil {
		return reactionFailure("DingTalk reaction client is not configured", nil)
	}
	if err := ValidateConfig(r.Config); err != nil {
		return reactionFailure("DingTalk reaction configuration is invalid", err)
	}
	for _, value := range []string{r.RobotCode, conversationID, messageID} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len(value) > maxIDBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return reactionFailure("DingTalk reaction requires valid robot, conversation and original message identifiers", nil)
		}
	}
	switch label {
	case "已收到", "处理中", "检索记忆", "AI执行", "Jev判断", "保存记忆", "整理证据", "提取经验", "Jev评估", "保存候选", "发送回复", "已完成", "打叉":
	default:
		return reactionFailure("DingTalk reaction label must be a supported execution stage", nil)
	}
	requestCtx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	token, err := r.accessToken(requestCtx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"robotCode": r.RobotCode, "openConversationId": conversationID, "openMsgId": messageID,
		"emotionType": 2, "emotionName": label,
		"textEmotion": map[string]string{"emotionId": "2659900", "emotionName": label, "text": label, "backgroundId": "im_bg_1"},
	})
	if err != nil {
		return reactionFailure("DingTalk reaction could not be encoded", err)
	}
	raw, status, err := r.post(requestCtx, path, token, body)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		// A later operation may acquire a fresh token. Never replay this one.
		r.tokenMu.Lock()
		if r.token == token {
			r.token = ""
		}
		r.tokenMu.Unlock()
	}
	if err != nil {
		return err
	}
	var result struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Success == nil {
		return &reactionError{outcome: Unknown, message: "DingTalk did not explicitly confirm the reaction operation"}
	}
	if !*result.Success {
		return reactionFailure("DingTalk rejected the reaction operation", nil)
	}
	return nil
}

func (r *Reactor) clock() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reactor) accessToken(ctx context.Context) (string, error) {
	r.tokenGateOnce.Do(func() { r.tokenGate = make(chan struct{}, 1) })
	select {
	case r.tokenGate <- struct{}{}:
		defer func() { <-r.tokenGate }()
	case <-ctx.Done():
		return "", reactionFailure("DingTalk reaction token acquisition canceled", ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return "", reactionFailure("DingTalk reaction token acquisition canceled", err)
	}
	credentials := sha256.Sum256([]byte(r.Config.ClientID + "\x00" + r.Config.ClientSecret))
	r.tokenMu.Lock()
	if r.token != "" && r.tokenCredentials == credentials && r.clock().Before(r.tokenExpiresAt) {
		token := r.token
		r.tokenMu.Unlock()
		return token, nil
	}
	r.token = ""
	r.tokenMu.Unlock()
	body, err := json.Marshal(map[string]string{"appKey": r.Config.ClientID, "appSecret": r.Config.ClientSecret})
	if err != nil {
		return "", reactionFailure("DingTalk application credential request could not be encoded", err)
	}
	requestedAt := r.clock()
	raw, _, err := r.post(ctx, reactionTokenPath, "", body)
	if err != nil {
		return "", err
	}
	var result struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    *int64 `json:"expireIn"`
	}
	if json.Unmarshal(raw, &result) != nil || !validReactionToken(result.AccessToken) || result.ExpireIn == nil || *result.ExpireIn <= 0 || *result.ExpireIn > 86400 {
		return "", reactionFailure("DingTalk returned an invalid access token or lifetime", nil)
	}
	ttl := time.Duration(*result.ExpireIn) * time.Second
	margin := min(ttl/10, 30*time.Second)
	expiresAt := requestedAt.Add(ttl - margin)
	if !r.clock().Before(expiresAt) {
		return "", reactionFailure("DingTalk access token expired before it could be used", nil)
	}
	r.tokenMu.Lock()
	r.token, r.tokenCredentials, r.tokenExpiresAt = result.AccessToken, credentials, expiresAt
	r.tokenMu.Unlock()
	return result.AccessToken, nil
}

func validReactionToken(token string) bool {
	if len(token) == 0 || len(token) > 4096 {
		return false
	}
	for i := range len(token) {
		if token[i] <= 0x20 || token[i] >= 0x7f {
			return false
		}
	}
	return true
}

func (r *Reactor) post(ctx context.Context, path, token string, body []byte) ([]byte, int, error) {
	operation := "reaction request"
	uncertain := Unknown
	if path == reactionTokenPath {
		operation = "reaction token request"
		uncertain = Failed
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, reactionFailure("DingTalk "+operation+" canceled before dispatch", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reactionAPIBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, reactionFailure("DingTalk "+operation+" could not be constructed", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("x-acs-dingtalk-access-token", token)
	}
	client := http.Client{Timeout: replyTimeout}
	if r.HTTPClient != nil {
		client = *r.HTTPClient
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, &reactionError{outcome: uncertain, message: "DingTalk " + operation + " failed without platform confirmation", cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 300 && response.StatusCode < 500 {
			uncertain = Failed
		}
		return nil, response.StatusCode, &reactionError{outcome: uncertain, message: fmt.Sprintf("DingTalk %s returned HTTP %d", operation, response.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, response.StatusCode, &reactionError{outcome: uncertain, message: "DingTalk " + operation + " response could not be read within its limit", cause: err}
	}
	return raw, response.StatusCode, nil
}
