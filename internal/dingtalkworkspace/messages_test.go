package dingtalkworkspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Opt-in reads one explicitly selected real forwarded copy with the embedded
// worker and configured owner's session. It sends no message or approval action.
func TestForwardedCardReadLive(t *testing.T) {
	path := os.Getenv("AGENT_HOST_CARD_READ_CONFIG")
	if path == "" {
		t.Skip("requires explicit real card read opt-in")
	}
	c, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: Process(os.Getenv("AGENT_HOST_CARD_READ_BINARY"))}
	body, err := s.ReadForwardedCard(t.Context(), os.Getenv("AGENT_HOST_CARD_READ_CONVERSATION"), os.Getenv("AGENT_HOST_CARD_READ_MESSAGE"))
	if err != nil || !strings.Contains(body, "4C16G") {
		t.Fatalf("exact real card read failed: %v", err)
	}
	t.Logf("Exact forwarded approval text read through embedded owner workspace (%d characters); no delivery or approval action", len([]rune(body)))
}

func TestForwardedCardReadRequiresExactMessageAndConversation(t *testing.T) {
	path, c, _ := configuration(t)
	c.Services = append(c.Services, "chat")
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	_, fp, _ := Load(path)
	executable, _ := os.Executable()
	contract, err := Process(executable)(context.Background(), Request{Config: path, Fingerprint: fp, Args: []string{"schema", "--cli-path", "chat message list-by-ids", "--compact", "--format", "json"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, conversation, content string
		want                      bool
	}{
		{"source", "conversation", "Approval preview", true},
		{"other", "conversation", "Wrong message", false},
		{"source", "other", "Wrong conversation", false},
		{"source", "conversation", "", false},
	} {
		run := func(_ context.Context, r Request) (json.RawMessage, error) {
			if r.Args[0] == "schema" {
				return contract, nil
			}
			if r.Args[0] == "profile" {
				return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
			}
			if !strings.Contains(strings.Join(r.Args, " "), "--msg-ids=source --profile corp:owner") {
				t.Fatal("unbounded or unbound card read", r.Args)
			}
			body, _ := json.Marshal(map[string]any{"success": true, "result": map[string]any{"messages": []any{map[string]string{"openMessageId": tc.id, "openConversationId": tc.conversation, "content": tc.content}}}})
			return body, nil
		}
		text, err := (Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: run}).ReadForwardedCard(context.Background(), "conversation", "source")
		if (err == nil) != tc.want || tc.want && text != tc.content {
			t.Fatal("wrong card lookup result", text, err)
		}
	}
}

func TestPersonalMessagingUsesOfficialContractsAndOwnerIdentity(t *testing.T) {
	path, c, _ := configuration(t)
	c.Services = append(c.Services, "chat")
	b, _ := json.Marshal(c)
	os.WriteFile(path, b, 0600)
	_, fp, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	official := Process(executable)
	contracts := map[string]json.RawMessage{}
	for _, pathName := range []string{"chat message send", "chat message query-send-status", "chat +chat-messages", "chat search"} {
		raw, e := official(context.Background(), Request{Config: path, Fingerprint: fp, Args: []string{"schema", "--cli-path", pathName, "--compact", "--format", "json"}})
		if e != nil {
			t.Fatal("official messaging contract unavailable", pathName, e)
		}
		contracts[pathName] = raw
	}
	sends, reads, statusReads := 0, 0, 0
	run := func(ctx context.Context, r Request) (json.RawMessage, error) {
		if r.Args[0] == "schema" {
			return contracts[r.Args[2]], nil
		}
		if r.Args[0] == "profile" {
			return json.RawMessage(`{"success":true,"profiles":[{"profile":"corp:owner","corpId":"corp","userId":"owner"}]}`), nil
		}
		joined := strings.Join(r.Args, " ")
		if !strings.Contains(joined, "--profile corp:owner") {
			t.Fatal("owner identity not fixed", joined)
		}
		if r.Args[1] == "+chat-messages" {
			reads++
			for _, part := range []string{"--page-limit=1", "--max-items=10", "--no-reactions=true", "--open-dingtalk-id=peer-id"} {
				if !strings.Contains(joined, part) {
					t.Fatal("read bounds/selector lost", joined)
				}
			}
			return json.RawMessage(`{"contractVersion":"im.message-list.v1","complete":false,"partial":true,"hasMore":true,"nextPage":{"time":123,"direction":"newer"},"messages":[{"messageId":"source-id","senderId":"peer","text":"original message","createTime":123}]}`), nil
		}
		if r.Args[2] == "query-send-status" {
			statusReads++
			return json.RawMessage(`{"ok":true,"outcome":"success","data":{"status":"delivered","openMessageId":"sent-id"}}`), nil
		}
		sends++
		for _, part := range []string{"--content=Requested message", "--open-dingtalk-id=peer-id", "--idempotency-key=", "--yes"} {
			if !strings.Contains(joined, part) {
				t.Fatal("send contract lost", joined)
			}
		}
		return json.RawMessage(`{"ok":true,"outcome":"pending","data":{"openTaskId":"send-task"}}`), nil
	}
	s := Service{Path: path, Fingerprint: fp, ExpectedProfile: c.Profile, Run: run}
	request := json.RawMessage(`{"operation_id":"personal-send","peer_open_dingtalk_id":"peer-id","text":"Requested message"}`)
	value, err := s.Call(context.Background(), "send_message", request)
	if err != nil || value.(map[string]any)["status"] != "provider_pending" {
		t.Fatal("asynchronous send claimed delivery", value, err)
	}
	if _, err = s.Call(context.Background(), "send_message", request); err != nil || sends != 1 {
		t.Fatal("personal message replayed", err, sends)
	}
	value, err = s.Call(context.Background(), "get_send_status", json.RawMessage(`{"open_task_id":"send-task"}`))
	if err != nil || statusReads != 1 || !strings.Contains(string(value.(json.RawMessage)), "sent-id") {
		t.Fatal("delivery query failed", value, err)
	}
	value, err = s.Call(context.Background(), "read_messages", json.RawMessage(`{"peer_open_dingtalk_id":"peer-id","after":"2026-10-13T00:00:00Z","before":"2026-10-13T01:00:00Z"}`))
	if err != nil || reads != 1 || !strings.Contains(string(value.(json.RawMessage)), `"complete":false`) || !strings.Contains(string(value.(json.RawMessage)), "source-id") {
		t.Fatal("partial original evidence lost", value, err)
	}
	if _, err = s.Call(context.Background(), "send_message", json.RawMessage(`{"operation_id":"bad","conversation_id":"group","peer_open_dingtalk_id":"peer","text":"invalid"}`)); err == nil || sends != 1 {
		t.Fatal("ambiguous target sent")
	}
	if _, err = s.Call(context.Background(), "read_messages", json.RawMessage(`{"conversation_id":"group","after":"2026-10-01T00:00:00Z","before":"2026-10-13T00:00:00Z"}`)); err == nil || reads != 1 {
		t.Fatal("unbounded read executed")
	}
	if _, err = commandPath(c, "chat group destroy"); err == nil {
		t.Fatal("chat management became available")
	}
}

func TestGenericMessagingCannotBypassReadBoundsOrSenderPolicy(t *testing.T) {
	c := Config{Profile: "corp:owner"}
	a := Args{Command: "chat +chat-messages", Parameters: map[string]string{"conversation-id": "verified-group", "start": "2026-10-13T00:00:00Z", "end": "2026-10-13T01:00:00Z", "max-items": "20", "page-limit": "50", "no-reactions": "false"}}
	if err := enforceMessaging(c, &a); err != nil {
		t.Fatal(err)
	}
	if a.Parameters["page-limit"] != "1" || a.Parameters["no-reactions"] != "true" {
		t.Fatal("generic route widened collection")
	}
	a.Parameters["download-resources"] = "true"
	if err := enforceMessaging(c, &a); err != nil || a.Parameters["output-dir"] != "downloads" {
		t.Fatal("explicit downloads unavailable", err)
	}
	a.Parameters["download-resources"] = "false"
	if err := enforceMessaging(c, &a); err == nil {
		t.Fatal("output destination accepted without downloading")
	}
	a = Args{Command: "chat message send", OperationID: "send", Parameters: map[string]string{"open-dingtalk-id": "verified-peer", "content": "Requested", "ai-tag": "false"}}
	if err := enforceMessaging(c, &a); err == nil {
		t.Fatal("generic route disabled official identity marking")
	}
	delete(a.Parameters, "ai-tag")
	if err := enforceMessaging(c, &a); err != nil {
		t.Fatal(err)
	}
	first := a.Parameters["idempotency-key"]
	a.Parameters["idempotency-key"] = "caller-override"
	if err := enforceMessaging(c, &a); err != nil || a.Parameters["idempotency-key"] != first {
		t.Fatal("caller overrode deduplication UUID", err)
	}
}
