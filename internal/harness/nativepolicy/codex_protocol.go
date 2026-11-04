package nativepolicy

import (
	"encoding/json"
	"errors"
	"strings"

	"agent-host/internal/harness"
)

type codexRPCDecoder struct {
	expectedSession string
	session         string
	resume          bool
	terminal        bool
	emit            func(harness.Observation) error
}

func mustJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func (d *codexRPCDecoder) consume(raw []byte) error {
	var frame struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.JSONRPC != "" && frame.JSONRPC != "2.0" {
		return errors.New("Codex app-server emitted malformed JSON-RPC evidence")
	}
	observation := harness.Observation{Type: "codex.rpc", Raw: append(json.RawMessage(nil), raw...)}
	if frame.Method == "" {
		var response struct {
			Model  string `json:"model"`
			Thread struct {
				ID, Model string
			} `json:"thread"`
		}
		if json.Unmarshal(frame.Result, &response) == nil && response.Thread.ID != "" {
			if err := d.observeSession(response.Thread.ID); err != nil {
				return err
			}
			observation.Type, observation.NativeSessionID = "init", response.Thread.ID
			observation.Model = firstString(response.Model, response.Thread.Model)
			return d.send(observation)
		}
		return nil
	}
	observation.Type = "codex." + strings.ReplaceAll(frame.Method, "/", ".")
	var params struct {
		ThreadID, TurnID, Delta, FromModel, ToModel string
		Thread                                      struct{ ID, Model string }
		Turn                                        struct {
			ID, Status string
			Error      json.RawMessage
			Items      []struct{ ID, Type, Text string }
		}
		Item struct {
			ID, Type, Text, Status             string
			Server, ServerName, Tool, ToolName string
			Arguments, Result, Error, Output   json.RawMessage
		}
	}
	if len(frame.Params) > 0 && json.Unmarshal(frame.Params, &params) != nil {
		return errors.New("Codex app-server emitted invalid notification params")
	}
	if params.ThreadID != "" && d.session != "" && params.ThreadID != d.session {
		return errors.New("Codex app-server notification belongs to another thread")
	}
	switch frame.Method {
	case "thread/started":
		if err := d.observeSession(params.Thread.ID); err != nil {
			return err
		}
		observation.Type, observation.NativeSessionID, observation.Model = "init", params.Thread.ID, params.Thread.Model
	case "item/agentMessage/delta":
		observation.Type, observation.Delta, observation.NativeSessionID = "text_delta", params.Delta, d.session
	case "item/started", "item/completed":
		completed := frame.Method == "item/completed"
		switch params.Item.Type {
		case "agentMessage", "agent_message":
			if completed && params.Item.Text != "" {
				observation.Type, observation.Message = "text_final", params.Item.Text
			}
		case "commandExecution", "command_execution", "fileChange", "file_change", "webSearch", "web_search":
			observation.Type, observation.Tool = codexToolObservation(params.Item.ID, "codex."+params.Item.Type, completed, frame.Params, params.Item.Status, params.Item.Output)
		case "mcpToolCall", "mcp_tool_call":
			name := "mcp__" + firstString(params.Item.Server, params.Item.ServerName) + "__" + firstString(params.Item.Tool, params.Item.ToolName)
			observation.Type, observation.Tool = codexToolObservation(params.Item.ID, name, completed, params.Item.Arguments, params.Item.Status, firstRaw(params.Item.Result, params.Item.Error))
		}
		observation.NativeSessionID = d.session
	case "model/rerouted":
		observation.Type, observation.Model, observation.NativeSessionID = "model", params.ToModel, d.session
	case "turn/completed":
		d.terminal = true
		for _, item := range params.Turn.Items {
			if (item.Type == "agentMessage" || item.Type == "agent_message") && item.Text != "" {
				if err := d.send(harness.Observation{Type: "text_final", Message: item.Text, NativeSessionID: d.session, Raw: append(json.RawMessage(nil), raw...)}); err != nil {
					return err
				}
			}
		}
		observation.Type, observation.Message, observation.NativeSessionID = "result", params.Turn.Status, d.session
		if params.Turn.Status != "completed" {
			observation.Message = "Codex turn " + params.Turn.Status
		}
	}
	return d.send(observation)
}

func (d *codexRPCDecoder) observeSession(id string) error {
	if id == "" || d.resume && id != d.expectedSession || d.session != "" && d.session != id {
		return errors.New("Codex app-server reported an incompatible native thread")
	}
	d.session = id
	return nil
}

func (d *codexRPCDecoder) send(observation harness.Observation) error {
	if d.emit != nil {
		return d.emit(observation)
	}
	return nil
}

func codexToolObservation(id, name string, completed bool, input json.RawMessage, status string, result json.RawMessage) (string, *harness.ToolObservation) {
	stage, kind := "call", "tool_start"
	if completed {
		stage, kind = "result", "tool_result"
	}
	return kind, &harness.ToolObservation{ID: id, Name: name, Stage: stage, Input: input, Result: result, IsError: status == "failed"}
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if len(value) > 0 && string(value) != "null" {
			return value
		}
	}
	return nil
}
