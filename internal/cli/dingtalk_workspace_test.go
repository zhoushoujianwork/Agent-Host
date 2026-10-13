package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/dingtalkworkspace"
)

func TestDingTalkWorkspaceMountOwnerAndCoexistence(t *testing.T) {
	path, _, bot := writeBotFixture(t)
	workspace := filepath.Join(filepath.Dir(path), "data-dingtalk-workspace.json")
	o := options{dingtalkConfig: path, audienceID: "owner"}
	if err := resolveKnowledge(&o); err != nil {
		t.Fatal(err)
	}
	if len(o.mcpServers) != 1 || o.mcpServers[0].Name != "dingtalk" {
		t.Fatal("channel did not mount workspace", o.mcpServers)
	}
	unconfigured := o.dingtalkWorkspaceFingerprint
	c := dingtalkworkspace.Config{Profile: bot.CorpID + ":" + bot.OwnerUserID, StateDirectory: filepath.Join(filepath.Dir(path), "workspace-state"), Services: []string{"contact", "calendar", "todo"}}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(workspace, raw, 0600); err != nil {
		t.Fatal(err)
	}
	o.emailConfig = emailFixture(t)
	if err := resolveKnowledge(&o); err != nil {
		t.Fatal(err)
	}
	if len(o.mcpServers) != 2 || o.mcpServers[0].Name != "email" || o.mcpServers[1].Name != "dingtalk" {
		t.Fatal("workspace displaced email", o.mcpServers)
	}
	if unconfigured == o.dingtalkWorkspaceFingerprint || assemblyConfig(o).DingTalkFingerprint != o.dingtalkWorkspaceFingerprint {
		t.Fatal("runtime failed to bind workspace policy")
	}
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 2 {
		t.Fatal("repeated assembly duplicated MCP", err)
	}
	o.audienceID = "group:one"
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 || o.dingtalkWorkspaceFingerprint != "" {
		t.Fatal("owner capabilities exposed to group", err)
	}
	o.audienceID = "owner"
	o.noTools = true
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 {
		t.Fatal("tool-free execution mounted workspace", err)
	}
	o.noTools = false
	c.Profile = "other:owner"
	raw, _ = json.Marshal(c)
	os.WriteFile(workspace, raw, 0600)
	if err := resolveKnowledge(&o); err == nil {
		t.Fatal("another bot owner's credential selected")
	}
}

func TestDingTalkWorkspaceLocalCheck(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspace.json")
	raw, _ := json.Marshal(dingtalkworkspace.Config{Profile: "corp:owner", StateDirectory: filepath.Join(root, "state"), Services: []string{"contact"}})
	os.WriteFile(path, raw, 0600)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"dingtalk-workspace", "check", "--config", path}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"authentication_verified":false`) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("local check created credential state")
	}
	o, _, err := parse("chat", []string{"--dingtalk-workspace-config", path}, io.Discard)
	if err != nil || o.dingtalkWorkspaceConfig != path {
		t.Fatal("workspace flag failed", err)
	}
}
