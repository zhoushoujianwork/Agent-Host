package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func historyFixture(t *testing.T, count int, quote bool) (string, Binding) {
	t.Helper()
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i <= count; i++ {
		id := fmt.Sprintf("receipt-%d", i)
		m := state.ChannelMessage{ID: id, Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "native-" + id,
			ConversationID: "owner-dm", ConversationType: "1", SenderID: "owner", Profile: "test", Binding: "owner-binding", Scope: "default-scope",
			MessageType: "text", Text: "Earlier deployment discussion", ReceivedAt: time.Now().UTC(), ReplyExpiresAt: time.Now().Add(time.Hour).UTC()}
		if quote {
			m.Text = strings.Repeat("中", 500)
			m.Input = &state.ChannelInput{Quote: &state.ChannelQuote{Body: strings.Repeat("中", 500), Title: strings.Repeat("中", 500)}}
		}
		if _, err := store.EnqueueChannelMessage(context.Background(), m, "https://example.invalid/?secret=capability"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimNextChannelMessage(context.Background(), m.Profile); err != nil {
			t.Fatal(err)
		}
		session := state.Session{ID: "session-" + id, Scope: m.Scope, NativeSessionID: "native-session-" + id}
		if err := store.CreateSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		if err := store.BindChannelSession(context.Background(), m.Binding, session.ID); err != nil {
			t.Fatal(err)
		}
		run := state.Run{ID: "run-" + id, SessionID: session.ID, Scope: m.Scope, Input: m.Text, NativeSessionID: session.NativeSessionID}
		if err := store.CreateChannelRun(context.Background(), run, id); err != nil {
			t.Fatal(err)
		}
		if i != count {
			if err := store.CompleteRun(context.Background(), run.ID, "completed", "unformatted native output", "test", ""); err != nil {
				t.Fatal(err)
			}
			if err := store.FinishChannelMessage(context.Background(), id, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if err := store.PrepareChannelReply(context.Background(), id, "Formatted **deployment** answer"); err != nil {
				t.Fatal(err)
			}
			if err := store.FinishChannelReply(context.Background(), id, "unknown", "transport detail omitted"); err != nil {
				t.Fatal(err)
			}
		}
	}
	binding, err := Resolve(home, fmt.Sprintf("receipt-%d", count))
	if err != nil {
		t.Fatal(err)
	}
	return home, binding
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`

func serveLines(t *testing.T, binding Binding, lines ...string) []map[string]json.RawMessage {
	t.Helper()
	var output bytes.Buffer
	if err := Serve(context.Background(), binding, strings.NewReader(strings.Join(lines, "\n")+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]json.RawMessage
	decoder := json.NewDecoder(&output)
	for decoder.More() {
		var response map[string]json.RawMessage
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	return responses
}

func TestHistoryMCPListsOnlyBuiltInReadToolsAndReadsSavedTurn(t *testing.T) {
	_, binding := historyFixture(t, 1, false)
	server := binding.Server("/usr/bin/agent-host")
	if server.Name != "conversation_history" || len(server.Args) != 7 || server.Args[0] != "history-mcp" || server.Args[3] != "--message" || server.Args[4] != "receipt-1" || server.Args[6] != binding.Fingerprint {
		t.Fatal("hidden MCP launch shape changed", server)
	}
	responses := serveLines(t, binding, initialize,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"query":"deployment"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_conversation_history","arguments":{"reference":"host-history:receipt-0"}}}`)
	if len(responses) != 4 {
		t.Fatal(responses)
	}
	var listed struct {
		Tools []tool `json:"tools"`
	}
	if json.Unmarshal(responses[1]["result"], &listed) != nil || len(listed.Tools) != 2 {
		t.Fatal("unexpected tools", responses[1])
	}
	for _, tool := range listed.Tools {
		if !tool.Annotations["readOnlyHint"] || tool.Annotations["destructiveHint"] || tool.InputSchema["additionalProperties"] != false {
			t.Fatal("history tool contract permits writes or widened identities", tool)
		}
	}
	for _, response := range responses[2:] {
		var result struct {
			Structured state.ConversationHistoryResult `json:"structuredContent"`
			IsError    bool                            `json:"isError"`
		}
		if json.Unmarshal(response["result"], &result) != nil || result.IsError || len(result.Structured.Turns) != 1 || result.Structured.Turns[0].Reply.Text != "Formatted **deployment** answer" || result.Structured.Turns[0].DeliveryStatus != "unknown" {
			t.Fatal("MCP did not retrieve exact saved channel evidence", string(response["result"]))
		}
		for _, secret := range []string{"capability", "unformatted", "transport detail", "reply_url", "workspace"} {
			if strings.Contains(string(response["result"]), secret) {
				t.Fatal("MCP leaked administrative payload", secret)
			}
		}
	}
}

func TestHistoryMCPRejectsAmbiguousArgumentsAndUninitializedCalls(t *testing.T) {
	_, binding := historyFixture(t, 1, false)
	responses := serveLines(t, binding,
		`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`,
		initialize,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"account_id":"another"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"query":"x","query":"deployment"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"Query":"deployment"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"limit":null}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"after":"yesterday"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read_conversation_history","arguments":{"reference":"host-history:receipt-1"}}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"search_conversation_history","arguments":{}}}`)
	if len(responses) != 8 || responses[0]["error"] == nil {
		t.Fatal("uninitialized call or notification protocol changed", responses)
	}
	for _, response := range responses[2:] {
		if response["error"] != nil {
			continue
		}
		var result struct {
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(response["result"], &result) != nil || !result.IsError {
			t.Fatal("ambiguous or widened call accepted", response)
		}
	}
	bad := binding
	bad.Fingerprint = strings.Repeat("0", 64)
	if err := Serve(context.Background(), bad, strings.NewReader(initialize), &bytes.Buffer{}); err == nil {
		t.Fatal("unpinned MCP launch accepted")
	}
}

func TestHistoryMCPEnforcesEncodedOutputLimit(t *testing.T) {
	_, binding := historyFixture(t, 20, true)
	responses := serveLines(t, binding, initialize,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_conversation_history","arguments":{"limit":20}}}`)
	var result struct {
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(responses[1]["result"], &result) != nil || !result.IsError || len(responses[1]["result"]) > 64<<10 {
		t.Fatal("oversized encoded history escaped output bound", len(responses[1]["result"]), string(responses[1]["result"]))
	}
}

func TestReadRejectsPlatformQuoteIDWithActionableError(t *testing.T) {
	home, binding := historyFixture(t, 1, true)
	store, err := state.Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = call(t.Context(), store, binding.receipt, "read_conversation_history", json.RawMessage(`{"reference":"msgSP6lAI7rQsjfw8hIxgZfmA=="}`))
	if err == nil || !strings.Contains(err.Error(), "search_conversation_history") || strings.Contains(err.Error(), "bounds") {
		t.Fatal("platform quote ID must explain the reference mismatch", err)
	}
	result, err := call(t.Context(), store, binding.receipt, "read_conversation_history", json.RawMessage(`{"reference":"host-history:receipt-0"}`))
	if err != nil || len(result.Turns) != 1 {
		t.Fatal("search references must remain readable", result, err)
	}
}
