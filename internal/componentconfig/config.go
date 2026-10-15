// Package componentconfig decodes the shared startup envelope without loading services.
package componentconfig

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"agent-host/internal/mcpstdio"
)

// Access is accepted only for compatibility with older configuration files.
// Enabled datasource plugins are global; these selectors are not applied.
type Access struct {
	Channels  []string `json:"channels"`
	Audiences []string `json:"audiences"`
}

type Envelope struct {
	Version          int             `json:"version"`
	Enabled          *bool           `json:"enabled"`
	ExecutionProfile string          `json:"execution_profile,omitempty"`
	Access           *Access         `json:"access,omitempty"`
	Settings         json.RawMessage `json:"settings,omitempty"`
}

// DataSources is the built-in configuration registry. An empty provider denotes
// the account-addressed email adapter, which has its own settings contract.
var DataSources = map[string]string{
	"local-documents": "local_markdown",
	"dokki":           "dokki",
	"confluence":      "confluence",
	"email":           "",
}

func DataSourceID(path string) string {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "datasource.plugins.") || !strings.HasSuffix(name, ".json") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, "datasource.plugins."), ".json")
}

func Decode(raw []byte) (Envelope, error) {
	var e Envelope
	if mcpstdio.Decode(raw, &e) != nil || e.Version != 1 || e.Enabled == nil {
		return e, errors.New("component requires version 1, explicit enabled and documented fields")
	}
	return e, nil
}

// Settings preserves legacy direct configurations. Unified files are decoded by
// name, so an absent/disabled DingTalk branch never falls back to legacy DWS.
func Settings(path string, raw []byte, branch string) ([]byte, error) {
	name := filepath.Base(path)
	if DataSourceID(path) == "" && name != "plugins.email.json" && name != "plugins.knowledge.json" && name != "channels.dingtalk.json" && name != "channels.feishu.json" {
		return raw, nil
	}
	e, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	if !*e.Enabled {
		return nil, errors.New("component is disabled")
	}
	if branch == "" {
		return e.Settings, nil
	}
	var branches map[string]json.RawMessage
	if mcpstdio.Decode(e.Settings, &branches) != nil {
		return nil, errors.New("invalid channel settings")
	}
	var fields map[string]json.RawMessage
	if mcpstdio.Decode(branches[branch], &fields) != nil {
		return nil, errors.New("channel feature is absent")
	}
	var enabled bool
	if json.Unmarshal(fields["enabled"], &enabled) != nil || !enabled {
		return nil, errors.New("channel feature is disabled")
	}
	delete(fields, "enabled")
	return json.Marshal(fields)
}

func Root(path string) string { return filepath.Dir(filepath.Dir(path)) }

func Resolve(path, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(path), value)
}
