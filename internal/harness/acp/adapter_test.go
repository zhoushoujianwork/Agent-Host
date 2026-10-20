package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

// The real test executable speaks ACP on stdin/stdout. This exercises process
// supervision, nested requests, replay, partial updates and terminal receipts.
func TestACPProcess(t *testing.T) {
	if os.Getenv("AGH_ACP_PROCESS") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	send := func(v any) { raw, _ := json.Marshal(v); fmt.Println(string(raw)) }
	update := func(v any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-acp-session", "update": v}})
	}
	var promptID json.RawMessage
	for scanner.Scan() {
		var p packet
		_ = json.Unmarshal(scanner.Bytes(), &p)
		result := any(map[string]any{})
		switch p.Method {
		case "initialize":
			var version = ClaudeBridgeVersion
			if os.Getenv("AGH_ACP_WRONG_VERSION") == "1" {
				version = "unknown"
			}
			result = map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "@agentclientprotocol/claude-agent-acp", "version": version}, "agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]bool{"image": true}, "sessionCapabilities": map[string]any{"additionalDirectories": map[string]any{}}}}
		case "session/new", "session/load":
			var params map[string]any
			_ = json.Unmarshal(p.Params, &params)
			_ = os.WriteFile(filepath.Join(params["cwd"].(string), "session-method"), []byte(p.Method), 0600)
			if os.Getenv("AGH_ACP_NO_INIT") != "1" {
				servers := []map[string]string{}
				for _, value := range params["mcpServers"].([]any) {
					server := value.(map[string]any)
					status := "connected"
					if os.Getenv("AGH_ACP_BAD_MCP") == "1" {
						status = "failed"
					}
					servers = append(servers, map[string]string{"name": server["name"].(string), "status": status})
				}
				send(map[string]any{"jsonrpc": "2.0", "method": "_claude/sdkMessage", "params": map[string]any{"sessionId": "native-acp-session", "message": map[string]any{"type": "system", "subtype": "init", "model": "observed-model", "mcp_servers": servers}}})
			}
			if p.Method == "session/load" {
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "OLD REPLAY MUST NOT DELIVER"}})
			}
			result = map[string]any{"sessionId": "native-acp-session", "models": map[string]string{"currentModelId": "observed-model"}, "modes": map[string]any{"availableModes": []map[string]string{{"id": "bypassPermissions"}}}}
		case "session/prompt":
			promptID = p.ID
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(p.Params, &params)
			text := params.Prompt[0].Text
			if text == "permission" {
				send(map[string]any{"jsonrpc": "2.0", "id": "permission-1", "method": "session/request_permission", "params": map[string]any{"sessionId": "native-acp-session", "options": []map[string]string{{"kind": "allow_once", "optionId": "yes"}}}})
				continue
			}
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "**First** "}})
			if text == "slow" {
				continue
			}
			if text == "crash" {
				os.Exit(3)
			}
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tool-1", "name": "Bash", "rawInput": map[string]any{}, "status": "pending"})
			update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool-1", "rawInput": map[string]string{"command": "printf ok"}, "status": "in_progress"})
			update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool-1", "status": "completed", "rawOutput": "ok"})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "answer"}})
			result = map[string]string{"stopReason": "end_turn"}
		case "session/cancel":
			send(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]string{"stopReason": "cancelled"}})
			continue
		case "":
			if string(p.ID) == `"permission-1"` {
				var permission struct {
					Outcome struct {
						Outcome string `json:"outcome"`
					} `json:"outcome"`
				}
				_ = json.Unmarshal(p.Result, &permission)
				if permission.Outcome.Outcome == "selected" {
					send(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]string{"stopReason": "end_turn"}})
				}
			}
			continue
		}
		if len(p.ID) > 0 {
			send(map[string]any{"jsonrpc": "2.0", "id": p.ID, "result": result})
		}
	}
	os.Exit(0)
}

func fixture(t *testing.T) (*Adapter, harness.Request) {
	t.Helper()
	t.Setenv("AGH_ACP_PROCESS", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Version: 1, Transport: "acp", Engine: "claude", Command: binary, Args: []string{"-test.run=^TestACPProcess$"}, AdapterVersion: ClaudeBridgeVersion}, binary), harness.Request{Workspace: root, NativeProfile: filepath.Join(root, "native"), NativeSessionID: "host-binding", Prompt: "hello", Model: "test"}
}

func TestStreamPatchesAndCompatibleResume(t *testing.T) {
	a, req := fixture(t)
	var observations []harness.Observation
	emit := func(o harness.Observation) error { observations = append(observations, o); return nil }
	result, err := a.Run(t.Context(), req, emit)
	if err != nil || result.Status != "completed" || result.Text != "**First** answer" || result.Model != "observed-model" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var events []event.Event
	for i, o := range observations {
		if o.Tool != nil {
			events = append(events, event.Event{Seq: int64(i + 1), Phase: "execute", Data: map[string]any{"tool": o.Tool}})
		}
	}
	evidence, err := state.EvidenceFromEvents(events)
	if err != nil || len(evidence) != 1 || string(evidence[0].Input) != `{"command":"printf ok"}` {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	req.Resume = true
	result, err = a.Run(t.Context(), req, emit)
	if err != nil || result.Text != "**First** answer" {
		t.Fatalf("resume=%+v err=%v", result, err)
	}
	method, _ := os.ReadFile(filepath.Join(req.Workspace, "session-method"))
	if string(method) != "session/load" {
		t.Fatalf("method=%s", method)
	}
	for _, o := range observations {
		if strings.Contains(o.Delta, "REPLAY") {
			t.Fatal("history delivered as fresh output")
		}
	}
	req.PermissionMode = "bypassPermissions"
	if _, err = a.Run(t.Context(), req, emit); err == nil {
		t.Fatal("incompatible session accepted")
	}
}

func TestCancelWaitsForACPReceiptAndRetainsPartialAnswer(t *testing.T) {
	a, req := fixture(t)
	req.Prompt = "slow"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := a.Run(ctx, req, func(o harness.Observation) error {
		if o.Type == "text_delta" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || result.Status != "canceled" || result.Text != "**First** " {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFailureNeverStartsFallbackAndCallbackFailureStopsProcess(t *testing.T) {
	for _, kind := range []string{"crash", "callback", "version"} {
		t.Run(kind, func(t *testing.T) {
			a, req := fixture(t)
			req.Prompt = kind
			if kind == "version" {
				t.Setenv("AGH_ACP_WRONG_VERSION", "1")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := a.Run(ctx, req, func(o harness.Observation) error {
				if kind == "callback" && o.Type == "text_delta" {
					return errors.New("journal unavailable")
				}
				return nil
			})
			if err == nil {
				t.Fatal("failure was hidden")
			}
		})
	}
}

func TestPermissionPolicyIsExplicit(t *testing.T) {
	for _, mode := range []string{"default", "bypassPermissions"} {
		t.Run(mode, func(t *testing.T) {
			a, req := fixture(t)
			req.Prompt = "permission"
			req.PermissionMode = mode
			result, err := a.Run(t.Context(), req, nil)
			if mode == "default" && err == nil {
				t.Fatal("unsupported approval silently allowed")
			}
			if mode == "bypassPermissions" && (err != nil || result.Status != "completed") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestUnsupportedConstraintsFailBeforeStartingACP(t *testing.T) {
	a, req := fixture(t)
	req.RestrictedReadOnly = true
	if _, err := a.Run(t.Context(), req, nil); err == nil {
		t.Fatal("restricted policy ignored")
	}
	if _, err := os.Stat(filepath.Join(req.Workspace, "session-method")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("agent started before policy check")
	}
}

func TestNativeMCPAdmissionCannotDisappearThroughACP(t *testing.T) {
	for _, kind := range []string{"missing-init", "failed-mcp"} {
		t.Run(kind, func(t *testing.T) {
			a, req := fixture(t)
			if kind == "missing-init" {
				t.Setenv("AGH_ACP_NO_INIT", "1")
			} else {
				t.Setenv("AGH_ACP_BAD_MCP", "1")
				req.MCPServers = []harness.MCPServer{{Name: "required", Command: "/bin/false"}}
			}
			result, err := a.Run(t.Context(), req, nil)
			if err == nil || result.Text != "" {
				t.Fatalf("missing admission accepted: %+v %v", result, err)
			}
		})
	}
}
