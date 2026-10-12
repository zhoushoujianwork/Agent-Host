package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"agent-host/internal/harness"
)

const maxNativeLine = 16 * 1024 * 1024

type lineWriter struct {
	pending []byte
	limit   int
	consume func([]byte) error
	cancel  func()
	err     error
}

func (w *lineWriter) fail(err error) error {
	w.err = err
	w.cancel()
	return err
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		}
		limit := w.limit
		if limit == 0 {
			limit = maxNativeLine
		}
		if len(w.pending)+end > limit {
			return 0, w.fail(fmt.Errorf("Claude protocol: native line exceeds %d bytes", limit))
		}
		w.pending = append(w.pending, p[:end]...)
		if end == len(p) {
			break
		}
		if err := w.flush(); err != nil {
			return 0, err
		}
		p = p[end+1:]
	}
	return n, nil
}

func (w *lineWriter) flush() error {
	line := bytes.TrimSpace(w.pending)
	if len(line) != 0 {
		if err := w.consume(line); err != nil {
			return w.fail(err)
		}
	}
	w.pending = w.pending[:0]
	return nil
}

func (w *lineWriter) finish() { _ = w.flush() }

type envelope struct {
	Type             string            `json:"type"`
	Subtype          string            `json:"subtype"`
	SessionID        string            `json:"session_id"`
	Model            string            `json:"model"`
	IsError          *bool             `json:"is_error"`
	Result           string            `json:"result"`
	StructuredOutput json.RawMessage   `json:"structured_output"`
	Message          json.RawMessage   `json:"message"`
	Event            json.RawMessage   `json:"event"`
	Tools            []string          `json:"tools"`
	MCPServers       []json.RawMessage `json:"mcp_servers"`
	MCPServerErrors  []json.RawMessage `json:"mcp_server_errors"`
	Origin           struct {
		Kind string `json:"kind"`
	} `json:"origin"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   json.RawMessage `json:"is_error"`
}

type message struct {
	Model   string         `json:"model"`
	Content []contentBlock `json:"content"`
}

type streamDecoder struct {
	session          string
	model            string
	text             string
	subtype          string
	initialized      bool
	extraction       bool
	mcpServers       []harness.MCPServer
	mcpSetupFailed   bool
	finalSeen        bool
	finalError       bool
	structuredOutput json.RawMessage
	toolCalls        map[string]harness.ToolObservation
	toolResults      map[string]harness.ToolObservation
	emit             func(harness.Observation) error
}

func (s *streamDecoder) consume(line []byte) error {
	raw := bytes.Clone(line)
	observation := harness.Observation{Type: "native", Raw: raw, NativeSessionID: s.session}
	var event envelope
	if err := json.Unmarshal(raw, &event); err != nil {
		// A RawMessage must itself remain serializable by the host journal.
		// The []byte field uses base64 to retain even invalid UTF-8 exactly.
		observation.Raw, _ = json.Marshal(struct {
			InvalidNativeLine []byte `json:"invalid_native_line_base64"`
		}{raw})
		observation.Type = "protocol_error"
		observation.Message = "Malformed native JSON"
		if emitErr := s.observe(observation); emitErr != nil {
			return emitErr
		}
		return fmt.Errorf("Claude protocol: malformed event: %w", err)
	}
	if event.Type == "" {
		return s.reject(observation, "missing event type")
	}
	if event.SessionID != "" && event.SessionID != s.session {
		return s.reject(observation, "native session ID does not match requested session")
	}
	if s.finalSeen && event.Type != "system" && event.Type != "rate_limit_event" {
		return s.reject(observation, "unexpected event after final result")
	}
	var parseErr error
	switch event.Type {
	case "system":
		observation.Message = event.Subtype
		if event.Subtype == "init" {
			if s.initialized || event.SessionID == "" || event.Model == "" {
				return s.reject(observation, "invalid or duplicate session initialization")
			}
			if s.extraction {
				for _, tool := range event.Tools {
					if tool != "StructuredOutput" {
						return s.reject(observation, "extraction initialization exposed native tool "+tool)
					}
				}
				if len(event.MCPServers) != 0 {
					return s.reject(observation, "extraction initialization exposed MCP servers")
				}
			}
			if err := s.validateMCPInitialization(event); err != nil {
				s.mcpSetupFailed = true
				return s.reject(observation, err.Error())
			}
			s.initialized = true
			s.model = event.Model
			observation.Type, observation.Model = "init", s.model
		}
	case "stream_event":
		parseErr = s.partial(event.Event, &observation)
	case "assistant", "user":
		var msg message
		parseErr = json.Unmarshal(event.Message, &msg)
		if parseErr == nil {
			if msg.Model != "" {
				s.model = msg.Model
			}
			observation.Type = event.Type
			var tools []harness.ToolObservation
			for _, block := range msg.Content {
				switch block.Type {
				case "text":
					// Full text remains evidence; only partial deltas are rendered
					// as live text, so an assistant envelope cannot duplicate it.
				case "tool_use":
					if event.Type != "assistant" || block.ID == "" || block.Name == "" || len(block.Input) == 0 || block.Input[0] != '{' {
						return s.reject(observation, "invalid tool call identity or input")
					}
					if s.extraction && block.Name != "StructuredOutput" {
						return s.reject(observation, "extraction attempted native tool "+block.Name)
					}
					tools = append(tools, harness.ToolObservation{ID: block.ID, Name: block.Name, Stage: "call", Input: block.Input})
				case "tool_result":
					if event.Type != "user" || block.ToolUseID == "" {
						return s.reject(observation, "invalid tool result identity")
					}
					// The native Messages protocol defines is_error as optional.
					// Absence means no reported error; null or non-booleans are invalid.
					isError := false
					if len(block.IsError) > 0 {
						if !bytes.Equal(block.IsError, []byte("true")) && !bytes.Equal(block.IsError, []byte("false")) {
							return s.reject(observation, "tool result is_error is not a boolean")
						}
						isError = bytes.Equal(block.IsError, []byte("true"))
					}
					tools = append(tools, harness.ToolObservation{ID: block.ToolUseID, Stage: "result", Result: block.Content, IsError: isError})
				}
			}
			if len(tools) > 0 {
				observation.Model = s.model
				return s.observeTools(observation, tools)
			}
		}
	case "result":
		// Task-notification envelopes concern native background tasks, not the
		// final result of this submitted turn.
		if event.Origin.Kind == "task-notification" {
			observation.Message = "Native task notification"
			break
		}
		if event.SessionID == "" || event.IsError == nil || event.Subtype == "" {
			return s.reject(observation, "final result lacks session, subtype, or is_error")
		}
		s.finalSeen, s.finalError = true, *event.IsError || event.Subtype != "success"
		s.text, s.subtype = event.Result, event.Subtype
		s.structuredOutput = bytes.Clone(event.StructuredOutput)
		observation.Type, observation.Message = "result", event.Subtype
	}
	if parseErr != nil {
		return s.reject(observation, "malformed "+event.Type+" payload: "+parseErr.Error())
	}
	observation.Model = s.model
	return s.observe(observation)
}

// Native initialization reports connection state, not document retrieval or
// authorization success. Require the selected sources to be connected before
// accepting a business run; an absent/pending source cannot silently disappear.
func (s *streamDecoder) validateMCPInitialization(event envelope) error {
	if len(event.MCPServerErrors) > 0 {
		// Retain native diagnostics in the raw observation, without promoting
		// arbitrary server/configuration messages into the host error summary.
		return errors.New("native initialization rejected MCP server configuration; inspect native evidence")
	}
	expected := make(map[string]bool, len(s.mcpServers))
	for _, server := range s.mcpServers {
		expected[server.Name] = false
	}
	for _, raw := range event.MCPServers {
		var server struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &server); err != nil || server.Name == "" || server.Status == "" {
			return errors.New("invalid MCP server initialization record")
		}
		connected, ok := expected[server.Name]
		if !ok {
			return errors.New("native initialization exposed an unconfigured MCP server")
		}
		if connected {
			return fmt.Errorf("duplicate MCP initialization for configured server %s", server.Name)
		}
		if server.Status != "connected" {
			return fmt.Errorf("configured MCP server %s is not connected; inspect native evidence", server.Name)
		}
		expected[server.Name] = true
	}
	for _, server := range s.mcpServers {
		if !expected[server.Name] {
			return fmt.Errorf("configured MCP server %s is missing from native initialization", server.Name)
		}
	}
	return nil
}

func (s *streamDecoder) observeTools(source harness.Observation, tools []harness.ToolObservation) error {
	if s.toolCalls == nil {
		s.toolCalls, s.toolResults = make(map[string]harness.ToolObservation), make(map[string]harness.ToolObservation)
	}
	emitted := false
	for _, tool := range tools {
		seen := s.toolCalls
		if tool.Stage == "result" {
			seen = s.toolResults
			if call, ok := s.toolCalls[tool.ID]; ok {
				tool.Name = call.Name
			}
		}
		if previous, exists := seen[tool.ID]; exists {
			if !sameTool(previous, tool) {
				return s.reject(source, "conflicting repeated tool "+tool.Stage+" for "+tool.ID)
			}
			continue
		}
		seen[tool.ID] = tool
		observation := source
		if s.extraction && tool.Name == "StructuredOutput" {
			// The native schema formatter does not perform a business operation.
			// Retain its raw protocol evidence without presenting it as a tool
			// available to the isolated extractor or as new execution evidence.
			observation.Type, observation.Message = "native", "Native structured output formatter"
			if err := s.observe(observation); err != nil {
				return err
			}
			emitted = true
			continue
		}
		observation.Tool = &tool
		observation.Type, observation.Message = "tool_start", tool.Name+" "+tool.ID+" "+string(tool.Input)
		if tool.Stage == "result" {
			observation.Type, observation.Message = "tool_result", tool.ID+" "+string(tool.Result)
		}
		// Each normalized block retains its complete source envelope. This
		// permits evidence references without guessing how blocks were joined.
		if err := s.observe(observation); err != nil {
			return err
		}
		emitted = true
	}
	if !emitted {
		// Repeated full native messages remain raw evidence but must not create
		// a second call/result record or repeat a live tool notification.
		source.Type = "native"
		return s.observe(source)
	}
	return nil
}

func sameTool(a, b harness.ToolObservation) bool {
	if a.ID != b.ID || a.Name != b.Name || a.Stage != b.Stage || a.IsError != b.IsError {
		return false
	}
	return equivalentJSON(a.Input, b.Input) && equivalentJSON(a.Result, b.Result)
}

func equivalentJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var left, right any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&left) != nil || rightDecoder.Decode(&right) != nil {
		return false
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return bytes.Equal(l, r)
}

func (s *streamDecoder) partial(raw json.RawMessage, observation *harness.Observation) error {
	var event struct {
		Type         string       `json:"type"`
		Message      message      `json:"message"`
		ContentBlock contentBlock `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.Type == "" {
		return errors.New("missing partial event type")
	}
	switch event.Type {
	case "message_start":
		if event.Message.Model != "" {
			s.model = event.Message.Model
		}
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			observation.Type = "tool_start"
			observation.Message = event.ContentBlock.Name + " " + event.ContentBlock.ID
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			observation.Type, observation.Delta = "text_delta", event.Delta.Text
		case "input_json_delta":
			observation.Type, observation.Message = "tool_input", event.Delta.PartialJSON
		}
	}
	return nil
}

func (s *streamDecoder) reject(observation harness.Observation, reason string) error {
	observation.Type, observation.Message = "protocol_error", reason
	if err := s.observe(observation); err != nil {
		return err
	}
	return errors.New("Claude protocol: " + reason)
}

func (s *streamDecoder) observe(observation harness.Observation) error {
	if s.emit == nil {
		return nil
	}
	if err := s.emit(observation); err != nil {
		return fmt.Errorf("Claude observation callback: %w", err)
	}
	return nil
}
