package acp

import (
	"encoding/json"
	"errors"
	"strings"

	"agent-host/internal/harness"
)

type toolState struct {
	name             string
	input, output    json.RawMessage
	called, finished bool
}
type projection struct {
	session, model, text string
	tools                map[string]*toolState
	emit                 func(harness.Observation) error
}

// safeEvidence excludes binary prompt/output bodies from the public journal.
func safeEvidence(raw json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["type"] == "image" || x["type"] == "audio" {
				delete(x, "data")
			}
			if _, ok := x["blob"]; ok {
				x["blob"] = "[binary omitted]"
			}
			for _, child := range x {
				strip(child)
			}
		case []any:
			for _, child := range x {
				strip(child)
			}
		}
	}
	strip(value)
	out, _ := json.Marshal(value)
	return out
}

func (p *projection) update(raw json.RawMessage) error {
	var u struct {
		Kind      string          `json:"sessionUpdate"`
		ID        string          `json:"toolCallId"`
		Name      string          `json:"name"`
		Title     string          `json:"title"`
		Status    string          `json:"status"`
		RawInput  json.RawMessage `json:"rawInput"`
		RawOutput json.RawMessage `json:"rawOutput"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return errors.New("invalid ACP session update")
	}
	o := harness.Observation{Type: "acp_update", NativeSessionID: p.session, Model: p.model, Raw: safeEvidence(raw)}
	switch u.Kind {
	case "agent_message_chunk":
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &block) != nil {
			return errors.New("invalid ACP message content")
		}
		if block.Type == "text" {
			if len(p.text)+len(block.Text) > 512<<10 {
				return errors.New("ACP answer exceeds 512 KiB")
			}
			p.text += block.Text
			o.Type = "text_delta"
			o.Delta = block.Text
		}
	case "tool_call", "tool_call_update":
		if u.ID == "" {
			return errors.New("ACP tool update has no ID")
		}
		t := p.tools[u.ID]
		if t == nil {
			if u.Kind != "tool_call" {
				return errors.New("ACP tool update has no preceding call")
			}
			if len(p.tools) >= 10000 {
				return errors.New("ACP tool count exceeds run limit")
			}
			t = &toolState{input: json.RawMessage(`{}`)}
			p.tools[u.ID] = t
		}
		if u.Name != "" {
			t.name = u.Name
		}
		if t.name == "" && u.Title != "" {
			t.name = u.Title
		}
		if len(u.RawInput) > 0 {
			t.input = safeEvidence(u.RawInput)
		}
		if len(u.RawOutput) > 0 {
			t.output = safeEvidence(u.RawOutput)
		} else if len(u.Content) > 0 {
			t.output = safeEvidence(u.Content)
		}
		if t.name == "" {
			t.name = "Tool"
		}
		stage := "update"
		o.Type = "tool_update"
		if !t.called {
			stage = "call"
			o.Type = "tool_start"
			t.called = true
		}
		o.Tool = &harness.ToolObservation{ID: u.ID, Name: t.name, Stage: stage, Input: t.input}
		if u.Status == "completed" || u.Status == "failed" {
			if !t.finished {
				// Preserve the final argument patch before closing the same call.
				if err := p.send(o); err != nil {
					return err
				}
				t.finished = true
				o.Type = "tool_result"
				o.Tool = &harness.ToolObservation{ID: u.ID, Name: t.name, Stage: "result", Input: t.input, Result: t.output, IsError: u.Status == "failed"}
			} else {
				o.Tool = nil
				o.Type = "acp_update"
			}
		}
	}
	return p.send(o)
}

func (p *projection) send(o harness.Observation) error {
	if p.emit != nil {
		return p.emit(o)
	}
	return nil
}

func currentModel(raw json.RawMessage) string {
	var result struct {
		Models struct {
			Current string `json:"currentModelId"`
		} `json:"models"`
		Config []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Value    string `json:"currentValue"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return ""
	}
	if result.Models.Current != "" {
		return result.Models.Current
	}
	for _, option := range result.Config {
		if option.Category == "model" || option.ID == "model" {
			return strings.TrimSpace(option.Value)
		}
	}
	return ""
}
