package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	return confluenceContentWriteTool(update, "page")
}

func confluenceContentWriteTool(update bool, kind string) mcpTool {
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
	if kind == "blogpost" {
		name = strings.ReplaceAll(name, "_page", "_blogpost")
		description = strings.ReplaceAll(description, "page", "blog post")
		delete(properties, "parent_id")
		if !update {
			required = []string{"title", "content"}
			description = "Create a Confluence blog post for user-authorized Agent-Host-managed content. Omit space_key to use the authenticated user's personal blog. Prefer this location for automatically managed notes and reports unless the user requests another location. Blog posts have no parent page. Returns identity and version; read the blog post to verify content. Never automatically retry a failed or uncertain write; inspect the target first."
		}
	}
	return mcpTool{Name: name, Description: description, InputSchema: inputSchema(properties, required...),
		Annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": update, "idempotentHint": false, "openWorldHint": true}}
}

func (c *sourceClient) writeConfluencePage(ctx context.Context, args confluenceWriteArgs, update bool) (map[string]any, error) {
	return c.writeConfluenceContent(ctx, args, update, "page")
}

func (c *sourceClient) writeConfluenceContent(ctx context.Context, args confluenceWriteArgs, update bool, kind string) (map[string]any, error) {
	if kind != "page" && kind != "blogpost" || kind == "blogpost" && args.ParentID != "" {
		return nil, errors.New("invalid Confluence content type or blog parent")
	}
	if strings.TrimSpace(args.Title) == "" || len(args.Title) > 1000 || len(args.Content) > maxConfluenceWriteBytes || strings.ContainsAny(args.Title, "\x00\r\n") || strings.ContainsRune(args.Content, 0) {
		return nil, errors.New("invalid Confluence title or storage body")
	}
	body := map[string]any{"type": kind, "title": args.Title,
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
		if kind == "blogpost" && args.SpaceKey == "" {
			key, err := c.confluencePersonalSpace(ctx)
			if err != nil {
				return nil, err
			}
			args.SpaceKey = key
		}
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
	if !pageID.MatchString(stringValue(result, "id")) || update && stringValue(result, "id") != args.ID || stringValue(result, "type") != kind || stringValue(result, "title") == "" || versionErr != nil || actualVersion != wantVersion {
		return nil, errors.New("Confluence write response identity/version is invalid; the write may have succeeded: inspect the target before any retry")
	}
	compact := compactIdentity(result)
	compact["operation"] = operation
	return c.receipt(compact, map[string]any{"coverage": "write_response", "operation": operation, "content_verified": false})
}

func (c *sourceClient) confluencePersonalSpace(ctx context.Context) (string, error) {
	user, err := c.confluenceREST(ctx, "/user/current")
	if err != nil {
		return "", fmt.Errorf("Confluence personal blog lookup failed before create: %w", err)
	}
	username := stringValue(user, "username")
	if strings.TrimSpace(username) == "" || len(username) > 256 || strings.ContainsAny(username, "\x00\r\n") {
		return "", errors.New("Confluence current username unavailable; supply an explicit authorized space_key")
	}
	key := "~" + username
	space, err := c.confluenceREST(ctx, "/space/"+url.PathEscape(key))
	if err != nil {
		return "", fmt.Errorf("Confluence personal space unavailable before create: %w", err)
	}
	if stringValue(space, "key") != key || stringValue(space, "type") != "personal" {
		return "", errors.New("Confluence personal space identity mismatch; no blog post created")
	}
	return key, nil
}
