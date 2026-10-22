// Adapted from github.com/zhoushoujianwork/memgov/plugins/data/knowledge
// (Apache-2.0; see memgov-LICENSE). Modified for Agent-Host's source pinning,
// strict protocol validation and source-specific read/write tools.
package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations"`
}

func inputSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func field(kind string) map[string]any { return map[string]any{"type": kind} }

func readTool(name, description string, schema map[string]any) mcpTool {
	return mcpTool{Name: name, Description: description, InputSchema: schema,
		Annotations: map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}}
}

func readSchema(id string) map[string]any {
	return inputSchema(map[string]any{
		id:                        field("string"),
		"offset":                  map[string]any{"type": "integer", "minimum": 0, "default": 0},
		"limit":                   map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadCharacters, "default": defaultReadCharacters},
		"expected_content_sha256": map[string]any{"type": "string", "description": "Prior coverage.full_content_sha256; required when offset > 0 to reject changed source content."},
	}, id)
}

const excerptInstructions = " Returns a bounded character excerpt, not necessarily a complete document. Inspect _agent_host.coverage; request its next_offset with the same ID and expected_content_sha256=full_content_sha256 to continue. Preserve source scope and partial coverage. Each call rereads the source; changed content rejects continuation."

// ToolContract exposes the same public definitions used by tools/list, without credentials.
func ToolContract(provider string) any { return toolsFor(provider) }

func toolsFor(provider string) []mcpTool {
	switch provider {
	case "local_markdown":
		return []mcpTool{
			readTool("search_local_markdown", "Search the explicitly configured local Markdown paths. Results retain relative paths and source receipts. Read relevant documents before making claims; bounded scan coverage and zero matches do not prove global absence. "+CitationInstructions, inputSchema(map[string]any{"query": field("string"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 10}}, "query")),
			readTool("read_local_markdown", "Read an authorized local Markdown document using its relative path from search. "+CitationInstructions+excerptInstructions, readSchema("path")),
		}
	case "dokki":
		return []mcpTool{
			readTool("list_dokki_workspaces", "List workspaces visible to this source's configured credential; the returned window may be incomplete.", inputSchema(map[string]any{"limit": field("integer")})),
			readTool("search_dokki", "Search this Dokki source when relevant; read matching resources before making claims and cite returned source_url. Zero results do not prove global absence.", inputSchema(map[string]any{"query": field("string"), "mode": field("string"), "workspace_ids": map[string]any{"type": "array", "items": field("string")}, "limit": field("integer")}, "query")),
			readTool("read_dokki_resource", "Read a Dokki resource identified by a search result."+excerptInstructions, readSchema("resource_id")),
		}
	case "confluence":
		return []mcpTool{
			readTool("search_confluence", "Search visible Confluence content: keyword mode searches pages and blog posts; CQL mode retains the supplied filter. Results cover this query and window, not all company knowledge. Read relevant content and cite source_url.", inputSchema(map[string]any{"query": field("string"), "query_mode": field("string"), "limit": field("integer"), "start": field("integer")}, "query")),
			readTool("read_confluence_page", "Read a Confluence page by numeric ID, retaining source URL and upstream version when available."+excerptInstructions, readSchema("id")),
			confluenceWriteTool(false),
			confluenceWriteTool(true),
			readTool("read_confluence_blogpost", "Read a Confluence blog post by numeric ID, retaining source URL and upstream version."+excerptInstructions, readSchema("id")),
			confluenceContentWriteTool(false, "blogpost"),
			confluenceContentWriteTool(true, "blogpost"),
		}
	default:
		return nil
	}
}

func toolAllowed(provider, name string) bool {
	for _, tool := range toolsFor(provider) {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func callSourceTool(ctx context.Context, source ResolvedSource, name string, arguments json.RawMessage) (map[string]any, error) {
	if !toolAllowed(source.Source.Provider, name) {
		return nil, errors.New("knowledge tool is not enabled for this source")
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if bytes.Equal(bytes.TrimSpace(arguments), []byte("null")) {
		return nil, errors.New("invalid knowledge tool arguments")
	}
	c := newSourceClient(source)
	switch name {
	case "create_confluence_page", "update_confluence_page", "create_confluence_blogpost", "update_confluence_blogpost":
		var args confluenceWriteArgs
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		// Enforce the distinct create/update schemas before any network operation.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(arguments, &fields)
		kind := "page"
		if strings.HasSuffix(name, "_blogpost") {
			kind = "blogpost"
		}
		update := strings.HasPrefix(name, "update_")
		schema := confluenceContentWriteTool(update, kind).InputSchema
		for key := range fields {
			if schema["properties"].(map[string]any)[key] == nil {
				return nil, errors.New("invalid knowledge tool arguments")
			}
		}
		for _, key := range schema["required"].([]string) {
			if fields[key] == nil {
				return nil, errors.New("invalid knowledge tool arguments")
			}
		}
		return c.writeConfluenceContent(ctx, args, update, kind)
	case "search_local_markdown":
		args := struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}{Limit: 10}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		return c.searchLocalMarkdown(ctx, args.Query, args.Limit)
	case "read_local_markdown":
		args := struct {
			Path           string `json:"path"`
			Offset         int    `json:"offset"`
			Limit          int    `json:"limit"`
			ExpectedDigest string `json:"expected_content_sha256"`
		}{Limit: defaultReadCharacters}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		return c.readLocalMarkdown(ctx, args.Path, args.Offset, args.Limit, args.ExpectedDigest)
	case "list_dokki_workspaces":
		args := struct {
			Limit int `json:"limit"`
		}{Limit: 50}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		return c.listDokkiWorkspaces(ctx, args.Limit)
	case "search_dokki":
		args := struct {
			Query        string   `json:"query"`
			Mode         string   `json:"mode"`
			WorkspaceIDs []string `json:"workspace_ids"`
			Limit        int      `json:"limit"`
		}{Mode: "hybrid", Limit: 10}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		return c.searchDokki(ctx, args.Query, args.Mode, args.WorkspaceIDs, args.Limit)
	case "read_dokki_resource":
		args := struct {
			ResourceID     string `json:"resource_id"`
			Offset         int    `json:"offset"`
			Limit          int    `json:"limit"`
			ExpectedDigest string `json:"expected_content_sha256"`
		}{Limit: defaultReadCharacters}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		return c.readDokkiResource(ctx, args.ResourceID, args.Offset, args.Limit, args.ExpectedDigest)
	case "search_confluence":
		args := struct {
			Query string `json:"query"`
			Mode  string `json:"query_mode"`
			Limit int    `json:"limit"`
			Start int    `json:"start"`
		}{Mode: "keyword", Limit: 10}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		return c.searchConfluence(ctx, args.Query, args.Mode, args.Limit, args.Start)
	case "read_confluence_page", "read_confluence_blogpost":
		args := struct {
			ID             string `json:"id"`
			Offset         int    `json:"offset"`
			Limit          int    `json:"limit"`
			ExpectedDigest string `json:"expected_content_sha256"`
		}{Limit: defaultReadCharacters}
		if err := decodeStrictJSON(arguments, &args); err != nil {
			return nil, errors.New("invalid knowledge tool arguments")
		}
		kind := "page"
		if name == "read_confluence_blogpost" {
			kind = "blogpost"
		}
		return c.readConfluenceContent(ctx, args.ID, args.Offset, args.Limit, args.ExpectedDigest, kind)
	}
	return nil, errors.New("unknown knowledge tool")
}

// decodeStrictJSON rejects trailing input, duplicate keys, excessive nesting
// and fields unknown to a typed destination. Map destinations preserve upstream
// metadata without imposing a fabricated schema on source bodies.
func decodeStrictJSON(raw []byte, destination any) error {
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if err := scanJSONValue(scanner, 0); err != nil {
		return err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	// encoding/json otherwise accepts case-insensitive field names and null
	// scalar values in structs, which would contradict the published schemas.
	typeOf := reflect.TypeOf(destination)
	if typeOf != nil && typeOf.Kind() == reflect.Pointer && typeOf.Elem().Kind() == reflect.Struct {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return errors.New("JSON object required")
		}
		allowed := map[string]bool{}
		for i := 0; i < typeOf.Elem().NumField(); i++ {
			name := typeOf.Elem().Field(i).Tag.Get("json")
			for j, r := range name {
				if r == ',' {
					name = name[:j]
					break
				}
			}
			allowed[name] = true
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

func scanJSONValue(decoder *json.Decoder, depth int) error {
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
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func validRPCID(raw json.RawMessage) bool {
	if len(raw) > 1024 {
		return false
	}
	var value any
	if err := decodeStrictJSON(raw, &value); err != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

// Serve exposes exactly one pinned source over newline-delimited MCP stdio.
// Local source access is revalidated on each tool call. No network request is
// made by startup, initialize, or tools/list.
func Serve(ctx context.Context, configPath, sourceName, expectedFingerprint string, in io.Reader, out io.Writer) error {
	if expectedFingerprint == "" {
		return errors.New("knowledge source fingerprint is required")
	}
	source, err := ResolveSource(configPath, sourceName, expectedFingerprint)
	if err != nil {
		return errors.New("knowledge source configuration or credentials unavailable")
	}
	if len(toolsFor(source.Source.Provider)) == 0 {
		return errors.New("unsupported knowledge provider")
	}
	reader := bufio.NewScanner(in)
	reader.Buffer(make([]byte, 4096), 1<<20)
	writer := json.NewEncoder(out)
	writeError := func(id json.RawMessage, code int, message string) error {
		if !validRPCID(id) {
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
		var request mcpRequest
		if !json.Valid(raw) {
			if err := writeError(nil, -32700, "invalid JSON"); err != nil {
				return err
			}
			continue
		}
		if err := decodeStrictJSON(raw, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" || len(request.ID) != 0 && !validRPCID(request.ID) {
			if err := writeError(request.ID, -32600, "invalid MCP request"); err != nil {
				return err
			}
			continue
		}
		// Notifications never trigger a remote source operation.
		if len(request.ID) == 0 {
			continue
		}
		if len(request.Params) == 0 {
			request.Params = json.RawMessage(`{}`)
		}
		var result any
		var failure string
		code := -32602
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string         `json:"protocolVersion"`
				Capabilities    map[string]any `json:"capabilities"`
				ClientInfo      map[string]any `json:"clientInfo"`
				Meta            map[string]any `json:"_meta,omitempty"`
			}
			if initialized || decodeStrictJSON(request.Params, &params) != nil || params.ProtocolVersion == "" || params.ClientInfo == nil || params.Capabilities == nil {
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
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "agent-host-knowledge", "version": "1.0.0"}}
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
			if decodeStrictJSON(request.Params, &params) != nil || params.Cursor != "" {
				failure = "invalid tool list parameters"
				break
			}
			result = map[string]any{"tools": toolsFor(source.Source.Provider)}
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
			if decodeStrictJSON(request.Params, &params) != nil || !toolAllowed(source.Source.Provider, params.Name) {
				failure = "tool is not enabled for this source"
				break
			}
			current, err := ResolveSource(configPath, sourceName, expectedFingerprint)
			if err != nil {
				result = toolError("knowledge source access changed or is unavailable")
				break
			}
			value, err := callSourceTool(ctx, current, params.Name, params.Arguments)
			if err != nil {
				result = toolError(err.Error())
				break
			}
			body, err := json.Marshal(value)
			if err != nil {
				return errors.New("knowledge result could not be encoded")
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(body)}}, "structuredContent": value, "isError": false}
			encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			if err != nil || len(encoded)+1 > maxToolResultBytes {
				result = toolError("encoded knowledge result exceeds the 64 KiB output limit; request a smaller limit (metadata may also exceed the bound)")
			}
		default:
			code, failure = -32601, "method not found"
		}
		if failure != "" {
			if err := writeError(request.ID, code, failure); err != nil {
				return err
			}
		} else if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return err
		}
	}
	if reader.Err() != nil {
		return fmt.Errorf("knowledge MCP input failed")
	}
	return nil
}

func toolError(message string) map[string]any {
	return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": message}}}
}
