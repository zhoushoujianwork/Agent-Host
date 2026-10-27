package feishu

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
)

const endpointPath = "/open-apis/vc/v1/realtime/endpoint"

func endpointArguments(profile, meetingID string) []string {
	params, _ := json.Marshal(map[string]string{"meeting_id": meetingID})
	return []string{"--profile", profile, "api", "GET", endpointPath, "--params", string(params), "--as", "bot", "--format", "json"}
}

// parseAudioEndpoint accepts either an official CLI envelope
// ({"ok":true,"data":{...}}) or the raw platform response ({"code":0,"data":{...}}).
// It never returns, wraps or logs the response, because the URL is a credential.
func parseAudioEndpoint(out Output) (string, error) {
	var top struct {
		OK   *bool           `json:"ok"`
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if !singleJSON(out.Stdout, &top) || top.OK == nil && top.Code == nil {
		return "", errors.New("Feishu realtime endpoint response is invalid")
	}
	if top.OK != nil && !*top.OK || top.Code != nil && *top.Code != 0 {
		return "", errors.New("Feishu rejected the realtime endpoint request")
	}
	var inner struct {
		URL  string          `json:"websocket_url"`
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(top.Data, &inner) != nil {
		return "", errors.New("Feishu realtime endpoint response is invalid")
	}
	if inner.Code != nil && *inner.Code != 0 {
		return "", errors.New("Feishu rejected the realtime endpoint request")
	}
	if inner.URL == "" && inner.Code != nil && len(inner.Data) > 0 { // envelope around a raw response
		inner.Code = nil
		if json.Unmarshal(inner.Data, &inner) != nil {
			return "", errors.New("Feishu realtime endpoint response is invalid")
		}
	}
	return validateAudioURL(inner.URL)
}

func validateAudioURL(raw string) (string, error) {
	bad := errors.New("Feishu realtime endpoint URL is invalid")
	if raw == "" || len(raw) > 4096 {
		return "", bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", bad
	}
	switch u.Scheme {
	case "wss":
	case "ws": // plaintext only for local protocol fixtures
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !(host == "localhost" || ip != nil && ip.IsLoopback()) {
			return "", bad
		}
	default:
		return "", bad
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", bad
	}
	return raw, nil
}
