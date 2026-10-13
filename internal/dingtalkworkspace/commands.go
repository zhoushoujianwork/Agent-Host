package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"

	"agent-host/internal/mcpstdio"
)

var pathWord = regexp.MustCompile(`^\+?[a-z][a-z0-9_-]*$`)

func commandPath(c Config, path string) ([]string, error) {
	parts := strings.Split(path, " ")
	if len(parts) < 2 || len(parts) > 6 || !slices.Contains(c.Services, parts[0]) {
		return nil, errors.New("command must belong to a configured DingTalk service")
	}
	for _, p := range parts {
		if !pathWord.MatchString(p) {
			return nil, errors.New("invalid DingTalk command path")
		}
	}
	return parts, nil
}

func describe(ctx context.Context, run Runner, r Request, path string) (map[string]any, error) {
	r.Args = []string{"schema", "--cli-path", path, "--compact", "--format", "json"}
	raw, err := run(ctx, r)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if mcpstdio.Decode(raw, &schema) != nil || schema["cli_path"] != path || schema["availability"] != "available" {
		return nil, errors.New("command is absent from the pinned official schema")
	}
	if schema["effect"] != "read" && schema["effect"] != "write" {
		return nil, errors.New("command has no supported official read/write contract")
	}
	return schema, nil
}

func commandArguments(parts []string, params map[string]string, schema map[string]any) ([]string, error) {
	declared, _ := schema["parameters"].(map[string]any)
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if _, ok := declared[key]; !ok || !pathWord.MatchString(key) || strings.ContainsRune(value, 0) || len(value) > 32768 {
			return nil, errors.New("parameter is absent from the official leaf schema or exceeds its limit")
		}
		// Global identity, credential, output and destination overrides are never
		// forwarded even if an upstream leaf later declares a same-named flag.
		switch key {
		case "profile", "token", "client-id", "client-secret", "format", "output", "output-file", "yes", "help", "endpoint", "base-url", "api-base-url", "header":
			return nil, errors.New("Host-managed DingTalk parameter cannot be overridden")
		}
		keys = append(keys, key)
	}
	for key, value := range declared {
		field, _ := value.(map[string]any)
		if field["required"] == true && params[key] == "" {
			return nil, errors.New("required DingTalk command parameter is missing")
		}
	}
	sort.Strings(keys)
	args := append([]string(nil), parts...)
	for _, key := range keys {
		args = append(args, "--"+key+"="+params[key])
	}
	return args, nil
}

func verifyIdentity(ctx context.Context, run Runner, r Request, c Config) error {
	r.Args = []string{"profile", "list", "--format", "json"}
	raw, err := run(ctx, r)
	if err != nil {
		return err
	}
	var registry struct {
		Success  bool `json:"success"`
		Profiles []struct {
			Profile string `json:"profile"`
			CorpID  string `json:"corpId"`
			UserID  string `json:"userId"`
		} `json:"profiles"`
	}
	// The official registry has additional navigation fields. Reject duplicate
	// JSON before reading the selected identity, but preserve forward fields.
	var value any
	if mcpstdio.Decode(raw, &value) != nil || json.Unmarshal(raw, &registry) != nil || !registry.Success {
		return errors.New("DingTalk identity registry unavailable; login with agent-host dingtalk-workspace login")
	}
	matches := 0
	for _, p := range registry.Profiles {
		if p.Profile == c.Profile && p.CorpID+":"+p.UserID == c.Profile {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("configured DingTalk identity is not logged in or is ambiguous; no alternate account was selected")
	}
	return nil
}
