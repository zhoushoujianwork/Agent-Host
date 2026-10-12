package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/harness/claude"
	"agent-host/internal/history"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

// This opt-in acceptance uses synthetic private state and the real native
// harness. It never reads a live DingTalk/DWS account or sends a message.
func TestNativeConversationHistoryLive(t *testing.T) {
	if os.Getenv("AGENT_HOST_LIVE_HISTORY") != "1" {
		t.Skip("set AGENT_HOST_LIVE_HISTORY=1 for isolated native MCP acceptance")
	}
	home, workspace, current, marker := historyCLIFixture(t)
	capability, err := history.Resolve(home, current)
	if err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("AGENT_HOST_LIVE_BINARY")
	if binary == "" {
		binary, err = filepath.Abs("../../bin/agent-host")
		if err != nil {
			t.Fatal(err)
		}
	}
	native := os.Getenv("AGENT_HOST_LIVE_CLAUDE")
	if native == "" {
		native = "claude"
	}
	native, err = exec.LookPath(native)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("AGENT_HOST_LIVE_MODEL")
	if model == "" {
		model = "sonnet"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	started := time.Now()
	nativeSessionID := host.ID()
	toolNames := []string{"mcp__conversation_history__search_conversation_history", "mcp__conversation_history__read_conversation_history"}
	// Successful tool pairs must contain the expected stored receipt, rather
	// than merely complete a tool operation or repeat text from native history.
	runNative := func(capability history.Binding, expectedReceipt, excludedReceipt, prompt string, resume bool) harness.Result {
		t.Helper()
		paired := map[string]bool{}
		calls := map[string]string{}
		result, err := claude.New(native).Run(ctx, harness.Request{
			Workspace: workspace, Model: model, NativeSessionID: nativeSessionID, Resume: resume,
			HostInstructions: history.Instructions, Prompt: prompt,
			MCPServers: []harness.MCPServer{capability.Server(binary)}, AllowedTools: toolNames,
		}, func(o harness.Observation) error {
			if o.Tool == nil {
				return nil
			}
			if o.Tool.Stage == "call" {
				if o.Tool.Name != toolNames[0] && o.Tool.Name != toolNames[1] {
					return fmt.Errorf("native history acceptance attempted unexpected tool %s", o.Tool.Name)
				}
				calls[o.Tool.ID] = o.Tool.Name
			} else if o.Tool.Stage == "result" && !o.Tool.IsError {
				name := calls[o.Tool.ID]
				if name == "" {
					return errors.New("native history result is not paired with this invocation's call")
				}
				data, err := nativeHistoryToolResult(o.Tool.Result)
				if err != nil {
					return err
				}
				if !data.Coverage.CurrentExcluded {
					return errors.New("native history result omitted current-request exclusion evidence")
				}
				for _, turn := range data.Turns {
					if turn.ReceiptID == excludedReceipt {
						return errors.New("native history result exposed the current receipt")
					}
					if turn.ReceiptID == expectedReceipt {
						paired[name] = true
					}
				}
			}
			return nil
		})
		if err != nil || result.Status != "completed" || result.NativeSessionID != nativeSessionID {
			t.Fatalf("native history acceptance failed: resume=%v status=%s session=%s error=%v", resume, result.Status, result.NativeSessionID, err)
		}
		for _, name := range toolNames {
			if !paired[name] {
				t.Fatalf("native resume=%v did not retrieve receipt %s through required history operation %s", resume, expectedReceipt, name)
			}
		}
		return result
	}
	result := runNative(capability, "history-old", current,
		"Use the conversation_history MCP to search the earlier conversation (empty keyword is permitted), then read the returned matching reference. You must call BOTH search_conversation_history and read_conversation_history. Report the exact previously saved assistant reply and its delivery status. Use only these history tools; do not use filesystem or shell tools, and do not invent missing material.", false)
	if !strings.Contains(result.Text, marker) || !strings.Contains(strings.ToLower(result.Text), "accepted") {
		t.Fatal("native answer did not recover the saved reply and accepted delivery state")
	}
	// Admit another synthetic request after the first native invocation. The
	// former current receipt now becomes authorized earlier history. Reusing
	// old MCP argv on --resume would retain its earlier snapshot and hide it.
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	formerCurrent, err := store.ChannelMessage(ctx, current)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	newCurrent := formerCurrent
	newCurrent.ID, newCurrent.MessageID = "history-resume-current", "provider-history-resume-current"
	newCurrent.Text, newCurrent.ReceivedAt = "Check the refreshed conversation history snapshot.", time.Now().UTC()
	if inserted, err := store.EnqueueChannelMessage(ctx, newCurrent, "synthetic-private-resume-reply"); err != nil || !inserted {
		store.Close()
		t.Fatal("enqueue native-resume receipt", inserted, err)
	}
	claimed, err := store.ClaimNextChannelMessage(ctx, newCurrent.Profile)
	if err != nil || claimed.ID != newCurrent.ID {
		store.Close()
		t.Fatal("claim native-resume receipt", claimed.ID, err)
	}
	if err := store.ResolveChannelProjects(ctx, newCurrent.ID, "history-resume-current-route", formerCurrent.ProjectID,
		formerCurrent.Workspace, formerCurrent.ResolvedScope, formerCurrent.RuntimeFingerprint, formerCurrent.Projects, formerCurrent.AdditionalDirectories); err != nil {
		store.Close()
		t.Fatal("resolve native-resume receipt", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	refreshed, err := history.Resolve(home, newCurrent.ID)
	if err != nil || refreshed.Fingerprint != capability.Fingerprint {
		t.Fatal("per-turn receipt changed compatible history grants", refreshed.Fingerprint, capability.Fingerprint, err)
	}
	if strings.Join(refreshed.Server(binary).Args, "\x00") == strings.Join(capability.Server(binary).Args, "\x00") {
		t.Fatal("native resume did not replace receipt-bound MCP arguments")
	}
	runNative(refreshed, formerCurrent.ID, newCurrent.ID,
		"A new synthetic request has been admitted and the conversation_history MCP binding has refreshed. Search for the exact earlier message text "+fmt.Sprintf("%q", formerCurrent.Text)+" and read its returned reference using the MCP. You must make fresh calls to BOTH search_conversation_history and read_conversation_history in this resumed turn, then report that message's input and actual execution/delivery states. Use only these two history tools; do not use filesystem or shell tools, and do not invent missing material.", true)
	if path := os.Getenv("AGENT_HOST_LIVE_REPORT"); path != "" {
		report, err := json.MarshalIndent(map[string]any{
			"report_version": 1, "date": time.Now().In(time.FixedZone("Asia/Singapore", 8*60*60)).Format("2006-01-02"),
			"method": "isolated_native_mcp_acceptance", "passed": true,
			"synthetic_source": true, "fresh_native_session": true,
			"native_search_completed": true, "native_read_completed": true,
			"saved_reply_recovered": true, "delivery_status_recovered": true,
			"native_resume_completed": true, "same_native_session": true,
			"stable_history_binding_on_resume": true, "refreshed_receipt_on_native_resume": true,
			"former_current_receipt_retrieved_after_resume": true,
			"current_request_excluded_initial":              true, "current_request_excluded_after_resume": true,
			"only_history_tools_called": true,
			"elapsed_ms":                time.Since(started).Milliseconds(),
			"live_account_read":         false, "messages_sent": 0,
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(report, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// The native Messages stream wraps MCP text results in content blocks. Decode
// the tool's actual data rather than accepting an assistant's recalled answer.
func nativeHistoryToolResult(raw json.RawMessage) (state.ConversationHistoryResult, error) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "text" {
			return state.ConversationHistoryResult{}, errors.New("native history result is not one MCP text block")
		}
		text = blocks[0].Text
	}
	var result state.ConversationHistoryResult
	if json.Unmarshal([]byte(text), &result) != nil || result.Turns == nil || result.Coverage.Source == "" {
		return result, errors.New("native history result omitted structured conversation evidence")
	}
	return result, nil
}
