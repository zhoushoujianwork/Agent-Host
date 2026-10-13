// Package email exposes managed mailboxes through one account-addressed MCP.
package email

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"agent-host/internal/componentconfig"
	"agent-host/internal/mcpstdio"
)

type Account struct {
	ID             string `json:"id"`
	Address        string `json:"address"`
	Provider       string `json:"provider"`
	CredentialFile string `json:"credential_file"`
	SendEnabled    bool   `json:"send_enabled"`
	Disabled       bool   `json:"disabled,omitempty"`
}
type Config struct {
	Accounts       []Account `json:"accounts"`
	StateDirectory string    `json:"state_directory"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func privateFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("email file path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("email file must be an owner-private regular file")
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("email file must belong to the current user")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open email file")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("email file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("email file exceeds size limit or cannot be read")
	}
	return raw, nil
}
func Load(path string) (Config, string, error) {
	var c Config
	raw, err := privateFile(path)
	if err != nil {
		return c, "", err
	}
	raw, err = componentconfig.Settings(path, raw, "")
	if err != nil {
		return c, "", err
	}
	if componentconfig.DataSourceID(path) == "email" {
		var settings struct {
			Accounts []Account `json:"accounts"`
		}
		if mcpstdio.Decode(raw, &settings) != nil {
			return c, "", errors.New("email datasource settings require accounts only")
		}
		c.Accounts = settings.Accounts
	} else if mcpstdio.Decode(raw, &c) != nil {
		return c, "", errors.New("invalid email configuration")
	}
	if filepath.Base(path) == "plugins.email.json" || componentconfig.DataSourceID(path) == "email" {
		c.StateDirectory = filepath.Join(componentconfig.Root(path), "plugins", "email")
		if componentconfig.DataSourceID(path) == "email" {
			c.StateDirectory = filepath.Join(componentconfig.Root(path), "datasource", "plugins", "email")
		}
		for i := range c.Accounts {
			c.Accounts[i].CredentialFile = componentconfig.Resolve(path, c.Accounts[i].CredentialFile)
		}
	}
	if len(c.Accounts) == 0 || len(c.Accounts) > 32 || !filepath.IsAbs(c.StateDirectory) {
		return c, "", errors.New("email requires 1-32 accounts and an absolute state_directory")
	}
	seen := map[string]bool{}
	for _, a := range c.Accounts {
		parsed, e := mail.ParseAddress(a.Address)
		if !identifier.MatchString(a.ID) || seen[a.ID] || e != nil || parsed.Address != a.Address || strings.ContainsAny(a.Address, "\r\n") {
			return c, "", errors.New("invalid or duplicate email account")
		}
		seen[a.ID] = true
		if a.Provider != "163" && a.Provider != "gmail" {
			return c, "", errors.New("unsupported email provider")
		}
		if a.Provider == "163" && !strings.HasSuffix(strings.ToLower(a.Address), "@163.com") {
			return c, "", errors.New("163 adapter requires a personal @163.com mailbox")
		}
		if !filepath.IsAbs(a.CredentialFile) {
			return c, "", errors.New("credential_file must be absolute")
		}
	}
	canonical, _ := json.Marshal(struct {
		Policy string
		Config Config
	}{"email-mcp-v1", c})
	sum := sha256.Sum256(canonical)
	return c, hex.EncodeToString(sum[:]), nil
}
func (c Config) account(id string) (Account, error) {
	for _, a := range c.Accounts {
		if a.ID == id && !a.Disabled {
			return a, nil
		}
	}
	return Account{}, errors.New("unknown or disabled account_id")
}
func secret(a Account) (string, error) {
	raw, err := privateFile(a.CredentialFile)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("invalid email credential")
	}
	return s, nil
}
func (c Config) Catalog() []map[string]any {
	out := []map[string]any{}
	for _, a := range c.Accounts {
		if a.Disabled {
			continue
		}
		ops := []string{"list_folders", "search_messages", "read_message", "read_attachment"}
		if a.SendEnabled {
			ops = append(ops, "send_message", "reply_message", "operation_status")
		}
		out = append(out, map[string]any{"account_id": a.ID, "address": a.Address, "provider": a.Provider, "operations": ops, "connection_status": "unchecked"})
	}
	return out
}
