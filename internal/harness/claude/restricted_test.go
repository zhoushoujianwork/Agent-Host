package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func restrictedRequest(t *testing.T) harness.Request {
	t.Helper()
	return harness.Request{Prompt: "Inspect this project", Workspace: t.TempDir(), NativeSessionID: "e7a0d011-08ab-48ca-908e-77d853094ba0", RestrictedReadOnly: true}
}

func TestRestrictedReadOnlyArguments(t *testing.T) {
	for _, resume := range []bool{false, true} {
		req := restrictedRequest(t)
		req.Resume = resume
		args, err := argumentsWithStreamEnvironment(req, map[string]string{nativeFallbackEnvironment: "1"})
		if err != nil {
			t.Fatal(err)
		}
		for flag, expected := range map[string]string{
			"--permission-mode": "dontAsk", "--tools": "Read,Glob,Grep", "--allowedTools": "Read,Glob,Grep",
			"--disallowedTools": "Bash,Edit,Write,NotebookEdit,Agent,Task", "--setting-sources": "", "--mcp-config": `{"mcpServers":{}}`,
		} {
			index := slices.Index(args, flag)
			if index < 0 || index+1 == len(args) || args[index+1] != expected {
				t.Errorf("%s missing or unexpected value: %v", flag, args)
			}
		}
		for _, flag := range []string{"--bare", "--strict-mcp-config", "--disable-slash-commands", "--no-chrome"} {
			if !slices.Contains(args, flag) {
				t.Errorf("missing %s", flag)
			}
		}
		for _, flag := range []string{"--add-dir", "--plugin-dir", "--continue"} {
			if slices.Contains(args, flag) {
				t.Errorf("unexpected %s", flag)
			}
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(args[slices.Index(args, "--settings")+1]), &settings); err != nil {
			t.Fatal(err)
		}
		if settings["disableAllHooks"] != true || settings["autoMemoryEnabled"] != false {
			t.Errorf("unsafe settings: %v", settings)
		}
		sessionFlag := "--session-id"
		if resume {
			sessionFlag = "--resume"
		}
		if !slices.Contains(args, sessionFlag) {
			t.Errorf("missing %s", sessionFlag)
		}
	}
}

func TestRestrictedReadOnlyRejectsIncompatibleCapabilities(t *testing.T) {
	tests := map[string]func(*harness.Request){
		"bypass":             func(r *harness.Request) { r.PermissionMode = "bypassPermissions" },
		"default":            func(r *harness.Request) { r.PermissionMode = "default" },
		"tool free":          func(r *harness.Request) { r.ToolsDisabled = true },
		"extraction":         func(r *harness.Request) { r.Extraction = true },
		"directory":          func(r *harness.Request) { r.AdditionalDirectories = []string{"/tmp"} },
		"plugin":             func(r *harness.Request) { r.SkillPluginDir = "/tmp/plugin" },
		"plugin digest":      func(r *harness.Request) { r.SkillPluginDigest = "digest" },
		"shell":              func(r *harness.Request) { r.AllowedTools = []string{"Bash"} },
		"shell pattern":      func(r *harness.Request) { r.AllowedTools = []string{"Bash(ls *)"} },
		"mcp wildcard":       func(r *harness.Request) { r.AllowedTools = []string{"mcp__approval__*"} },
		"read pattern":       func(r *harness.Request) { r.AllowedTools = []string{"Read(*)"} },
		"unmounted approval": func(r *harness.Request) { r.AllowedTools = []string{restrictedApprovalTool} },
		"other mcp": func(r *harness.Request) {
			r.MCPServers = []harness.MCPServer{{Name: "execution", Command: "/bin/echo"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			req := restrictedRequest(t)
			mutate(&req)
			if _, err := argumentsWithStreamEnvironment(req, nil); err == nil {
				t.Fatal("accepted incompatible restricted configuration")
			}
		})
	}
}

func TestRestrictedReadOnlyApprovalTool(t *testing.T) {
	req := restrictedRequest(t)
	req.PermissionMode = "dontAsk"
	req.MCPServers = []harness.MCPServer{{Name: "approval", Command: "/usr/local/bin/agent-host", Args: []string{"approval-mcp"}}}
	req.AllowedTools = []string{"Read", restrictedApprovalTool}
	args, err := argumentsWithStreamEnvironment(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	allowed := args[slices.Index(args, "--allowedTools")+1]
	if allowed != "Read,Glob,Grep,"+restrictedApprovalTool || strings.Contains(allowed, "*") {
		t.Fatalf("unexpected tool grants: %s", allowed)
	}
}

func TestRestrictedNativeInventoryValidation(t *testing.T) {
	for _, tool := range []string{"Bash", "Edit", "Write", "Agent", "Task", "mcp__approval__arbitrary", "mcp__other__request_approval", "FutureTool"} {
		decoder := streamDecoder{session: "s", restrictedReadOnly: true}
		raw, _ := json.Marshal(map[string]any{"type": "system", "subtype": "init", "session_id": "s", "model": "m", "tools": []string{"Read", tool}})
		if err := decoder.consume(raw); err == nil || !decoder.restrictedSetupFailed || decoder.initialized {
			t.Errorf("accepted forbidden inventory tool %s", tool)
		}
	}
	if err := validateRestrictedInventory(nil, nil); err == nil {
		t.Fatal("accepted missing native inventory")
	}
	if err := validateRestrictedInventory([]string{restrictedApprovalTool}, nil); err == nil {
		t.Fatal("accepted unmounted approval tool")
	}
	if err := validateRestrictedInventory([]string{"Read", restrictedApprovalTool}, []harness.MCPServer{{Name: "approval"}}); err != nil {
		t.Fatal(err)
	}
	if err := validateRestrictedInventory([]string{"Read"}, nil); err != nil {
		t.Fatal(err)
	}
}
