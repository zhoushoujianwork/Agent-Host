package feishumeeting

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultWakePrefix is the explicit default when a voice config omits one.
	DefaultWakePrefix = "AI"
	ttsEndpointPath   = "/api/v1/tts/ws_binary"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// VoiceConfig enables the optional unattributed-audio bridge. It never holds
// credential values: every secret is referenced by environment variable name.
type VoiceConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// AcceptUnattributedAudio must be true to enable voice: meeting audio is not
	// tied to a human identity or to the sender allowlist.
	AcceptUnattributedAudio bool   `json:"accept_unattributed_audio,omitempty"`
	WakePrefix              string `json:"wake_prefix,omitempty"`
	// CredentialsFile optionally names a private JSON object that maps the
	// referenced environment variable names to secret strings. When set, it is
	// the only credential source and the inherited environment is not consulted.
	// A relative path is resolved against the directory of the Feishu
	// configuration file by the loader. The file is read by the Host adapter.
	CredentialsFile string   `json:"credentials_file,omitempty"`
	ASR             VoiceASR `json:"asr,omitempty"`
	TTS             VoiceTTS `json:"tts,omitempty"`
}

// ReferencedEnvNames returns the distinct, nonempty credential names this
// configuration references, in a stable order.
func (v VoiceConfig) ReferencedEnvNames() []string {
	var names []string
	for _, name := range []string{v.ASR.APIKeyEnv, v.ASR.AppIDEnv, v.ASR.AccessTokenEnv, v.TTS.AppIDEnv, v.TTS.AccessTokenEnv} {
		if name == "" {
			continue
		}
		seen := false
		for _, existing := range names {
			seen = seen || existing == name
		}
		if !seen {
			names = append(names, name)
		}
	}
	return names
}

type VoiceASR struct {
	Endpoint       string `json:"endpoint,omitempty"`
	Cluster        string `json:"cluster,omitempty"`
	ResourceID     string `json:"resource_id,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	AppIDEnv       string `json:"app_id_env,omitempty"`
	AccessTokenEnv string `json:"access_token_env,omitempty"`
}

type VoiceTTS struct {
	Endpoint       string `json:"endpoint,omitempty"`
	Cluster        string `json:"cluster,omitempty"`
	Voice          string `json:"voice,omitempty"`
	AppIDEnv       string `json:"app_id_env,omitempty"`
	AccessTokenEnv string `json:"access_token_env,omitempty"`
}

// VoiceCredentials are resolved values and must never be logged or persisted.
type VoiceCredentials struct {
	ASRAPIKey, ASRAppID, ASRAccessToken string
	TTSAppID, TTSAccessToken            string
}

// Normalized returns the configuration with explicit defaults applied.
func (v VoiceConfig) Normalized() VoiceConfig {
	if v.Enabled && v.WakePrefix == "" {
		v.WakePrefix = DefaultWakePrefix
	}
	return v
}

// Validate is local only. A disabled block needs no further fields.
func (v VoiceConfig) Validate() error {
	if !v.Enabled {
		return nil
	}
	if !v.AcceptUnattributedAudio {
		return errors.New("Feishu voice requires accept_unattributed_audio true: meeting audio has no verified speaker identity and bypasses the sender allowlist")
	}
	v = v.Normalized()
	if f := v.CredentialsFile; f != "" && (len(f) > 4096 || !utf8.ValidString(f) || strings.TrimSpace(f) != f || strings.ContainsAny(f, "\x00\r\n")) {
		return errors.New("Feishu voice credentials_file must be a single-line file path")
	}
	if !validWakePrefix(v.WakePrefix) {
		return errors.New("Feishu voice wake_prefix must be 1-32 bytes of printable text without control characters")
	}
	if err := checkEndpoint(v.ASR.Endpoint, ""); err != nil {
		return errors.New("Feishu voice asr.endpoint must be a ws:// or wss:// URL without credentials")
	}
	if err := checkEndpoint(v.TTS.Endpoint, ttsEndpointPath); err != nil {
		return errors.New("Feishu voice tts.endpoint must be a ws:// or wss:// URL without credentials and use " + ttsEndpointPath)
	}
	for _, name := range []string{v.ASR.APIKeyEnv, v.ASR.AppIDEnv, v.ASR.AccessTokenEnv, v.TTS.AppIDEnv, v.TTS.AccessTokenEnv} {
		if name != "" && !envName.MatchString(name) {
			return errors.New("Feishu voice credentials must be referenced by environment variable name, never by value")
		}
	}
	if v.ASR.APIKeyEnv == "" && (v.ASR.AppIDEnv == "" || v.ASR.AccessTokenEnv == "") {
		return errors.New("Feishu voice ASR requires api_key_env or both app_id_env and access_token_env")
	}
	if strings.TrimSpace(v.ASR.ResourceID) == "" && strings.TrimSpace(v.ASR.Cluster) == "" {
		return errors.New("Feishu voice ASR requires resource_id or cluster")
	}
	if v.TTS.AppIDEnv == "" || v.TTS.AccessTokenEnv == "" || strings.TrimSpace(v.TTS.Cluster) == "" || strings.TrimSpace(v.TTS.Voice) == "" {
		return errors.New("Feishu voice TTS requires app_id_env, access_token_env, cluster and voice")
	}
	for _, s := range []string{v.ASR.Cluster, v.ASR.ResourceID, v.TTS.Cluster, v.TTS.Voice} {
		if len(s) > 256 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
			return errors.New("Feishu voice cluster, resource and voice identifiers are invalid")
		}
	}
	return nil
}

// ResolveCredentials reads only the referenced variables. It reports names,
// never values, and performs no network access.
func (v VoiceConfig) ResolveCredentials(lookup func(string) (string, bool)) (VoiceCredentials, error) {
	var c VoiceCredentials
	if err := v.Validate(); err != nil || !v.Enabled {
		return c, err
	}
	var missing []string
	get := func(name string) string {
		if name == "" {
			return ""
		}
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			missing = append(missing, name)
			return ""
		}
		return value
	}
	c.ASRAPIKey, c.ASRAppID, c.ASRAccessToken = get(v.ASR.APIKeyEnv), get(v.ASR.AppIDEnv), get(v.ASR.AccessTokenEnv)
	c.TTSAppID, c.TTSAccessToken = get(v.TTS.AppIDEnv), get(v.TTS.AccessTokenEnv)
	if len(missing) > 0 {
		return VoiceCredentials{}, errors.New("Feishu voice credential environment variables are unset, empty or malformed: " + strings.Join(missing, ", "))
	}
	return c, nil
}

func validWakePrefix(s string) bool {
	if s == "" || len(s) > 32 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func checkEndpoint(raw, path string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return errors.New("invalid endpoint")
	}
	if path != "" && u.Path != path {
		return errors.New("invalid endpoint path")
	}
	return nil
}
