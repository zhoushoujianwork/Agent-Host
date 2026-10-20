package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"agent-host/internal/harness"
)

// ACPOptions preserves the same Host-controlled Claude policy as print mode.
// These SDK options are supported by the pinned Claude ACP 0.81.2 bridge.
func (a *Adapter) ACPOptions(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (map[string]any, error) {
	if req.Extraction || req.JSONSchema != "" || req.RestrictedReadOnly {
		return nil, errors.New("Claude ACP does not yet support Host extraction, structured output or restricted-member profiles")
	}
	policy, err := nativeStreamEnvironment()
	if err != nil {
		return nil, err
	}
	if _, err = argumentsWithStreamEnvironment(req, policy); err != nil {
		return nil, err
	}
	if err = validateRunDirectories(req.Workspace, req.AdditionalDirectories); err != nil {
		return nil, err
	}
	if req.SkillPluginDir != "" {
		manifest, err := validateSkillPlugin(req.SkillPluginDir, req.SkillPluginDigest)
		if err != nil {
			return nil, err
		}
		if err = a.supportsSkillPlugin(ctx); err != nil {
			return nil, err
		}
		if emit != nil {
			raw, _ := json.Marshal(manifest)
			if err = emit(harness.Observation{Type: "skills_provided", Message: "provided task-scoped native skill package", Raw: raw}); err != nil {
				return nil, err
			}
		}
	}
	sections := []string{req.HostInstructions}
	seen := map[string]bool{}
	for index, directory := range append([]string{req.Workspace}, req.AdditionalDirectories...) {
		if seen[directory] {
			continue
		}
		seen[directory] = true
		text, source, err := a.directoryInstructions(ctx, directory, index > 0)
		if err != nil {
			return nil, err
		}
		if text != "" {
			sections = append(sections, text)
			if emit != nil {
				raw, _ := json.Marshal(map[string]any{"source": source, "workspace": directory, "mechanism": "acp-system-prompt"})
				if err = emit(harness.Observation{Type: "project_instructions", Message: "loaded " + source, Raw: raw}); err != nil {
					return nil, err
				}
			}
		}
	}
	settings := map[string]any{"autoMemoryEnabled": false, "env": policy}
	options := map[string]any{"strictMcpConfig": true, "model": req.Model, "persistSession": true, "settings": settings,
		"systemPrompt": map[string]any{"type": "preset", "preset": "claude_code", "append": withKnowledgeGuidance(strings.Join(sections, "\n\n"), req.ToolsDisabled)}}
	if len(req.AllowedTools) > 0 {
		options["allowedTools"] = req.AllowedTools
	}
	if req.ToolsDisabled {
		options["tools"] = []string{}
		options["extraArgs"] = map[string]any{"disable-slash-commands": nil, "no-chrome": nil}
		settings["disableAllHooks"] = true
	}
	if req.SkillPluginDir != "" {
		options["plugins"] = []map[string]string{{"type": "local", "path": req.SkillPluginDir}}
	}
	return options, nil
}

// ValidateACPInitialization applies the same strict MCP admission as print mode
// to the pinned bridge's opt-in native initialization evidence.
func ValidateACPInitialization(raw json.RawMessage, servers []harness.MCPServer) (string, json.RawMessage, error) {
	var native envelope
	if err := json.Unmarshal(raw, &native); err != nil {
		return "", nil, err
	}
	if native.Type != "system" || native.Subtype != "init" {
		return "", nil, errors.New("missing native initialization evidence")
	}
	decoder := streamDecoder{mcpServers: servers}
	admitted := decoder.validateMCPInitialization(native)
	statuses := []map[string]string{}
	for _, value := range native.MCPServers {
		var server struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(value, &server)
		statuses = append(statuses, map[string]string{"name": server.Name, "status": server.Status})
	}
	var inventory struct {
		Skills  []string `json:"skills"`
		Plugins []struct {
			Name    string `json:"name"`
			Version string `json:"version,omitempty"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return native.Model, nil, errors.New("invalid native capability inventory")
	}
	projected, _ := json.Marshal(map[string]any{"protocol": "acp", "native_initialization": true, "model": native.Model, "mcp_servers": statuses, "admitted": admitted == nil, "skills": inventory.Skills, "tools": native.Tools, "plugins": inventory.Plugins})
	return native.Model, projected, admitted
}
