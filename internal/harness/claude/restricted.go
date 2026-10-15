package claude

import (
	"errors"
	"fmt"

	"agent-host/internal/harness"
)

const restrictedApprovalTool = "mcp__approval__request_approval"

func validateRestrictedInventory(tools []string, servers []harness.MCPServer) error {
	if len(tools) == 0 {
		return errors.New("Claude restricted initialization did not establish a native tool inventory")
	}
	for _, tool := range tools {
		switch tool {
		case "Read", "Glob", "Grep":
		case restrictedApprovalTool:
			if len(servers) != 1 || servers[0].Name != "approval" {
				return errors.New("Claude restricted initialization exposed an unconfigured approval tool")
			}
		default:
			return fmt.Errorf("Claude restricted initialization exposed forbidden tool %q", tool)
		}
	}
	return nil
}

// This fixed profile is deliberately independent of configurable permission
// rules. In particular, shell-pattern denials cannot establish read-only access.
func validateRestrictedReadOnly(req harness.Request) error {
	if !req.RestrictedReadOnly {
		return nil
	}
	if req.PermissionMode != "" && req.PermissionMode != "dontAsk" {
		return errors.New("Claude restricted reads require dontAsk permissions")
	}
	if req.ToolsDisabled || req.Extraction || len(req.AdditionalDirectories) != 0 || req.SkillPluginDir != "" || req.SkillPluginDigest != "" {
		return errors.New("Claude restricted reads conflict with tool-free execution, extraction, additional directories or native skills")
	}
	for _, server := range req.MCPServers {
		if server.Name != "approval" {
			return fmt.Errorf("Claude restricted reads cannot mount MCP server %q", server.Name)
		}
	}
	for _, tool := range req.AllowedTools {
		switch tool {
		case "Read", "Glob", "Grep":
		case restrictedApprovalTool:
			if len(req.MCPServers) == 0 {
				return errors.New("Claude restricted approval tool requires the explicit approval MCP server")
			}
		default:
			return fmt.Errorf("Claude restricted reads cannot allow tool rule %q", tool)
		}
	}
	return nil
}
