// Package discovery collects explicitly selected daily DWS conversations and
// proposes evidence-backed topics independently of Agent task execution.
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"agent-host/internal/dingtalkworkspace"
	"agent-host/internal/state"
)

type Config struct {
	DWSExecutable             string   `json:"dws_executable,omitempty"`
	DingTalkWorkspaceConfig   string   `json:"dingtalk_workspace_config,omitempty"`
	Profile                   string   `json:"profile"`
	IntervalSeconds           int      `json:"interval_seconds"`
	InitialWindowHours        int      `json:"initial_window_hours"`
	ReconciliationWindowHours int      `json:"reconciliation_window_hours"`
	RetentionDays             int      `json:"retention_days"`
	PageLimit                 int      `json:"page_limit"`
	MaxMessages               int      `json:"max_messages"`
	TimeoutSeconds            int      `json:"timeout_seconds"`
	Model                     string   `json:"model"`
	Sources                   []Source `json:"sources"`
}

type Source struct {
	ConversationID     string `json:"conversation_id"`
	Type               string `json:"type"`
	PeerOpenDingTalkID string `json:"peer_open_dingtalk_id,omitempty"`
	ProjectID          string `json:"project_id"`
}

func (s Source) Attribution(cfg Config) state.DiscoverySource {
	identity := strings.SplitN(cfg.Profile, ":", 2)
	attr := state.DiscoverySource{ID: state.DiscoverySourceID(cfg.Profile, s.ConversationID, s.ProjectID, s.Type, s.PeerOpenDingTalkID), Profile: cfg.Profile, ConversationID: s.ConversationID, Type: s.Type, PeerOpenDingTalkID: s.PeerOpenDingTalkID, ProjectID: s.ProjectID}
	if len(identity) == 2 {
		attr.TenantID, attr.PrincipalID = identity[0], identity[1]
	}
	return attr
}

var stableIdentity = regexp.MustCompile(`^[A-Za-z0-9._=+/-]{1,256}$`)
var profileIdentity = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}:[A-Za-z0-9._-]{1,128}$`)
var projectIdentity = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// LoadConfig accepts one owner-private, strict JSON file. Selecting a source
// is an explicit collection grant; a default account or all-chat mode is absent.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := readPrivateConfig(path)
	if err != nil {
		return cfg, err
	}
	if err = decodeStrict(b, &cfg); err != nil {
		return cfg, errors.New("discovery config must contain unique documented JSON fields with valid types")
	}
	return cfg, cfg.Check(context.Background(), nil)
}

// Check validates static bounds, plus current project registrations when a
// Store is supplied. It does not authenticate or read live chat content.
func (cfg Config) Check(ctx context.Context, store *state.Store) error {
	var err error
	if cfg.DingTalkWorkspaceConfig != "" {
		if cfg.DWSExecutable != "" {
			return errors.New("select either embedded DingTalk workspace or external DWS executable, never both")
		}
		c, _, e := dingtalkworkspace.Load(cfg.DingTalkWorkspaceConfig)
		if e != nil {
			return e
		}
		if c.Profile != cfg.Profile {
			return errors.New("discovery workspace identity differs from the selected source profile")
		}
	} else {
		if !filepath.IsAbs(cfg.DWSExecutable) || filepath.Clean(cfg.DWSExecutable) != cfg.DWSExecutable || strings.ContainsAny(cfg.DWSExecutable, "\x00\r\n") {
			return errors.New("discovery DWS executable must be an explicit clean absolute path")
		}
		info, err := os.Stat(cfg.DWSExecutable)
		if err != nil {
			return fmt.Errorf("inspect discovery executable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return errors.New("discovery DWS executable must be an executable regular file")
		}
	}
	if !profileIdentity.MatchString(cfg.Profile) {
		return errors.New("discovery requires one fixed corpId:userId personal profile")
	}
	if cfg.IntervalSeconds < 30 || cfg.IntervalSeconds > 86400 || cfg.InitialWindowHours < 2 || cfg.InitialWindowHours > 720 || cfg.ReconciliationWindowHours < 1 || cfg.ReconciliationWindowHours > 168 || cfg.ReconciliationWindowHours >= cfg.InitialWindowHours || cfg.RetentionDays < 1 || cfg.RetentionDays > 365 || cfg.InitialWindowHours > cfg.RetentionDays*24 || cfg.ReconciliationWindowHours > cfg.RetentionDays*24 || cfg.PageLimit < 1 || cfg.PageLimit > 50 || cfg.MaxMessages < 1 || cfg.MaxMessages > 500 || cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 300 {
		return errors.New("discovery interval, windows, retention, pages, messages or timeout exceed bounded limits")
	}
	if strings.TrimSpace(cfg.Model) == "" || len(cfg.Model) > 256 || strings.HasPrefix(cfg.Model, "-") || strings.ContainsAny(cfg.Model, " \t\r\n\x00") {
		return errors.New("discovery extractor model must be a single non-flag identifier")
	}
	if len(cfg.Sources) < 1 || len(cfg.Sources) > 16 {
		return errors.New("discovery requires 1-16 explicitly selected conversations")
	}
	seen := map[string]bool{}
	for _, s := range cfg.Sources {
		if !stableIdentity.MatchString(s.ConversationID) || !projectIdentity.MatchString(s.ProjectID) || s.ProjectID == "default" || seen[s.ConversationID] {
			return errors.New("discovery needs unique stable conversation IDs and explicit registered projects")
		}
		seen[s.ConversationID] = true
		if s.Type != "dm" && s.Type != "group" || s.Type == "dm" && !stableIdentity.MatchString(s.PeerOpenDingTalkID) || s.Type == "group" && s.PeerOpenDingTalkID != "" {
			return errors.New("discovery sources require dm with a stable peer openDingTalkId, or group with a conversation ID")
		}
		if store != nil {
			if _, err = store.Project(ctx, s.ProjectID); err != nil {
				return fmt.Errorf("discovery project %s is unavailable: %w", s.ProjectID, err)
			}
		}
	}
	return nil
}

func readPrivateConfig(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("discovery config path must be a clean absolute path")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("discovery config must be an owner-private regular file (0600 or 0400), without symlinks")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open private discovery config")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 {
		return nil, errors.New("discovery config changed while opening")
	}
	owner, ok := after.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("discovery config must belong to the current user")
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return nil, errors.New("discovery config exceeds its 64 KiB read limit")
	}
	return b, nil
}

func rejectDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[strings.ToLower(key)] {
					return errors.New("duplicate JSON field")
				}
				seen[strings.ToLower(key)] = true
				if err = value(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err = value(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func decodeStrict(raw []byte, out any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid JSON fields or types")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
