package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const maxConfluenceWriteBytes = 128 << 10

type confluenceWriteArgs struct {
	ID              string `json:"id"`
	SpaceKey        string `json:"space_key"`
	ParentID        string `json:"parent_id"`
	Title           string `json:"title"`
	Content         string `json:"content"`
	ExpectedVersion int64  `json:"expected_version"`
}

func confluenceWriteTool(update bool) mcpTool {
	properties := map[string]any{
		"title":   map[string]any{"type": "string", "description": "Complete page title."},
		"content": map[string]any{"type": "string", "description": "Complete page body in Confluence storage XHTML; replaces the body on update. Maximum 128 KiB."},
	}
	name := "create_confluence_page"
	description := "Create a Confluence page in the specified space, optionally under parent_id. Use only for a user-requested write. Returns page identity and version; read the page to verify content. Never automatically retry a failed or uncertain write; inspect the target first."
	required := []string{"space_key", "title", "content"}
	if update {
		name = "update_confluence_page"
		description = "Replace an existing Confluence page title and complete storage body for a user-requested update. Read the page first and supply its current version as expected_version; a concurrent edit causes a conflict. Never automatically retry a failed or uncertain write; read the page to reconcile."
		properties["id"] = field("string")
		properties["expected_version"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 2147483646}
		required = []string{"id", "title", "content", "expected_version"}
	} else {
		properties["space_key"] = field("string")
		properties["parent_id"] = field("string")
	}
	return mcpTool{Name: name, Description: description, InputSchema: inputSchema(properties, required...),
		Annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": update, "idempotentHint": false, "openWorldHint": true}}
}

func (c *sourceClient) writeConfluencePage(ctx context.Context, args confluenceWriteArgs, update bool) (map[string]any, error) {
	if strings.TrimSpace(args.Title) == "" || len(args.Title) > 1000 || len(args.Content) > maxConfluenceWriteBytes || strings.ContainsAny(args.Title, "\x00\r\n") || strings.ContainsRune(args.Content, 0) {
		return nil, errors.New("invalid Confluence title or storage body")
	}
	body := map[string]any{"type": "page", "title": args.Title,
		"body": map[string]any{"storage": map[string]any{"value": args.Content, "representation": "storage"}}}
	method, path, operation := http.MethodPost, "/content", "create"
	wantVersion := int64(1)
	if update {
		if !pageID.MatchString(args.ID) || args.ExpectedVersion < 1 || args.ExpectedVersion > 2147483646 || args.SpaceKey != "" || args.ParentID != "" {
			return nil, errors.New("update requires numeric id and expected_version 1..2147483646; space and parent cannot be changed")
		}
		wantVersion = args.ExpectedVersion + 1
		body["id"], body["version"] = args.ID, map[string]any{"number": wantVersion}
		method, path, operation = http.MethodPut, "/content/"+args.ID, "update"
	} else {
		if strings.TrimSpace(args.SpaceKey) == "" || len(args.SpaceKey) > 255 || strings.ContainsAny(args.SpaceKey, "\x00\r\n") || args.ParentID != "" && !pageID.MatchString(args.ParentID) || args.ID != "" || args.ExpectedVersion != 0 {
			return nil, errors.New("create requires space_key and an optional numeric parent_id; id and expected_version are not accepted")
		}
		body["space"] = map[string]any{"key": args.SpaceKey}
		if args.ParentID != "" {
			body["ancestors"] = []any{map[string]any{"id": args.ParentID}}
		}
	}
	result, err := c.confluenceRequest(ctx, method, path, body)
	if err != nil {
		return nil, fmt.Errorf("Confluence %s failed (%w); the write may have reached the server: inspect the target before any retry", operation, err)
	}
	version, _ := result["version"].(map[string]any)
	number, _ := version["number"].(json.Number)
	actualVersion, versionErr := number.Int64()
	if !pageID.MatchString(stringValue(result, "id")) || update && stringValue(result, "id") != args.ID || stringValue(result, "type") != "page" || stringValue(result, "title") == "" || versionErr != nil || actualVersion != wantVersion {
		return nil, errors.New("Confluence write response identity/version is invalid; the write may have succeeded: inspect the target before any retry")
	}
	compact := compactIdentity(result)
	compact["operation"] = operation
	return c.receipt(compact, map[string]any{"coverage": "write_response", "operation": operation, "content_verified": false})
}
