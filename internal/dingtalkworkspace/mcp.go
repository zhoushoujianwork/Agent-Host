package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"agent-host/internal/mcpstdio"
)

type Service struct {
	Path, Fingerprint, ExpectedProfile string
	Run                                Runner
}
type Args struct {
	Service     string            `json:"service,omitempty"`
	Query       string            `json:"query,omitempty"`
	Offset      int               `json:"offset,omitempty"`
	Limit       int               `json:"limit,omitempty"`
	Command     string            `json:"command,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Confirmed   bool              `json:"confirmed,omitempty"`
	OperationID string            `json:"operation_id,omitempty"`
}

func (s Service) Call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	c, fp, configured, err := Binding(s.Path, s.ExpectedProfile)
	if err != nil {
		return nil, err
	}
	if fp != s.Fingerprint {
		return nil, errors.New("DingTalk workspace configuration changed; start a new native session")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var a Args
	if mcpstdio.Decode(raw, &a) != nil {
		return nil, errors.New("invalid DingTalk tool arguments")
	}
	if err := validateFields(name, raw); err != nil {
		return nil, err
	}
	if name == "list_services" {
		ids := append([]string(nil), c.Services...)
		if !configured {
			for id := range Services {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		catalog := []map[string]string{}
		for _, id := range ids {
			catalog = append(catalog, map[string]string{"service": id, "description": Services[id]})
		}
		return map[string]any{"profile": c.Profile, "configured": configured, "authentication_verified": false, "official_version": Version, "services": catalog, "setup": "Configure the owner workspace and run agent-host dingtalk-workspace login --config PATH. No separate dws installation is needed."}, nil
	}
	if !configured {
		return nil, errors.New("DingTalk workspace is not configured; owner login is required")
	}
	r := Request{Config: s.Path, Fingerprint: fp}
	if name == "operation_status" {
		return operationStatus(c, a.OperationID, a.Offset)
	}
	if name == "list_commands" {
		if !slices.Contains(c.Services, a.Service) || a.Offset < 0 {
			return nil, errors.New("select a configured service and a nonnegative offset")
		}
		if a.Limit == 0 {
			a.Limit = 10
		}
		if a.Limit < 1 || a.Limit > 20 {
			return nil, errors.New("limit must be 1-20")
		}
		r.Args = []string{"schema", a.Service, "--compact", "--format", "json"}
		body, err := s.Run(ctx, r)
		if err != nil {
			return nil, err
		}
		var value any
		if mcpstdio.Decode(body, &value) != nil {
			return nil, errors.New("invalid official command catalog")
		}
		all := []map[string]any{}
		collectCommands(value, a.Service, a.Query, &all)
		sort.Slice(all, func(i, j int) bool { return all[i]["command"].(string) < all[j]["command"].(string) })
		if a.Offset > len(all) {
			return nil, errors.New("command offset is outside the catalog")
		}
		end := min(a.Offset+a.Limit, len(all))
		next := 0
		if end < len(all) {
			next = end
		}
		return map[string]any{"commands": all[a.Offset:end], "total": len(all), "next_offset": next, "official_version": Version}, nil
	}
	parts, err := commandPath(c, a.Command)
	if err != nil {
		return nil, err
	}
	schema, err := describe(ctx, s.Run, r, a.Command)
	if err != nil {
		return nil, err
	}
	if name == "describe_command" {
		return schema, nil
	}
	if name != "execute_command" {
		return nil, errors.New("unknown DingTalk tool")
	}
	args, err := commandArguments(parts, a.Parameters, schema)
	if err != nil {
		return nil, err
	}
	write := schema["effect"] == "write"
	if write && schema["confirmation"] == "user_required" && !a.Confirmed {
		return nil, errors.New("official contract requires user confirmation of this action and target before confirmed=true")
	}
	if err := verifyIdentity(ctx, s.Run, r, c); err != nil {
		return nil, err
	}
	args = append(args, "--profile", c.Profile, "--format", "json")
	if write {
		args = append(args, "--yes")
	}
	r.Args = args
	if len(encodeRequest(r)) > 65536 {
		return nil, errors.New("DingTalk command request exceeds the worker limit")
	}
	if write {
		return executeOnce(ctx, c, a.OperationID, r, s.Run)
	}
	body, err := s.Run(ctx, r)
	if err != nil {
		var failure *CommandFailure
		if errors.As(err, &failure) && len(body) <= 12000 {
			return nil, fmt.Errorf("official DingTalk failure: %s", body)
		}
		return nil, err
	}
	if len(body) > 12000 {
		return nil, errors.New("DingTalk result exceeds the tool limit; use a smaller page or narrower read")
	}
	var result struct {
		Outcome string `json:"outcome"`
		OK      *bool  `json:"ok"`
		Success *bool  `json:"success"`
	}
	if json.Unmarshal(body, &result) == nil && (result.Outcome == "failure" || result.OK != nil && !*result.OK || result.Success != nil && !*result.Success) {
		return nil, fmt.Errorf("official DingTalk failure: %s", body)
	}
	return json.RawMessage(body), nil
}

func collectCommands(value any, service, query string, out *[]map[string]any) {
	switch v := value.(type) {
	case map[string]any:
		path, _ := v["cli_path"].(string)
		if strings.HasPrefix(path, service+" ") && v["availability"] == "available" && (v["effect"] == "read" || v["effect"] == "write") {
			summary, _ := v["agent_summary"].(string)
			if query == "" || strings.Contains(strings.ToLower(path+" "+summary), strings.ToLower(query)) {
				*out = append(*out, map[string]any{"command": path, "summary": summary, "effect": v["effect"], "confirmation": v["confirmation"]})
			}
			return
		}
		for _, item := range v {
			collectCommands(item, service, query, out)
		}
	case []any:
		for _, item := range v {
			collectCommands(item, service, query, out)
		}
	}
}

func Serve(ctx context.Context, s Service, in io.Reader, out io.Writer) error {
	if s.Fingerprint == "" || s.Run == nil {
		return errors.New("DingTalk MCP requires a pinned binding and embedded worker")
	}
	if _, fp, _, err := Binding(s.Path, s.ExpectedProfile); err != nil || fp != s.Fingerprint {
		return errors.New("DingTalk MCP binding is unavailable or changed")
	}
	return mcpstdio.Serve(ctx, "agent-host-dingtalk-workspace", Tools(), s.Call, in, out)
}
