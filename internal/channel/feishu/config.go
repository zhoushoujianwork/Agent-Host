// Package feishu connects authorized text IM to the Host through lark-cli.
// Credentials stay in the CLI profile; execution and durable intake belong to the Host.
package feishu

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Config struct {
	Profile     string `json:"profile"`
	AppID       string `json:"app_id"`
	TenantID    string `json:"tenant_id"`
	BotOpenID   string `json:"bot_open_id"`
	OwnerOpenID string `json:"owner_open_id"`
}

var (
	profileID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	appID     = regexp.MustCompile(`^cli_[A-Za-z0-9]{1,64}$`)
	openID    = regexp.MustCompile(`^ou_[A-Za-z0-9]{1,64}$`)
	messageID = regexp.MustCompile(`^om_[A-Za-z0-9]{1,128}$`)
	chatID    = regexp.MustCompile(`^oc_[A-Za-z0-9]{1,128}$`)
	uuidID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,49}$`)
)

func (c Config) Validate() error {
	if !profileID.MatchString(c.Profile) || !appID.MatchString(c.AppID) || !openID.MatchString(c.BotOpenID) || !openID.MatchString(c.OwnerOpenID) || c.OwnerOpenID == c.BotOpenID {
		return errors.New("Feishu IM requires an explicit profile, application, bot Open ID and distinct owner Open ID")
	}
	if !identifier(c.TenantID, 256) {
		return errors.New("Feishu IM requires its actual tenant identifier")
	}
	return nil
}

func identifier(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
