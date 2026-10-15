package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"agent-host/internal/harness"
)

var restrictedProviderKeys = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
}

// Bare mode excludes all settings discovery. Preserve only explicit provider
// connection data from the configured profile, never native tools or commands.
// Values are child environment entries and must never appear in argv or evidence.
func restrictedProviderEnvironment(req harness.Request) (map[string]string, error) {
	environment := map[string]string{}
	if !req.RestrictedReadOnly || req.NativeProfile == "" {
		return environment, nil
	}
	directory, err := os.Lstat(req.NativeProfile)
	if errors.Is(err, os.ErrNotExist) {
		return environment, nil
	}
	if err != nil || !directory.IsDir() || !trustedRestrictedAuthEntry(directory) {
		return nil, errors.New("Claude restricted provider profile must be a user-owned directory without group or world write access")
	}
	path := filepath.Join(req.NativeProfile, "settings.json")
	entry, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return environment, nil
	}
	if err != nil || !entry.Mode().IsRegular() || !trustedRestrictedAuthEntry(entry) || entry.Size() > 1<<20 {
		return nil, errors.New("Claude restricted provider settings must be a user-owned regular file of at most 1MiB without group or world write access")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Claude restricted provider settings cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(entry, opened) || !trustedRestrictedAuthEntry(opened) {
		return nil, errors.New("Claude restricted provider settings changed during inspection")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("Claude restricted provider settings cannot be read within size limit")
	}
	var settings struct {
		Env          map[string]json.RawMessage `json:"env"`
		APIKeyHelper json.RawMessage            `json:"apiKeyHelper"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return nil, errors.New("Claude restricted provider settings must contain valid JSON")
	}
	for _, key := range restrictedProviderKeys {
		value, exists := settings.Env[key]
		if !exists {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return nil, fmt.Errorf("Claude restricted provider setting %s must be a string", key)
		}
		environment[key] = text
	}
	if len(settings.APIKeyHelper) != 0 && string(settings.APIKeyHelper) != "null" {
		auth := func(key string) string {
			if value, exists := environment[key]; exists {
				return value
			}
			return os.Getenv(key)
		}
		if auth("ANTHROPIC_AUTH_TOKEN") == "" && auth("ANTHROPIC_API_KEY") == "" {
			return nil, errors.New("Claude restricted mode cannot run apiKeyHelper; configure provider authentication through protected ANTHROPIC_AUTH_TOKEN or ANTHROPIC_API_KEY environment instead")
		}
	}
	return environment, nil
}
