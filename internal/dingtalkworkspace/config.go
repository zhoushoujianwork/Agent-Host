package dingtalkworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"agent-host/internal/mcpstdio"
)

type Config struct {
	Profile        string   `json:"profile"`
	StateDirectory string   `json:"state_directory"`
	Services       []string `json:"services"`
}

var Services = map[string]string{
	"chat":     "Send as the authorized owner, inspect send status, find conversations and read bounded message history.",
	"aisearch": "Find people by name, department or responsibility (AI搜问).",
	"contact":  "Read enterprise contacts, departments and the current user's identity.",
	"calendar": "Read calendars and availability; manage appointments and meeting rooms.",
	"todo":     "Read, create and update personal tasks (待办).",
	"doc":      "Search, read and edit DingTalk documents.",
	"drive":    "Find and manage DingTalk files and folders.",
	"minutes":  "Read meeting summaries, transcripts and action items (AI听记).",
	"wiki":     "Find and manage knowledge spaces.",
	"aitable":  "Read and edit AI tables (多维表).",
	"sheet":    "Read and edit online spreadsheet cells.",
	"mail":     "Search and read DingTalk mail; send only when the user requests it.",
	"oa":       "Read and process approvals (审批).",
	"report":   "Read and submit daily or weekly reports (日志).",
}

func Load(path string) (Config, string, error) {
	var c Config
	raw, err := readPrivate(path)
	if err != nil {
		return c, "", err
	}
	if mcpstdio.Decode(raw, &c) != nil {
		return c, "", errors.New("invalid DingTalk workspace configuration")
	}
	parts := strings.Split(c.Profile, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(c.Profile) > 1024 || strings.ContainsAny(c.Profile, " \t\r\n\x00") {
		return c, "", errors.New("DingTalk profile must be the exact corpId:userId identity")
	}
	if !filepath.IsAbs(c.StateDirectory) || filepath.Clean(c.StateDirectory) != c.StateDirectory || len(c.Services) == 0 {
		return c, "", errors.New("provide an absolute state_directory and selected services")
	}
	seen := map[string]bool{}
	for _, s := range c.Services {
		if Services[s] == "" || seen[s] {
			return c, "", errors.New("unsupported or duplicate DingTalk service")
		}
		seen[s] = true
	}
	sum := sha256.Sum256(append([]byte("dingtalk-workspace:"+Version+":"), raw...))
	return c, hex.EncodeToString(sum[:]), nil
}

func readPrivate(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("DingTalk configuration path must be clean and absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("DingTalk configuration must be an owner-private regular file")
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("DingTalk configuration must belong to the current user")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open DingTalk configuration")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("DingTalk configuration changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("DingTalk private file exceeds its read limit")
	}
	return b, nil
}

// Binding keeps the tool discoverable before owner authorization is configured.
// The bot profile is an assertion; it cannot select another user's credential.
func Binding(path, expected string) (Config, string, bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if !filepath.IsAbs(path) || expected == "" {
			return Config{}, "", false, errors.New("missing DingTalk workspace configuration")
		}
		sum := sha256.Sum256([]byte("dingtalk-unconfigured:" + Version + ":" + path + ":" + expected))
		return Config{Profile: expected}, hex.EncodeToString(sum[:]), false, nil
	}
	c, fp, err := Load(path)
	if err != nil {
		return c, "", false, err
	}
	if expected != "" && c.Profile != expected {
		return c, "", false, errors.New("DingTalk workspace profile must match the configured bot owner")
	}
	return c, fp, true, nil
}
