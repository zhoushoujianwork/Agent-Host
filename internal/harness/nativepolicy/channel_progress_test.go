package nativepolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestClaudeProgressResumeReceivesCurrentRunArguments(t *testing.T) {
	adapter, req := fixtureAdapter(t, "mcp_connected")
	fixtureResume(t, &req)
	path := filepath.Join(t.TempDir(), "request.json")
	fixtureSet(t, "AGENT_HOST_FIXTURE_REQUEST", path)
	req.MCPServers = []harness.MCPServer{{Name: "channel_progress", Command: "/fixture/agent-host", Args: []string{"channel-progress-mcp", "--run", "first"}, SessionKey: "conversation"}}
	for _, run := range []string{"first", "second"} {
		req.MCPServers[0].Args[2] = run
		result, err := adapter.Run(context.Background(), req, nil)
		if err != nil || result.Status != "completed" {
			t.Fatal(result, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var captured struct {
			Args []string `json:"args"`
		}
		if err = json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		idx := slices.Index(captured.Args, "--mcp-config")
		resume := slices.Index(captured.Args, "--resume")
		if idx < 0 || resume < 0 || captured.Args[resume+1] != req.NativeSessionID || !strings.Contains(captured.Args[idx+1], `"--run","`+run+`"`) {
			t.Fatal("resume lost current progress authority", captured.Args)
		}
	}
}
