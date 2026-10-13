// Package mcpstdio supplies the bounded internal read-only MCP transport.
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
)

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func validID(raw json.RawMessage) bool {
	if len(raw) > 1024 {
		return false
	}
	var value any
	if Decode(raw, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	}
	return false
}

func Serve(ctx context.Context, name string, catalog []Tool, call func(context.Context, string, json.RawMessage) (any, error), in io.Reader, out io.Writer) error {
	enabled := map[string]bool{}
	for _, t := range catalog {
		enabled[t.Name] = true
	}
	reader := bufio.NewScanner(in)
	reader.Buffer(make([]byte, 4096), 1<<20)
	writer := json.NewEncoder(out)
	writeError := func(id json.RawMessage, code int, message string) error {
		if !validID(id) {
			id = json.RawMessage(`null`)
		}
		return writer.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	}
	initialized := false
	for reader.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := reader.Bytes()
		var rpc request
		if !json.Valid(raw) {
			if err := writeError(nil, -32700, "invalid JSON"); err != nil {
				return err
			}
			continue
		}
		if Decode(raw, &rpc) != nil || rpc.JSONRPC != "2.0" || rpc.Method == "" || len(rpc.ID) != 0 && !validID(rpc.ID) {
			if err := writeError(rpc.ID, -32600, "invalid MCP request"); err != nil {
				return err
			}
			continue
		}
		if len(rpc.ID) == 0 {
			continue
		}
		if len(rpc.Params) == 0 {
			rpc.Params = json.RawMessage(`{}`)
		}
		var result any
		failure, code := "", -32602
		switch rpc.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string         `json:"protocolVersion"`
				Capabilities    map[string]any `json:"capabilities"`
				ClientInfo      map[string]any `json:"clientInfo"`
				Meta            map[string]any `json:"_meta,omitempty"`
			}
			if initialized || Decode(rpc.Params, &params) != nil || params.ProtocolVersion == "" || params.Capabilities == nil || params.ClientInfo == nil {
				failure = "invalid initialize parameters"
				break
			}
			version := params.ProtocolVersion
			switch version {
			case "2024-11-05", "2025-03-26", "2025-06-18":
			default:
				version = "2025-03-26"
			}
			initialized = true
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": name, "version": "1.0.0"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			var params struct {
				Cursor string         `json:"cursor,omitempty"`
				Meta   map[string]any `json:"_meta,omitempty"`
			}
			if !initialized {
				code, failure = -32002, "MCP server is not initialized"
				break
			}
			if Decode(rpc.Params, &params) != nil || params.Cursor != "" {
				failure = "invalid tool list parameters"
				break
			}
			result = map[string]any{"tools": catalog}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments,omitempty"`
				Meta      map[string]any  `json:"_meta,omitempty"`
			}
			if !initialized {
				code, failure = -32002, "MCP server is not initialized"
				break
			}
			if Decode(rpc.Params, &params) != nil || !enabled[params.Name] {
				failure = "read-only tool is not enabled"
				break
			}
			value, err := call(ctx, params.Name, params.Arguments)
			if err != nil {
				result = toolError(err.Error())
				break
			}
			body, err := json.Marshal(value)
			if err != nil {
				return errors.New("read-only result could not be encoded")
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(body)}}, "structuredContent": value, "isError": false}
			encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
			if err != nil || len(encoded)+1 > 64<<10 {
				result = toolError("encoded read-only result exceeds 64 KiB; request a smaller limit or neighbor window")
			}
		default:
			code, failure = -32601, "method not found"
		}
		if failure != "" {
			if err := writeError(rpc.ID, code, failure); err != nil {
				return err
			}
		} else if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result}); err != nil {
			return err
		}
	}
	if reader.Err() != nil {
		return errors.New("read-only MCP input failed")
	}
	return nil
}
func toolError(message string) map[string]any {
	return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": message}}}
}

// decode rejects ambiguous JSON (duplicates, null/unknown fields, excessive
// nesting and trailing input) before decoding the published typed contract.
func Decode(raw []byte, destination any) error {
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if err := scanJSON(scanner, 0); err != nil {
		return err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return errors.New("trailing JSON input")
	}
	kind := reflect.TypeOf(destination)
	if kind != nil && kind.Kind() == reflect.Pointer && kind.Elem().Kind() == reflect.Struct {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return errors.New("JSON object required")
		}
		allowed := map[string]bool{}
		for i := 0; i < kind.Elem().NumField(); i++ {
			allowed[stringsTag(kind.Elem().Field(i).Tag.Get("json"))] = true
		}
		for name, value := range fields {
			if !allowed[name] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return errors.New("unknown or null JSON field")
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	return decoder.Decode(destination)
}

func stringsTag(tag string) string {
	for i, r := range tag {
		if r == ',' {
			return tag[:i]
		}
	}
	return tag
}

func scanJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err := scanJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
