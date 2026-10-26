// Package doubao implements the observed Doubao binary ASR and legacy TTS
// WebSocket protocols. Configuration is explicit and never loaded from disk.
package doubao

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type Config struct {
	ASREndpoint, TTSEndpoint                     string
	APIKeyV2, AppID, AccessToken                 string
	ASRCluster, TTSCluster, ResourceID, ASRModel string
	// ConnectTimeout bounds dialing AND the initial request write. Zero means 10s.
	ConnectTimeout time.Duration
}

func (c Config) validate(tts bool) error {
	endpoint := c.ASREndpoint
	if tts {
		endpoint = c.TTSEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.Fragment != "" {
		return errors.New("doubao: invalid WebSocket endpoint")
	}
	if c.ConnectTimeout < 0 {
		return errors.New("doubao: negative connect timeout")
	}
	for _, v := range []string{c.APIKeyV2, c.AppID, c.AccessToken, c.ResourceID, c.ASRCluster, c.TTSCluster} {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("doubao: invalid authentication configuration")
		}
	}
	if tts {
		if u.Path != "/api/v1/tts/ws_binary" {
			return errors.New("doubao: only legacy /api/v1/tts/ws_binary TTS is supported")
		}
		if strings.TrimSpace(c.AppID) == "" || strings.TrimSpace(c.AccessToken) == "" || strings.TrimSpace(c.TTSCluster) == "" {
			return errors.New("doubao: legacy TTS requires app ID, access token and cluster")
		}
	} else {
		if strings.TrimSpace(c.APIKeyV2) == "" && (strings.TrimSpace(c.AppID) == "" || strings.TrimSpace(c.AccessToken) == "") {
			return errors.New("doubao: ASR requires API key v2 or app ID and access token")
		}
		if strings.TrimSpace(c.ResourceID) == "" && strings.TrimSpace(c.ASRCluster) == "" {
			return errors.New("doubao: ASR resource ID or cluster is required")
		}
	}
	return nil
}

func (c Config) headers(tts bool) (http.Header, error) {
	h := make(http.Header)
	if tts {
		h.Set("Authorization", "Bearer;"+c.AccessToken)
		return h, nil
	}
	if c.APIKeyV2 != "" {
		h.Set("X-Api-Key", c.APIKeyV2)
	} else {
		h.Set("X-Api-App-Key", c.AppID)
		h.Set("X-Api-Access-Key", c.AccessToken)
	}
	resource := c.ResourceID
	if resource == "" {
		resource = c.ASRCluster
	}
	h.Set("X-Api-Resource-Id", resource)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, errors.New("doubao: connection identity unavailable")
	}
	h.Set("X-Api-Connect-Id", hex.EncodeToString(id[:]))
	return h, nil
}

// start bounds the complete setup and never returns remote bodies or URLs.
func (c Config) start(ctx context.Context, tts bool, request any) (*websocket.Conn, error) {
	payload, err := marshalRequest(request)
	if err != nil {
		return nil, err
	}
	h, err := c.headers(tts)
	if err != nil {
		return nil, err
	}
	timeout := c.ConnectTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	setup, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := c.ASREndpoint
	if tts {
		endpoint = c.TTSEndpoint
	}
	// Do not follow redirects with authentication or signed endpoint parameters.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, resp, err := websocket.Dial(setup, endpoint, &websocket.DialOptions{HTTPHeader: h, HTTPClient: client})
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, safeError(setup, "connect failed")
	}
	conn.SetReadLimit(maxMessage)
	if err = conn.Write(setup, websocket.MessageBinary, encode(frame{kind: fullRequest, serialization: 1, compression: 1, payload: payload})); err != nil {
		_ = conn.CloseNow()
		return nil, safeError(setup, "initial request failed")
	}
	return conn, nil
}

func safeError(ctx context.Context, message string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("doubao: " + message)
}
