package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestProtocolRetainsMalformedBytesInSerializableEvidence(t *testing.T) {
	bad := []byte{'{', 'x', 0xff}
	var evidence harness.Observation
	decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { evidence = o; return nil }}
	if err := decoder.consume(bad); err == nil {
		t.Fatal("malformed stream accepted")
	}
	if evidence.Type != "protocol_error" || !json.Valid(evidence.Raw) {
		t.Fatalf("unserializable evidence: %+v", evidence)
	}
	var retained struct {
		Bytes []byte `json:"invalid_native_line_base64"`
	}
	if err := json.Unmarshal(evidence.Raw, &retained); err != nil || !bytes.Equal(retained.Bytes, bad) {
		t.Fatalf("raw bytes lost: %v, %v", retained.Bytes, err)
	}
}

func TestToolInputAndOutputRemainNativeEvidence(t *testing.T) {
	var observations []harness.Observation
	decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error {
		observations = append(observations, o)
		return nil
	}}
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"m"}`,
		`{"type":"stream_event","session_id":"s","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"Read"}}}`,
		`{"type":"stream_event","session_id":"s","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"x\"}"}}}`,
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"t","name":"Read","input":{"file_path":"x"}}]}}`,
		`{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"source contents","is_error":false}]}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"done"}`,
	}
	for _, line := range lines {
		if err := decoder.consume([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	wantTypes := []string{"init", "tool_start", "tool_input", "tool_start", "tool_result", "result"}
	for i, observation := range observations {
		if observation.Type != wantTypes[i] || string(observation.Raw) != lines[i] {
			t.Fatalf("observation %d = %+v", i, observation)
		}
	}
	if observations[1].Tool != nil || observations[2].Tool != nil {
		t.Fatal("partial tool input became a complete call")
	}
	call, result := observations[3].Tool, observations[4].Tool
	if call == nil || result == nil || call.ID != "t" || call.Stage != "call" || string(call.Input) != `{"file_path":"x"}` || result.ID != call.ID || result.Name != "Read" || result.Stage != "result" || result.IsError {
		t.Fatalf("normalized tool correlation = %+v %+v", call, result)
	}
}

func TestRepeatedInitializationRetainsEvidenceAndResolvedExecutionState(t *testing.T) {
	var observations []harness.Observation
	decoder := streamDecoder{session: "s", mcpServers: []harness.MCPServer{{Name: "knowledge_dokki"}}, emit: func(o harness.Observation) error {
		observations = append(observations, o)
		return nil
	}}
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"claude-sonnet-5[1m]","uuid":"first","mcp_servers":[{"name":"knowledge_dokki","status":"connected"}]}`,
		`{"type":"stream_event","session_id":"s","event":{"type":"message_start","message":{"model":"claude-sonnet-5"}}}`,
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"read","name":"Read","input":{"file_path":"source.go"}}]}}`,
		`{"type":"system","subtype":"task_notification","session_id":"s","task_id":"background","status":"completed"}`,
		`{"type":"system","subtype":"init","session_id":"s","model":"claude-sonnet-5[1m]","uuid":"refreshed","mcp_servers":[{"name":"knowledge_dokki","status":"connected"}]}`,
		`{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result","tool_use_id":"read","content":"source contents"}]}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"done"}`,
	}
	for i, line := range lines {
		if err := decoder.consume([]byte(line)); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if i == 4 && (decoder.finalSeen || decoder.model != "claude-sonnet-5") {
			t.Fatal("initialization refresh changed model or completed the task")
		}
	}
	if len(observations) != len(lines) || observations[0].Type != "init" || observations[4].Type != "native" {
		t.Fatalf("initialization refresh started a second execution: %+v", observations)
	}
	for i, observation := range observations {
		if string(observation.Raw) != lines[i] || observation.NativeSessionID != "s" {
			t.Fatalf("event %d lost native evidence: %+v", i, observation)
		}
	}
	if tool := observations[5].Tool; tool == nil || tool.ID != "read" || tool.Name != "Read" || tool.Stage != "result" {
		t.Fatalf("initialization refresh lost tool correlation: %+v", tool)
	}
	if !decoder.finalSeen || decoder.finalError || decoder.text != "done" || decoder.model != "claude-sonnet-5" {
		t.Fatalf("native completion changed after initialization refresh: %+v", decoder)
	}
}

func TestRepeatedInitializationRejectsInvalidIdentity(t *testing.T) {
	for _, tc := range []struct{ name, fields, wantError string }{
		{"other-session", `"session_id":"other","model":"m"`, "does not match requested session"},
		{"missing-session", `"model":"m"`, "invalid"},
		{"missing-model", `"session_id":"s"`, "invalid"},
		{"changed-model", `"session_id":"s","model":"changed"`, "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed harness.Observation
			decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observed = o; return nil }}
			if err := decoder.consume([]byte(`{"type":"system","subtype":"init","session_id":"s","model":"m"}`)); err != nil {
				t.Fatal(err)
			}
			line := `{"type":"system","subtype":"init",` + tc.fields + `}`
			err := decoder.consume([]byte(line))
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || observed.Type != "protocol_error" || string(observed.Raw) != line || decoder.finalSeen {
				t.Fatalf("invalid repeated initialization accepted or evidence lost: %v %+v", err, observed)
			}
		})
	}
}

func TestLinkedBackgroundContinuationPreservesNativeResultOrdering(t *testing.T) {
	main := `{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"waiting for Agent"}`
	continuation := `{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"actual final answer","origin":{"kind":"task-notification"}}`
	notice := `{"type":"system","subtype":"task_notification","session_id":"s","task_id":"background","tool_use_id":"agent","status":"completed"}`
	refresh := `{"type":"system","subtype":"init","session_id":"s","model":"alias[1m]","uuid":"refreshed"}`
	for _, tc := range []struct {
		name  string
		turns []string
	}{
		{"main-before-notice", []string{main, notice, refresh, continuation}},
		{"delayed-main", []string{notice, refresh, main, continuation}},
		{"continuation-before-main", []string{notice, refresh, continuation, main}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observations []harness.Observation
			decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observations = append(observations, o); return nil }}
			lines := []string{
				`{"type":"system","subtype":"init","session_id":"s","model":"alias[1m]","uuid":"first"}`,
				`{"type":"assistant","session_id":"s","message":{"model":"resolved","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
				`{"type":"system","subtype":"task_started","session_id":"s","task_id":"background","tool_use_id":"agent"}`,
			}
			lines = append(lines, tc.turns...)
			for i, line := range lines {
				if err := decoder.consume([]byte(line)); err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
				if decoder.finalSeen != (i == len(lines)-1) {
					t.Fatalf("event %d completed before both native results or lost completion", i)
				}
			}
			if !decoder.finalSeen || decoder.finalError || decoder.text != "actual final answer" || decoder.model != "resolved" {
				t.Fatalf("native continuation result changed: %+v", decoder)
			}
			if len(observations) != len(lines) {
				t.Fatal("native lifecycle evidence lost")
			}
			for i, observation := range observations {
				if string(observation.Raw) != lines[i] || observation.NativeSessionID != "s" {
					t.Fatalf("event %d evidence changed: %+v", i, observation)
				}
				if i > 0 && observation.Type == "init" || i < len(lines)-1 && observation.Type == "result" {
					t.Fatalf("intermediate native event restarted or completed execution: %+v", observation)
				}
			}
			if observations[len(lines)-1].Type != "result" {
				t.Fatal("native final result was not surfaced")
			}
		})
	}
}

func TestUnlinkedBackgroundEventsCannotAuthorizeContinuationResult(t *testing.T) {
	for _, tc := range []struct{ name, taskID, toolID, status string }{
		{"unknown-task", "other", "agent", "completed"},
		{"wrong-tool", "background", "other", "completed"},
		{"missing-tool", "background", "", "completed"},
		{"nonterminal", "background", "agent", "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed harness.Observation
			decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observed = o; return nil }}
			notice, _ := json.Marshal(map[string]string{"type": "system", "subtype": "task_notification", "session_id": "s", "task_id": tc.taskID, "tool_use_id": tc.toolID, "status": tc.status})
			lines := []string{
				`{"type":"system","subtype":"init","session_id":"s","model":"m"}`,
				`{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
				`{"type":"system","subtype":"task_started","session_id":"s","task_id":"background","tool_use_id":"agent"}`,
				string(notice),
				`{"type":"system","subtype":"init","session_id":"s","model":"m"}`,
				`{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"unrelated answer","origin":{"kind":"task-notification"}}`,
			}
			for _, line := range lines {
				if err := decoder.consume([]byte(line)); err != nil {
					t.Fatal(err)
				}
			}
			if decoder.finalSeen || decoder.text != "" || observed.Type != "native" || string(observed.Raw) != lines[len(lines)-1] {
				t.Fatalf("unlinked notification became submitted completion: %+v %+v", decoder, observed)
			}
		})
	}
}

func TestAuthorizedContinuationStillRequiresValidFinalResult(t *testing.T) {
	for _, fields := range []string{
		`"subtype":"success","is_error":false`,
		`"session_id":"s","is_error":false`,
		`"session_id":"s","subtype":"success"`,
	} {
		var observed harness.Observation
		decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observed = o; return nil }}
		for _, line := range []string{
			`{"type":"system","subtype":"init","session_id":"s","model":"m"}`,
			`{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
			`{"type":"system","subtype":"task_started","session_id":"s","task_id":"background","tool_use_id":"agent"}`,
			`{"type":"system","subtype":"task_notification","session_id":"s","task_id":"background","tool_use_id":"agent","status":"completed"}`,
			`{"type":"system","subtype":"init","session_id":"s","model":"m"}`,
		} {
			if err := decoder.consume([]byte(line)); err != nil {
				t.Fatal(err)
			}
		}
		line := `{"type":"result","origin":{"kind":"task-notification"},` + fields + `}`
		err := decoder.consume([]byte(line))
		if err == nil || !strings.Contains(err.Error(), "lacks session") || decoder.finalSeen || observed.Type != "protocol_error" || string(observed.Raw) != line {
			t.Fatalf("authorized continuation bypassed result validation: %v %+v", err, observed)
		}
	}
}

func TestInlineNotificationCompletionWaitsForNativeProcessClosure(t *testing.T) {
	decoder := streamDecoder{session: "s"}
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"alias"}`,
		`{"type":"assistant","session_id":"s","message":{"model":"resolved","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","session_id":"s","task_id":"background","tool_use_id":"agent"}`,
		`{"type":"system","subtype":"task_notification","session_id":"s","task_id":"background","tool_use_id":"agent","status":"completed"}`,
		`{"type":"stream_event","session_id":"s","parent_tool_use_id":null,"event":{"type":"message_start","message":{"model":"resolved"}}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"inline final answer"}`,
	}
	for i, line := range lines {
		if err := decoder.consume([]byte(line)); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if decoder.finalSeen {
			t.Fatalf("event %d completed before native process closure", i)
		}
	}
	decoder.finish()
	if !decoder.finalSeen || decoder.finalError || decoder.text != "inline final answer" || decoder.model != "resolved" {
		t.Fatalf("parent's completed same-turn response lost: %+v", decoder)
	}
}

func TestInlineNotificationCannotCompleteEarlierWaitingResult(t *testing.T) {
	decoder := streamDecoder{session: "s"}
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"m"}`,
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","session_id":"s","task_id":"background","tool_use_id":"agent"}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"waiting for Agent"}`,
		`{"type":"system","subtype":"task_notification","session_id":"s","task_id":"background","tool_use_id":"agent","status":"completed"}`,
		`{"type":"stream_event","session_id":"s","parent_tool_use_id":null,"event":{"type":"message_start","message":{"model":"m"}}}`,
	} {
		if err := decoder.consume([]byte(line)); err != nil {
			t.Fatal(err)
		}
		if decoder.finalSeen {
			t.Fatal("parent response start completed the earlier waiting result")
		}
	}
	decoder.finish()
	if decoder.finalSeen || decoder.text != "waiting for Agent" {
		t.Fatalf("process closure promoted an earlier incomplete result: %+v", decoder)
	}
}

func TestToolBlocksRemainSeparateAndRepeatedCallsAreNotNormalizedTwice(t *testing.T) {
	var observations []harness.Observation
	decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observations = append(observations, o); return nil }}
	call := `{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"a","name":"Read","input":{"file_path":"x"}},{"type":"tool_use","id":"b","name":"Bash","input":{"command":"go test ./..."}}]}}`
	result := `{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"source"}]},{"type":"tool_result","tool_use_id":"b","content":"tests failed","is_error":true}]}}`
	for _, line := range []string{call, call, result, result} {
		if err := decoder.consume([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if len(observations) != 6 {
		t.Fatalf("observations = %d", len(observations))
	}
	for i, want := range []string{"a", "b", "", "a", "b", ""} {
		o := observations[i]
		if want == "" {
			if o.Tool != nil || o.Type != "native" {
				t.Fatalf("duplicate normalized: %+v", o)
			}
			continue
		}
		if o.Tool == nil || o.Tool.ID != want {
			t.Fatalf("tool %d = %+v", i, o)
		}
		wantRaw := call
		if i >= 3 {
			wantRaw = result
		}
		if string(o.Raw) != wantRaw {
			t.Fatal("lost full source envelope")
		}
	}
	if observations[3].Tool.IsError || !observations[4].Tool.IsError {
		t.Fatal("tool error flags were mixed")
	}
	if err := decoder.consume([]byte(strings.Replace(call, `"x"`, `"changed"`, 1))); err == nil || !strings.Contains(err.Error(), "conflicting repeated") {
		t.Fatalf("conflicting ID accepted: %v", err)
	}
}

func TestInvalidToolEvidenceFailsVisibly(t *testing.T) {
	for _, payload := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Read","input":null}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","is_error":null}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","is_error":"false"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"a"}]}}`,
	} {
		var observation harness.Observation
		decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observation = o; return nil }}
		if err := decoder.consume([]byte(payload)); err == nil || observation.Type != "protocol_error" || string(observation.Raw) != payload {
			t.Fatalf("invalid tool evidence accepted or lost: %v %+v", err, observation)
		}
	}
}

func TestExtractionAllowsOnlyStructuredOutputFormatter(t *testing.T) {
	for _, tc := range []struct {
		tools, mcp string
		wantError  bool
	}{
		{`[]`, `[]`, false}, {`["StructuredOutput"]`, `[]`, false}, {`["Bash"]`, `[]`, true}, {`[]`, `[{"name":"server"}]`, true},
	} {
		for _, repeated := range []bool{false, true} {
			decoder := streamDecoder{session: "s", extraction: true}
			if repeated {
				if err := decoder.consume([]byte(`{"type":"system","subtype":"init","session_id":"s","model":"m","tools":["StructuredOutput"],"mcp_servers":[]}`)); err != nil {
					t.Fatal(err)
				}
			}
			line := `{"type":"system","subtype":"init","session_id":"s","model":"m","tools":` + tc.tools + `,"mcp_servers":` + tc.mcp + `}`
			if err := decoder.consume([]byte(line)); (err != nil) != tc.wantError {
				t.Fatalf("repeated=%t tools=%s mcp=%s err=%v", repeated, tc.tools, tc.mcp, err)
			}
		}
	}
}

func TestMCPInitializationRequiresExactConnectedSources(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		configured   bool
		wantError    string
	}{
		{"none", ``, false, ""},
		{"connected", `,"mcp_servers":[{"name":"knowledge_dokki","status":"connected"}]`, true, ""},
		{"missing", ``, true, "missing"},
		{"empty", `,"mcp_servers":[]`, true, "missing"},
		{"failed", `,"mcp_servers":[{"name":"knowledge_dokki","status":"failed"}]`, true, "not connected"},
		{"pending", `,"mcp_servers":[{"name":"knowledge_dokki","status":"pending"}]`, true, "not connected"},
		{"unknown-status", `,"mcp_servers":[{"name":"knowledge_dokki","status":"future_status"}]`, true, "not connected"},
		{"ambient", `,"mcp_servers":[{"name":"ambient","status":"connected"}]`, false, "unconfigured"},
		{"duplicate", `,"mcp_servers":[{"name":"knowledge_dokki","status":"connected"},{"name":"knowledge_dokki","status":"connected"}]`, true, "duplicate"},
		{"missing-status", `,"mcp_servers":[{"name":"knowledge_dokki"}]`, true, "invalid"},
		{"malformed-entry", `,"mcp_servers":["knowledge_dokki"]`, true, "invalid"},
		{"null-entry", `,"mcp_servers":[null]`, true, "invalid"},
		{"config-error", `,"mcp_server_errors":[{"name":"knowledge_dokki","type":"invalid_config","message":"private-configuration"}]`, true, "rejected"},
		{"error-despite-connected", `,"mcp_servers":[{"name":"knowledge_dokki","status":"connected"}],"mcp_server_errors":[{"name":"other","type":"future_type","message":"private-configuration"}]`, true, "rejected"},
	} {
		for _, repeated := range []bool{false, true} {
			name := tc.name
			if repeated {
				name += "/repeated"
			}
			t.Run(name, func(t *testing.T) {
				var observed harness.Observation
				decoder := streamDecoder{session: "s", emit: func(o harness.Observation) error { observed = o; return nil }}
				if tc.configured {
					decoder.mcpServers = []harness.MCPServer{{Name: "knowledge_dokki", Command: "/bin/host"}}
				}
				if repeated {
					fields := ""
					if tc.configured {
						fields = `,"mcp_servers":[{"name":"knowledge_dokki","status":"connected"}]`
					}
					if err := decoder.consume([]byte(`{"type":"system","subtype":"init","session_id":"s","model":"m"` + fields + `}`)); err != nil {
						t.Fatal(err)
					}
				}
				line := `{"type":"system","subtype":"init","session_id":"s","model":"m"` + tc.fields + `}`
				err := decoder.consume([]byte(line))
				if tc.wantError == "" {
					wantType := "init"
					if repeated {
						wantType = "native"
					}
					if err != nil || !decoder.initialized || observed.Type != wantType {
						t.Fatalf("valid init rejected: %v %+v", err, observed)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), "private-configuration") || decoder.initialized != repeated || !decoder.mcpSetupFailed || observed.Type != "protocol_error" {
					t.Fatalf("invalid init accepted or diagnostic leaked: %v %+v", err, observed)
				}
				if string(observed.Raw) != line {
					t.Fatal("native MCP setup evidence lost")
				}
			})
		}
	}
}

func TestExtractionFormatterRemainsRawEvidence(t *testing.T) {
	var observations []harness.Observation
	decoder := streamDecoder{session: "s", extraction: true, emit: func(o harness.Observation) error { observations = append(observations, o); return nil }}
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"format","name":"StructuredOutput","input":{"drafts":[]}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"format","content":"validated"}]}}`,
	}
	for _, line := range lines {
		if err := decoder.consume([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for i, o := range observations {
		if o.Tool != nil || string(o.Raw) != lines[i] || o.Type != "native" {
			t.Fatalf("formatter became business evidence: %+v", o)
		}
	}
	if err := decoder.consume([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"bad","name":"Bash","input":{"command":"touch file"}}]}}`)); err == nil {
		t.Fatal("extractor physical tool accepted")
	}
}

func TestLineLimitFailsAndCancels(t *testing.T) {
	canceled := false
	w := lineWriter{consume: func([]byte) error { return nil }, cancel: func() { canceled = true }}
	if _, err := w.Write(bytes.Repeat([]byte{'x'}, maxNativeLine+1)); err == nil || !canceled {
		t.Fatalf("oversized line did not fail and cancel: %v", err)
	}
	if _, err := w.Write([]byte("{}\n")); !errors.Is(err, w.err) {
		t.Fatalf("writer resumed after failure: %v", err)
	}
}

func TestImageEnvelopeLineBudgetStillBoundsOutput(t *testing.T) {
	consumed, canceled := false, false
	w := lineWriter{limit: maxNativeLine + 1024, consume: func(data []byte) error {
		consumed = len(data) == maxNativeLine+1
		return nil
	}, cancel: func() { canceled = true }}
	line := append(bytes.Repeat([]byte{'x'}, maxNativeLine+1), '\n')
	if _, err := w.Write(line); err != nil || !consumed || canceled {
		t.Fatal("bounded image envelope rejected", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte{'x'}, w.limit+1)); err == nil || !canceled {
		t.Fatal("image envelope limit not enforced")
	}
}
