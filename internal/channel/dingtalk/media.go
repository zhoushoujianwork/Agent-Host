package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	messageFileDownloadPath = "/v1.0/robot/messageFiles/download"
	MaxPictureBytes         = 5 << 20
	MaxFileBytes            = 20 << 20
	mediaTimeout            = 20 * time.Second
)

// MediaDownloader resolves authenticated callback capabilities. Configuration
// is immutable after first use. It returns bytes only: storage and execution
// lifecycle belong to the Host. Neither codes nor temporary URLs are returned.
type MediaDownloader struct {
	Config     Config
	HTTPClient *http.Client
	init       sync.Once
	tokens     *Reactor
}

func (d *MediaDownloader) Download(ctx context.Context, attachment Attachment) ([]byte, string, error) {
	if d == nil || ValidateConfig(d.Config) != nil {
		return nil, "", errors.New("DingTalk media client is not configured")
	}
	if !validDownloadCode(attachment.DownloadCode) || attachment.Kind != "image" && attachment.Kind != "file" {
		return nil, "", errors.New("DingTalk attachment has an invalid media reference")
	}
	limit := int64(MaxFileBytes)
	if attachment.Kind == "image" {
		limit = MaxPictureBytes
	}
	if attachment.Size < 0 || attachment.Size > limit {
		return nil, "", errors.New("DingTalk attachment exceeds its size limit")
	}
	robotCode := d.Config.RobotCode
	if robotCode == "" {
		robotCode = d.Config.ClientID
	}
	if len(robotCode) > maxIDBytes || strings.TrimSpace(robotCode) != robotCode || strings.ContainsAny(robotCode, "\r\n\x00") {
		return nil, "", errors.New("DingTalk media robot configuration is invalid")
	}
	d.init.Do(func() { d.tokens = &Reactor{Config: d.Config, HTTPClient: d.HTTPClient} })
	requestCtx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	token, err := d.tokens.accessToken(requestCtx)
	if err != nil {
		return nil, "", &operationError{operation: "media authentication", cause: err}
	}
	body, _ := json.Marshal(map[string]string{"downloadCode": attachment.DownloadCode, "robotCode": robotCode})
	raw, status, err := d.tokens.post(requestCtx, messageFileDownloadPath, token, body)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		d.tokens.tokenMu.Lock()
		if d.tokens.token == token {
			d.tokens.token = ""
		}
		d.tokens.tokenMu.Unlock()
	}
	if err != nil {
		return nil, "", &operationError{operation: "media resolution", cause: err}
	}
	var reply struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return nil, "", errors.New("DingTalk returned an invalid attachment download URL")
	}
	downloadURL, ok := secureMediaURL(reply.DownloadURL)
	if !ok {
		return nil, "", errors.New("DingTalk returned an invalid attachment download URL")
	}
	client := http.Client{Timeout: mediaTimeout}
	if d.HTTPClient != nil {
		client = *d.HTTPClient
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		target, ok := secureMediaURL(req.URL.String())
		if len(via) >= 5 || !ok {
			return errors.New("DingTalk attachment redirect was rejected")
		}
		req.URL, _ = url.Parse(target)
		return nil
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", &operationError{operation: "media request construction", cause: err}
	}
	// Application tokens are never forwarded to CDN requests or redirects.
	response, err := client.Do(req)
	if err != nil {
		return nil, "", &operationError{operation: "media download", cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return nil, "", errors.New("DingTalk attachment download was rejected or too large")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return nil, "", &operationError{operation: "media read within size limit", cause: err}
	}
	mime := http.DetectContentType(data)
	if attachment.Kind == "image" {
		switch mime {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return nil, "", errors.New("DingTalk picture has an unsupported media type")
		}
	}
	return data, mime, nil
}

// DingTalk can resolve a capability to an HTTP OSS URL. Upgrade only trusted
// hosts before dispatch, preserving the signed path and query byte for byte.
// Redirects follow the same rule; media is never transferred over HTTP.
func secureMediaURL(value string) (string, bool) {
	if strings.HasPrefix(value, "http://") {
		value = "https://" + strings.TrimPrefix(value, "http://")
	}
	return value, trustedMediaURL(value)
}

func trustedMediaURL(value string) bool {
	if len(value) > maxWebhookBytes || strings.TrimSpace(value) != value {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Hostname() == "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil {
		return false
	}
	for _, domain := range []string{"aliyuncs.com", "alicdn.com", "dingtalk.com", "dingtalkcdn.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
