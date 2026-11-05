package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"agent-host/internal/channelprogress"
	"agent-host/internal/state"
)

func TestChannelProgressPrivateCLIAndLaunchBinding(t *testing.T) {
	home, workspace, message, _ := historyCLIFixture(t)
	s, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.ChannelMessage(t.Context(), message)
	if err != nil {
		t.Fatal(err)
	}
	session := state.Session{ID: "progress-session", Scope: workspace, Workspace: workspace, RuntimeFingerprint: m.RuntimeFingerprint, NativeSessionID: "native-progress"}
	if err = s.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	if err = s.BindChannelSession(t.Context(), m.RouteBinding, session.ID); err != nil {
		t.Fatal(err)
	}
	r := state.Run{ID: "progress-run", SessionID: session.ID, Scope: workspace, Workspace: workspace, RuntimeFingerprint: session.RuntimeFingerprint, NativeSessionID: session.NativeSessionID, Input: m.Text}
	if err = s.CreateChannelRun(t.Context(), r, message); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableChannelProgress(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	binding, err := channelprogress.Resolve(home, message, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	args := binding.Server("/fixture/agh").Args
	var input, output bytes.Buffer
	for _, req := range []any{historyRequest(1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{}}), historyRequest(2, "tools/list", map[string]any{})} {
		if err = json.NewEncoder(&input).Encode(req); err != nil {
			t.Fatal(err)
		}
	}
	if err = Run(t.Context(), args, &input, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"name":"send_progress"`) || strings.Contains(output.String(), "search_conversation_history") {
		t.Fatal(output.String())
	}
	args[len(args)-1] = "stale"
	if err = Run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("stale fingerprint accepted")
	}
	for _, bad := range [][]string{{"channel-progress-mcp"}, {"channel-progress-mcp", "--home", "relative", "--message", message, "--run", r.ID, "--fingerprint", "x"}} {
		if err = Run(t.Context(), bad, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatal("invalid launch accepted", bad)
		}
	}
}

func TestChannelProgressPrivateCLIParseErrorsDoNotEchoSecrets(t *testing.T) {
	const secret = "credential-sentinel-91835"
	for _, args := range [][]string{{"--" + secret + "=value"}, {"--unknown", secret}, {"--home"}, {"--help"}} {
		var stderr, stdout bytes.Buffer
		err := runChannelProgressMCP(t.Context(), args, strings.NewReader(""), &stdout, &stderr)
		if err == nil || err.Error() != "invalid channel-progress-mcp arguments" || stderr.Len() != 0 || stdout.Len() != 0 {
			t.Fatalf("parse leaked input or returned an unstable error: err=%v stderr=%q stdout=%q", err, stderr.String(), stdout.String())
		}
	}
}
