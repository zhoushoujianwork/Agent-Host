package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/history"
	"agent-host/internal/state"
)

// historyCLIFixture retains synthetic source evidence without any external chat
// access. The current receipt is claimed and routed but has not been executed.
func historyCLIFixture(t *testing.T) (home, workspace, currentReceiptID, marker string) {
	t.Helper()
	home = t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker = "orbit-731"
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	old := state.ChannelMessage{ID: "history-old", Channel: "dingtalk", AccountID: "app", TenantID: "tenant",
		MessageID: "provider-history-old", ConversationID: "dm-owner", ConversationType: "1", SenderID: "owner",
		Profile: "fixture", Binding: "history-conversation", Scope: workspace, MessageType: "text",
		Text: "Record the deployment decision.", ReceivedAt: time.Now().Add(-time.Hour), ReplyExpiresAt: time.Now().Add(time.Hour)}
	if inserted, err := store.EnqueueChannelMessage(ctx, old, "synthetic-private-reply"); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	if _, err := store.ClaimNextChannelMessage(ctx, old.Profile); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveChannelRoute(ctx, old.ID, "history-old-route", "", workspace, workspace, "history-fixture-runtime"); err != nil {
		t.Fatal(err)
	}
	session := state.Session{ID: "history-old-session", Scope: workspace, Workspace: workspace, Model: "fixture", RuntimeFingerprint: "history-fixture-runtime", NativeSessionID: "history-old-native"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.BindChannelSession(ctx, "history-old-route", session.ID); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "history-old-run", SessionID: session.ID, Scope: workspace, Workspace: workspace,
		RuntimeFingerprint: session.RuntimeFingerprint, RequestedModel: "fixture", NativeSessionID: session.NativeSessionID, Input: old.Text}
	if err := store.CreateChannelRun(ctx, run, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, run.ID, "completed", "Unformatted native answer that must not become the channel history reply.", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishChannelMessage(ctx, old.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareChannelReply(ctx, old.ID, "The saved deployment marker is "+marker+"."); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishChannelReply(ctx, old.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	current := old
	current.ID, current.MessageID = "history-current", "provider-history-current"
	current.Text, current.ReceivedAt = "Find the earlier deployment decision.", time.Now()
	if inserted, err := store.EnqueueChannelMessage(ctx, current, "synthetic-private-current-reply"); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	if _, err := store.ClaimNextChannelMessage(ctx, current.Profile); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveChannelRoute(ctx, current.ID, "history-current-route", "", workspace, workspace, "history-fixture-runtime"); err != nil {
		t.Fatal(err)
	}
	return home, workspace, current.ID, marker
}

func historyRPC(t *testing.T, args []string, requests ...any) ([]map[string]json.RawMessage, error) {
	t.Helper()
	var in, out bytes.Buffer
	for _, req := range requests {
		if err := json.NewEncoder(&in).Encode(req); err != nil {
			t.Fatal(err)
		}
	}
	err := Run(t.Context(), append([]string{"history-mcp"}, args...), &in, &out, io.Discard)
	var responses []map[string]json.RawMessage
	decoder := json.NewDecoder(&out)
	for {
		var response map[string]json.RawMessage
		decodeErr := decoder.Decode(&response)
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil {
			t.Fatal("invalid history MCP response", decodeErr)
		}
		responses = append(responses, response)
	}
	return responses, err
}

func historyRequest(id int, method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func TestHistoryMCPHiddenCLIInitializesAndRetrievesSavedChannelEvidence(t *testing.T) {
	home, _, current, marker := historyCLIFixture(t)
	binding, err := history.Resolve(home, current)
	if err != nil {
		t.Fatal(err)
	}
	args := binding.Server("/fixture/agent-host").Args[1:]
	responses, err := historyRPC(t, args,
		historyRequest(1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}}),
		historyRequest(2, "tools/list", map[string]any{}),
		historyRequest(3, "tools/call", map[string]any{"name": "search_conversation_history", "arguments": map[string]any{"query": "deployment"}}),
		historyRequest(4, "tools/call", map[string]any{"name": "read_conversation_history", "arguments": map[string]any{"reference": "host-history:history-old"}}),
	)
	if err != nil || len(responses) != 4 {
		t.Fatal("history MCP exchange failed", len(responses), err)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(responses[0]["result"], &initialized); err != nil || initialized.ProtocolVersion == "" {
		t.Fatal("history MCP did not initialize", err)
	}
	var catalog struct {
		Tools []struct {
			Name        string          `json:"name"`
			Annotations map[string]bool `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(responses[1]["result"], &catalog); err != nil || len(catalog.Tools) != 2 {
		t.Fatal("incorrect history tool catalog", err)
	}
	names := map[string]bool{}
	for _, tool := range catalog.Tools {
		names[tool.Name] = true
		if !tool.Annotations["readOnlyHint"] || tool.Annotations["destructiveHint"] {
			t.Fatal("history tool is not read-only", tool.Name)
		}
	}
	if !names["search_conversation_history"] || !names["read_conversation_history"] {
		t.Fatal("history operations missing", names)
	}
	for _, response := range responses[2:] {
		var result struct {
			IsError bool                            `json:"isError"`
			Data    state.ConversationHistoryResult `json:"structuredContent"`
		}
		if err := json.Unmarshal(response["result"], &result); err != nil || result.IsError || len(response["error"]) > 0 || len(result.Data.Turns) != 1 {
			t.Fatal("history tool did not retrieve its authorized turn", result, err)
		}
		turn := result.Data.Turns[0]
		if turn.ReceiptID != "history-old" || turn.AuthorID != "owner" || turn.DeliveryStatus != "accepted" || !strings.Contains(turn.Reply.Text, marker) || !result.Data.Coverage.CurrentExcluded {
			t.Fatal("history source/reply/coverage changed", turn, result.Data.Coverage)
		}
		for _, private := range []string{"synthetic-private-reply", "synthetic-private-current-reply", "Unformatted native answer"} {
			if strings.Contains(string(response["result"]), private) {
				t.Fatal("history exposed transport state or a different answer", private)
			}
		}
	}
}

func TestHistoryMCPRejectsStaleAssemblyAndIdentityArguments(t *testing.T) {
	home, _, current, _ := historyCLIFixture(t)
	binding, err := history.Resolve(home, current)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--home", home, "--message", current, "--fingerprint", "stale-" + binding.Fingerprint}
	responses, err := historyRPC(t, args, historyRequest(1, "tools/list", map[string]any{}))
	if err == nil || !strings.Contains(err.Error(), "access changed") || len(responses) != 0 {
		t.Fatal("stale history binding opened an MCP session", responses, err)
	}
	args = binding.Server("/fixture/agent-host").Args[1:]
	responses, err = historyRPC(t, args,
		historyRequest(1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}}),
		historyRequest(2, "tools/call", map[string]any{
			"name": "search_conversation_history", "arguments": map[string]any{"conversation_id": "other-private-conversation"},
		}))
	if err != nil || len(responses) != 2 || !strings.Contains(string(responses[1]["result"]), `"isError":true`) || strings.Contains(string(responses[1]["result"]), "orbit-731") {
		t.Fatal("model-supplied identity widened history access", responses, err)
	}
}

func TestHistoryPolicyRotatesRuntimeOnceAndToolFreeAssemblyOmitsIt(t *testing.T) {
	_, workspace, _, _ := historyCLIFixture(t)
	o := options{workspace: workspace, scope: workspace, binary: "/fixture/claude", model: "fixture"}
	before := assemblyConfig(o)
	o.historyEnabled = true
	enabled := assemblyConfig(o)
	if before.ConversationHistoryPolicy != "" || enabled.ConversationHistoryPolicy != history.PolicyVersion {
		t.Fatal("history policy is missing or varies between turns")
	}
	assemble := func(config assembly.Configuration) assembly.Runtime {
		t.Helper()
		runtime, err := assembly.Assemble("", "", workspace, workspace, config)
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	old, current := assemble(before), assemble(enabled)
	if old.Fingerprint == current.Fingerprint || assemble(assemblyConfig(o)).Fingerprint != current.Fingerprint {
		t.Fatal("history access policy did not rotate once into a stable runtime")
	}
	o.noTools = true
	if assemblyConfig(o).ConversationHistoryPolicy != "" {
		t.Fatal("tool-free runtime retained history access policy")
	}
}
