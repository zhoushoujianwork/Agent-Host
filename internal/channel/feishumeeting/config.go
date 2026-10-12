// Package feishumeeting connects authorized meetings to the Host through lark-cli.
// Credentials remain in the CLI profile; native execution belongs to the Host.
package feishumeeting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Profile              string        `json:"profile"`
	AppID                string        `json:"app_id"`
	BotOpenID            string        `json:"bot_open_id"`
	MeetingID            string        `json:"meeting_id"`
	TenantID             string        `json:"tenant_id"`
	AllowedSenderOpenIDs []string      `json:"allowed_sender_open_ids"`
	PollInterval         time.Duration `json:"-"`
	AutoJoin             bool          `json:"auto_join,omitempty"`
}

var (
	openID      = regexp.MustCompile(`^ou_[A-Za-z0-9]{1,64}$`)
	appID       = regexp.MustCompile(`^cli_[A-Za-z0-9]{1,64}$`)
	meetingID   = regexp.MustCompile(`^[0-9]{10,30}$`)
	profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func (c Config) Validate() error {
	if !profileName.MatchString(c.Profile) || !appID.MatchString(c.AppID) || !openID.MatchString(c.BotOpenID) || (!meetingID.MatchString(c.MeetingID) && !(c.AutoJoin && c.MeetingID == "")) {
		return errors.New("Feishu meeting requires an explicit profile, app ID, bot open ID, and a long meeting ID unless auto_join is enabled")
	}
	if strings.TrimSpace(c.TenantID) != c.TenantID || c.TenantID == "" || len(c.TenantID) > 256 || strings.ContainsAny(c.TenantID, "\x00\r\n") {
		return errors.New("Feishu meeting requires its actual tenant identifier")
	}
	if c.PollInterval < 5*time.Second {
		return errors.New("Feishu meeting poll interval must be at least 5 seconds")
	}
	if len(c.AllowedSenderOpenIDs) == 0 || len(c.AllowedSenderOpenIDs) > 100 {
		return errors.New("Feishu meeting requires an explicit sender allowlist")
	}
	seen := map[string]bool{}
	for _, id := range c.AllowedSenderOpenIDs {
		if !openID.MatchString(id) || id == c.BotOpenID || seen[id] {
			return errors.New("Feishu meeting sender allowlist contains an invalid or duplicate human open ID")
		}
		seen[id] = true
	}
	return nil
}

// ValidateMeeting requires a concrete destination even in automatic entry mode.
func (c Config) ValidateMeeting() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !meetingID.MatchString(c.MeetingID) {
		return errors.New("Feishu meeting transport requires a concrete long meeting ID")
	}
	return nil
}

func (c Config) scope() string {
	b, _ := json.Marshal([]string{c.Profile, c.AppID, c.TenantID, c.BotOpenID, c.MeetingID})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func (c Config) arguments(command string) []string {
	return []string{"--profile", c.Profile, "vc", command, "--as", "bot", "--meeting-id", c.MeetingID}
}
