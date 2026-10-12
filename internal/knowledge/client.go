// Adapted from github.com/zhoushoujianwork/memgov/plugins/data/knowledge
// (Apache-2.0; see memgov-LICENSE). Modified for Agent-Host's pinned, read-only
// sources and attributed retrieval receipts; advanced Confluence tools omitted.
package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes      = 16 << 20
	maxToolResultBytes    = 64 << 10
	defaultReadCharacters = 8000
	maxReadCharacters     = 12000
)

var resourceID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)
var pageID = regexp.MustCompile(`^[0-9]{1,40}$`)
var highlightTags = regexp.MustCompile(`<[^>]*>`)
var highlightActive = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>`)
var highlightSpace = regexp.MustCompile(`\s+`)
var contentDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type sourceClient struct {
	source ResolvedSource
	http   *http.Client
}

func newSourceClient(source ResolvedSource) *sourceClient {
	return &sourceClient{source: source, http: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *sourceClient) request(ctx context.Context, method, endpoint string, body any) (map[string]any, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("knowledge request could not be encoded")
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, errors.New("invalid knowledge request")
	}
	req.Header.Set("Authorization", "Bearer "+c.source.Credential)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, errors.New("knowledge source request canceled")
		}
		var timeout interface{ Timeout() bool }
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			return nil, errors.New("knowledge source request timed out")
		}
		return nil, errors.New("knowledge source connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("knowledge source returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > maxResponseBytes {
		return nil, errors.New("knowledge source response exceeds 16 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, errors.New("knowledge source request canceled")
		}
		var timeout interface{ Timeout() bool }
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			return nil, errors.New("knowledge source request timed out")
		}
		return nil, errors.New("knowledge source response read failed")
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("knowledge source response exceeds 16 MiB")
	}
	var result map[string]any
	if err := decodeStrictJSON(raw, &result); err != nil || result == nil {
		return nil, errors.New("knowledge source returned invalid JSON")
	}
	return redactCredential(result, c.source.Credential).(map[string]any), nil
}

// Redact the connector credential even when an upstream response echoes it.
// This is not a classifier for arbitrary secrets appearing in source documents.
func redactCredential(value any, secret string) any {
	switch v := value.(type) {
	case string:
		if secret == "" {
			return v
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			v = strings.ReplaceAll(v, form, "[redacted]")
		}
		return v
	case []any:
		for i := range v {
			v[i] = redactCredential(v[i], secret)
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[redactCredential(k, secret).(string)] = redactCredential(item, secret)
		}
		return out
	default:
		return value
	}
}

func stringValue(value map[string]any, key string) string {
	v, _ := value[key].(string)
	return v
}

func (c *sourceClient) receipt(result map[string]any, coverage map[string]any) (map[string]any, error) {
	// Source-controlled metadata cannot impersonate the host's receipt.
	delete(result, "_agent_host")
	raw, _ := json.Marshal(result)
	digest := sha256.Sum256(raw)
	result["_agent_host"] = map[string]any{
		"source_instance": c.source.Source.Name, "provider": c.source.Source.Provider,
		"principal": c.source.Source.Principal, "source_fingerprint": c.source.Fingerprint,
		"fetched_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"content_sha256": hex.EncodeToString(digest[:]), "coverage": coverage,
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxToolResultBytes {
		return nil, errors.New("knowledge result exceeds the 64 KiB output limit; request a smaller result window")
	}
	return result, nil
}

// The external sources do not expose a common immutable snapshot API. Each
// excerpt request fetches the content again, then verifies the prior digest
// before returning the next character window. Offsets count Unicode characters,
// not bytes. A JSON excerpt is a window of a serialized value, not standalone JSON.
func contentExcerpt(content any, offset, limit int, expectedDigest string) (string, string, map[string]any, error) {
	text, ok := content.(string)
	encoding := "text"
	if !ok {
		raw, err := json.Marshal(content)
		if err != nil {
			return "", "", nil, errors.New("knowledge content could not be encoded")
		}
		text, encoding = string(raw), "json"
	}
	digest := sha256.Sum256([]byte(text))
	fullDigest := hex.EncodeToString(digest[:])
	if expectedDigest != "" && expectedDigest != fullDigest {
		return "", "", nil, errors.New("knowledge content changed since the previous excerpt; restart at offset 0")
	}
	characters := []rune(text)
	if offset > len(characters) {
		return "", "", nil, errors.New("offset exceeds returned source content length")
	}
	end := min(offset+limit, len(characters))
	coverage := map[string]any{
		"coverage": "returned_content_excerpt", "visibility": "configured_credentials",
		"offset": offset, "returned_characters": end - offset, "total_characters": len(characters),
		"truncated": offset != 0 || end < len(characters), "full_content_sha256": fullDigest,
		"digest_scope": "returned_upstream_content", "offset_unit": "unicode_characters",
	}
	if end < len(characters) {
		coverage["next_offset"] = end
	}
	return string(characters[offset:end]), encoding, coverage, nil
}

func validateReadWindow(offset, limit int, expectedDigest string) error {
	if offset < 0 || offset > maxResponseBytes || limit < 1 || limit > maxReadCharacters {
		return errors.New("read offset must be nonnegative and limit must be 1..12000 characters")
	}
	if expectedDigest != "" && !contentDigest.MatchString(expectedDigest) || offset > 0 && expectedDigest == "" {
		return errors.New("expected_content_sha256 must be a returned digest and is required after offset 0")
	}
	return nil
}

func pickFields(source map[string]any, names ...string) map[string]any {
	result := make(map[string]any)
	for _, name := range names {
		if value, exists := source[name]; exists {
			result[name] = value
		}
	}
	return result
}

func compactIdentity(source map[string]any) map[string]any {
	result := pickFields(source, "id", "resource_id", "name", "title", "type", "version", "created_at", "updated_at", "updatedAt", "modified_at", "workspace_id", "source_url")
	if stringValue(result, "title") == "" && stringValue(result, "name") != "" {
		result["title"] = result["name"]
	}
	return result
}

func compactPaging(source map[string]any) map[string]any {
	result := pickFields(source, "total", "totalSize", "total_count", "start", "limit", "size", "offset", "has_more", "hasMore", "next_cursor", "nextCursor", "next_offset", "nextPage", "complete", "partial", "truncated")
	if links, ok := source["_links"].(map[string]any); ok {
		result["_links"] = pickFields(links, "base", "webui", "next", "prev")
	}
	return result
}

func retainUpstreamCoverage(coverage, source map[string]any) {
	upstream := compactPaging(source)
	delete(upstream, "_links")
	if len(upstream) == 0 {
		return
	}
	coverage["upstream_coverage"] = upstream
	if upstream["truncated"] == true || upstream["partial"] == true || upstream["complete"] == false {
		coverage["truncated"] = true
	}
}

func compactSnippet(row, target map[string]any, name string) {
	highlight, ok := row[name].(string)
	if !ok {
		return
	}
	highlight = html.UnescapeString(highlight)
	highlight = highlightActive.ReplaceAllString(highlight, " ")
	highlight = highlightTags.ReplaceAllString(highlight, " ")
	runes := []rune(strings.TrimSpace(highlightSpace.ReplaceAllString(highlight, " ")))
	if len(runes) > 1200 {
		runes = runes[:1200]
		target[name+"_truncated"] = true
	}
	target[name] = string(runes)
}

func (c *sourceClient) dokki(ctx context.Context, method, path string, body any) (map[string]any, error) {
	return c.request(ctx, method, strings.TrimRight(c.source.Source.Endpoint, "/")+path, body)
}

func (c *sourceClient) dokkiSourceURL(kind, id string) string {
	routes := map[string]string{"document": "doc", "file": "file", "table": "table", "artifact": "artifact", "folder": "folder"}
	route := routes[strings.ToLower(kind)]
	if route == "" {
		return ""
	}
	base := c.source.Source.SourceURLBase
	if base == "" {
		u, _ := url.Parse(c.source.Source.Endpoint)
		base = (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	}
	return strings.TrimRight(base, "/") + "/" + route + "/" + url.PathEscape(id)
}

func (c *sourceClient) listDokkiWorkspaces(ctx context.Context, limit int) (map[string]any, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be 1..100")
	}
	result, err := c.dokki(ctx, http.MethodGet, fmt.Sprintf("/workspaces?limit=%d", limit), nil)
	if err != nil {
		return nil, err
	}
	rows, ok := result["workspaces"].([]any)
	if !ok || len(rows) > limit {
		return nil, errors.New("Dokki workspace response is invalid")
	}
	compactRows := make([]any, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("Dokki workspace response is invalid")
		}
		compactRows = append(compactRows, compactIdentity(row))
	}
	result = compactPaging(result)
	result["workspaces"] = compactRows
	return c.receipt(result, map[string]any{"visibility": "configured_credentials", "coverage": "requested_result_window", "limit": limit, "returned_count": len(rows)})
}

func (c *sourceClient) searchDokki(ctx context.Context, query, mode string, workspaces []string, limit int) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 2000 || limit < 1 || limit > 20 || mode != "keyword" && mode != "semantic" && mode != "hybrid" || len(workspaces) > 50 {
		return nil, errors.New("invalid Dokki search parameters")
	}
	for _, id := range workspaces {
		if !resourceID.MatchString(id) || id == "." || id == ".." {
			return nil, errors.New("invalid Dokki workspace ID")
		}
	}
	body := map[string]any{"query": query, "mode": mode, "limit": limit}
	if workspaces != nil {
		body["workspace_ids"] = workspaces
	}
	result, err := c.dokki(ctx, http.MethodPost, "/search", body)
	if err != nil {
		return nil, err
	}
	rows, ok := result["results"].([]any)
	if !ok || len(rows) > limit {
		return nil, errors.New("Dokki search response is invalid")
	}
	compactRows := make([]any, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("Dokki search hit is invalid")
		}
		id := stringValue(row, "resource_id")
		if !validResourceID(id) || stringValue(row, "title") == "" {
			return nil, errors.New("Dokki search hit is invalid")
		}
		compact := compactIdentity(row)
		delete(compact, "source_url")
		if sourceURL := c.dokkiSourceURL(stringValue(row, "type"), id); sourceURL != "" {
			compact["source_url"] = sourceURL
		}
		compactSnippet(row, compact, "highlight")
		compactRows = append(compactRows, compact)
	}
	result = compactPaging(result)
	result["results"] = compactRows
	coverage := map[string]any{"query": query, "query_mode": mode, "workspace_ids": workspaces,
		"visibility": "configured_credentials", "coverage": "requested_result_window", "limit": limit, "returned_count": len(rows),
		"limitation": "Only the requested query and result window visible to the configured credential were searched; no matches do not establish global absence."}
	return c.receipt(result, coverage)
}

func validResourceID(id string) bool { return resourceID.MatchString(id) && id != "." && id != ".." }

func (c *sourceClient) readDokkiResource(ctx context.Context, id string, offset, limit int, expectedDigest string) (map[string]any, error) {
	if !validResourceID(id) {
		return nil, errors.New("invalid Dokki resource ID")
	}
	if err := validateReadWindow(offset, limit, expectedDigest); err != nil {
		return nil, err
	}
	path := "/resources/" + url.PathEscape(id)
	detail, err := c.dokki(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resource, ok := detail["resource"].(map[string]any)
	if !ok || stringValue(resource, "id") != id {
		return nil, errors.New("Dokki resource identity mismatch")
	}
	body, err := c.dokki(ctx, http.MethodGet, path+"/content", nil)
	if err != nil {
		return nil, err
	}
	contentResource, ok := body["resource"].(map[string]any)
	if !ok || stringValue(contentResource, "id") != id || body["content"] == nil ||
		stringValue(resource, "type") != "" && stringValue(contentResource, "type") != "" && stringValue(resource, "type") != stringValue(contentResource, "type") {
		return nil, errors.New("Dokki content identity mismatch")
	}
	if resource["version"] != nil && contentResource["version"] != nil {
		before, _ := json.Marshal(resource["version"])
		after, _ := json.Marshal(contentResource["version"])
		if !bytes.Equal(before, after) {
			return nil, errors.New("Dokki resource version changed during read")
		}
	}
	excerpt, encoding, coverage, err := contentExcerpt(body["content"], offset, limit, expectedDigest)
	if err != nil {
		return nil, err
	}
	coverage["resource_id"] = id
	retainUpstreamCoverage(coverage, body)
	identity := compactIdentity(resource)
	contentIdentity := compactIdentity(contentResource)
	for name, value := range contentIdentity {
		if _, exists := identity[name]; !exists {
			identity[name] = value
		}
	}
	delete(identity, "source_url")
	detail = map[string]any{"resource": identity, "content": excerpt, "content_encoding": encoding}
	if sourceURL := c.dokkiSourceURL(stringValue(identity, "type"), id); sourceURL != "" {
		detail["source_url"] = sourceURL
	}
	return c.receipt(detail, coverage)
}

func (c *sourceClient) confluenceREST(ctx context.Context, path string) (map[string]any, error) {
	base := strings.TrimRight(c.source.Source.Endpoint, "/")
	if !strings.HasSuffix(base, "/rest/api") {
		base += "/rest/api"
	}
	result, err := c.request(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	webBase := strings.TrimSuffix(base, "/rest/api")
	return confluenceSourceURLs(result, webBase).(map[string]any), nil
}

func humanSourceURL(base, link string) string {
	if link == "" || strings.HasPrefix(link, "//") {
		return ""
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if !parsed.IsAbs() {
		parsed, err = url.Parse(strings.TrimRight(base, "/") + "/" + strings.TrimLeft(link, "/"))
	}
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" || strings.Contains(parsed.Path, "/rest/") || strings.Contains(parsed.Path, "/api/") {
		return ""
	}
	return parsed.String()
}

func confluenceSourceURLs(value any, base string) any {
	switch v := value.(type) {
	case []any:
		for i := range v {
			v[i] = confluenceSourceURLs(v[i], base)
		}
	case map[string]any:
		links, _ := v["_links"].(map[string]any)
		if b := stringValue(links, "base"); b != "" {
			base = b
		}
		sourceURL := humanSourceURL(base, stringValue(v, "url"))
		if sourceURL == "" {
			sourceURL = humanSourceURL(base, stringValue(links, "webui"))
		}
		delete(v, "source_url")
		for k := range v {
			v[k] = confluenceSourceURLs(v[k], base)
		}
		if sourceURL != "" {
			v["source_url"] = sourceURL
		}
	}
	return value
}

func (c *sourceClient) searchConfluence(ctx context.Context, query, mode string, limit, start int) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 2000 || strings.ContainsAny(query, "\r\n\x00") || mode != "keyword" && mode != "cql" || limit < 1 || limit > 50 || start < 0 || start > 10000 {
		return nil, errors.New("invalid Confluence search parameters")
	}
	cql := query
	if mode == "keyword" {
		cql = "type = page and text ~ " + strconv.Quote(query)
	}
	params := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}, "start": {strconv.Itoa(start)}}
	result, err := c.confluenceREST(ctx, "/content/search?"+params.Encode())
	if err != nil {
		return nil, err
	}
	rows, ok := result["results"].([]any)
	if !ok || len(rows) > limit {
		return nil, errors.New("Confluence search response is invalid")
	}
	compactRows := make([]any, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok || !pageID.MatchString(stringValue(row, "id")) || stringValue(row, "title") == "" {
			return nil, errors.New("Confluence search hit is invalid")
		}
		compact := compactIdentity(row)
		compactSnippet(row, compact, "excerpt")
		compactSnippet(row, compact, "highlight")
		compactRows = append(compactRows, compact)
	}
	coverage := map[string]any{"query": query, "query_mode": mode, "cql": cql,
		"visibility": "configured_credentials", "coverage": "requested_result_window", "start": start, "limit": limit, "returned_count": len(rows),
		"limitation": "Only the requested query and result window visible to the configured credential were searched; no matches do not establish global absence."}
	// Keep actual pagination/coverage, excluding unbounded source body extras.
	result = compactPaging(result)
	result["results"] = compactRows
	result["search_scope"] = coverage
	result["returned_count"] = len(rows)
	return c.receipt(result, coverage)
}

func (c *sourceClient) readConfluencePage(ctx context.Context, id string, offset, limit int, expectedDigest string) (map[string]any, error) {
	if !pageID.MatchString(id) {
		return nil, errors.New("Confluence page ID must be numeric")
	}
	if err := validateReadWindow(offset, limit, expectedDigest); err != nil {
		return nil, err
	}
	params := url.Values{"expand": {"body.storage,version,space"}}
	result, err := c.confluenceREST(ctx, "/content/"+url.PathEscape(id)+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if stringValue(result, "id") != id || stringValue(result, "type") != "page" {
		return nil, errors.New("Confluence page identity mismatch")
	}
	body, _ := result["body"].(map[string]any)
	storage, _ := body["storage"].(map[string]any)
	content, ok := storage["value"].(string)
	if !ok {
		return nil, errors.New("Confluence page body is unavailable")
	}
	excerpt, encoding, coverage, err := contentExcerpt(content, offset, limit, expectedDigest)
	if err != nil {
		return nil, err
	}
	coverage["page_id"] = id
	retainUpstreamCoverage(coverage, result)
	compact := compactIdentity(result)
	compact["content"], compact["content_encoding"], compact["content_format"] = excerpt, encoding, "confluence_storage"
	if space, ok := result["space"].(map[string]any); ok {
		compact["space"] = pickFields(space, "id", "key", "name")
	}
	return c.receipt(compact, coverage)
}
