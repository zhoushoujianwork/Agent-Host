package cli

import (
	"io"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/host"
)

func TestNonOwnerDebugIsExplicitChatOnlyAndSeparatesAudience(t *testing.T) {
	o, _, err := parse("chat", []string{"--debug-non-owner", "-m", "inspect"}, io.Discard)
	if err != nil || !o.debugNonOwner || o.audienceID != "debug-non-owner" {
		t.Fatal("debug parse failed", err)
	}
	applyNonOwnerDebugPolicy(&o)
	if o.permissionMode != "dontAsk" || !strings.Contains(o.hostInstructions, `"is_owner":false`) || !strings.Contains(o.hostInstructions, "no approval delivery transport") {
		t.Fatal("debug authority incorrect")
	}
	h := &host.Host{Config: host.Config{MCPServers: []harness.MCPServer{{Name: "write"}}, SkillsPreselected: true, ExpectedSkillDigest: "stale", HistoryHome: "private", ContextHome: "private"}}
	configureNonOwnerDebugHost(h)
	if !h.Config.RestrictedReadOnly || len(h.Config.MCPServers) != 0 || h.Config.SkillsPreselected || h.Config.HistoryHome != "" || h.Config.ContextHome != "" || h.Config.RequestApproval != nil {
		t.Fatal("debug capabilities expanded", h.Config)
	}
	normal, _, err := parse("chat", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	applyNonOwnerDebugPolicy(&normal)
	if normal.debugNonOwner || normal.permissionMode != "" || normal.hostInstructions != "" || normal.audienceID != "owner" {
		t.Fatal("ordinary chat policy changed")
	}
	for _, args := range [][]string{{"--debug-non-owner", "--no-tools"}, {"--debug-non-owner", "--allowed-tools", "Bash"}} {
		if _, _, err := parse("chat", args, io.Discard); err == nil {
			t.Fatal("conflicting debug flags accepted", args)
		}
	}
	if _, _, err := parse("dingtalk serve", []string{"--debug-non-owner"}, io.Discard); err == nil {
		t.Fatal("channel accepted unauthenticated debug authority flag")
	}
}
