package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const initializeFixture = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`

func mcpFixtureSource(t *testing.T, provider, endpoint string) (string, ResolvedSource) {
	t.Helper()
	dir := t.TempDir()
	credential := filepath.Join(dir, "credential.json")
	field := "api_key"
	if provider == "confluence" {
		field = "pat"
	}
	raw, _ := json.Marshal(map[string]string{field: "fixture-private-token"})
	if err := os.WriteFile(credential, raw, 0600); err != nil {
		t.Fatal(err)
	}
	source := Source{Name: "team-docs", Provider: provider, Endpoint: endpoint, CredentialFile: credential, Principal: "owner", Description: "Team reference documents", Projects: []string{"default"}}
	config := filepath.Join(dir, "sources.json")
	raw, _ = json.Marshal(Configuration{Version: 1, Sources: []Source{source}})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := Select(config, "default")
	if err != nil || len(selected) != 1 {
		t.Fatalf("source fixture: %v %#v", err, selected)
	}
	return config, selected[0]
}

func decodeMCPReplies(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var replies []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var reply map[string]any
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatalf("bad MCP response %q: %v", line, err)
		}
		replies = append(replies, reply)
	}
	return replies
}

func TestMCPOnlyAdvertisesAndDispatchesSelectedTools(t *testing.T) {
	for _, provider := range []string{"dokki", "confluence"} {
		t.Run(provider, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, `{"results":[]}`) }))
			defer server.Close()
			config, source := mcpFixtureSource(t, provider, server.URL)
			other := "search_dokki"
			if provider == "dokki" {
				other = "search_confluence"
			}
			input := strings.Join([]string{
				initializeFixture,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
				fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{"query":"x"}}}`, other),
				`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"query_confluence","arguments":{"tool":"confluence_export","arguments":{}}}}`,
				`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"confluence://page-export-pdf/42"}}`,
			}, "\n")
			var output bytes.Buffer
			if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), &output); err != nil {
				t.Fatal(err)
			}
			replies := decodeMCPReplies(t, output.Bytes())
			if len(replies) != 5 || requests.Load() != 0 {
				t.Fatalf("listing/isolation triggered network or malformed replies: %s", output.String())
			}
			tools := replies[1]["result"].(map[string]any)["tools"].([]any)
			want := 4
			if provider == "dokki" {
				want = 3
			}
			if len(tools) != want {
				t.Fatalf("wrong selected tools: %#v", tools)
			}
			for _, item := range tools {
				tool := item.(map[string]any)
				write := strings.HasPrefix(tool["name"].(string), "create_") || strings.HasPrefix(tool["name"].(string), "update_")
				if !toolAllowed(provider, tool["name"].(string)) || tool["annotations"].(map[string]any)["readOnlyHint"] != !write {
					t.Fatalf("unselected tool or incorrect write annotation: %#v", tool)
				}
			}
			for _, reply := range replies[2:] {
				if reply["error"] == nil || reply["result"] != nil {
					t.Fatalf("forbidden operation was accepted: %#v", reply)
				}
			}
			if strings.Contains(output.String(), source.Credential) || strings.Contains(output.String(), source.Source.CredentialFile) {
				t.Fatal("credential leaked to MCP output")
			}
		})
	}
}

func TestMCPRealHTTPCallAndToolErrorHaveDistinctWireResults(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.Contains(r.URL.Query().Get("cql"), "denied") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "fixture-private-token")
			return
		}
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	config, source := mcpFixtureSource(t, "confluence", server.URL)
	input := initializeFixture + "\n" + strings.Join([]string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"missing"},"_meta":{"progressToken":"p1"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"denied"}}}`,
	}, "\n")
	var output bytes.Buffer
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	replies := decodeMCPReplies(t, output.Bytes())
	zero := replies[1]["result"].(map[string]any)
	failed := replies[2]["result"].(map[string]any)
	if zero["isError"] != false || zero["structuredContent"] == nil || failed["isError"] != true || failed["structuredContent"] != nil || requests.Load() != 2 {
		t.Fatalf("zero-hit/error confused: %s", output.String())
	}
	if !strings.Contains(output.String(), "HTTP 403") || strings.Contains(output.String(), "fixture-private-token") {
		t.Fatalf("error missing or secret leaked: %s", output.String())
	}
}

func TestMCPRejectsMalformedProtocolAndArgumentsWithoutNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	config, source := mcpFixtureSource(t, "confluence", server.URL)
	cases := []string{
		`{`,
		`{"jsonrpc":"2.0","id":2,"method":"ping","method":"tools/list"}`,
		`{"JSONRPC":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":null}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"unsupported"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"x","endpoint":"https://other.test"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"x","query":"y"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"Query":"x"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"x","limit":null}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"x","limit":0}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_confluence_page","arguments":{"id":"../secret"}}}`,
	}
	for i, request := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var output bytes.Buffer
			if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(initializeFixture+"\n"+request), &output); err != nil {
				t.Fatal(err)
			}
			replies := decodeMCPReplies(t, output.Bytes())
			if len(replies) != 2 {
				t.Fatalf("missing error: %s", output.String())
			}
			last := replies[1]
			if last["error"] == nil {
				result, ok := last["result"].(map[string]any)
				if !ok || result["isError"] != true {
					t.Fatalf("invalid request accepted: %s", output.String())
				}
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("invalid request reached source")
	}
}

type mutateAfterWrite struct {
	bytes.Buffer
	mutate func()
}

func (w *mutateAfterWrite) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if w.mutate != nil {
		mutate := w.mutate
		w.mutate = nil
		mutate()
	}
	return n, err
}

func TestMCPRevalidatesCredentialAndSourceAccessBeforeCall(t *testing.T) {
	for _, change := range []string{"remove-source", "rotate-credential"} {
		t.Run(change, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
			defer server.Close()
			config, source := mcpFixtureSource(t, "confluence", server.URL)
			output := &mutateAfterWrite{mutate: func() {
				path, raw := config, []byte(`{"version":1,"sources":[]}`)
				if change == "rotate-credential" {
					path, raw = source.Source.CredentialFile, []byte(`{"pat":"replacement-token"}`)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}}
			input := initializeFixture + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"x"}}}`
			if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), output); err != nil {
				t.Fatal(err)
			}
			replies := decodeMCPReplies(t, output.Bytes())
			if requests.Load() != 0 || replies[1]["result"].(map[string]any)["isError"] != true {
				t.Fatalf("changed access reached source: %s", output.String())
			}
		})
	}
}

func TestMCPRequiresFingerprintAndInitialization(t *testing.T) {
	config, source := mcpFixtureSource(t, "confluence", "https://unreachable.example.test")
	var output bytes.Buffer
	if err := Serve(context.Background(), config, source.Source.Name, "", strings.NewReader(""), &output); err == nil {
		t.Fatal("unpinned launch accepted")
	}
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &output); err != nil {
		t.Fatal(err)
	}
	replies := decodeMCPReplies(t, output.Bytes())
	if replies[0]["error"] == nil {
		t.Fatal("uninitialized tool listing accepted")
	}
}

func TestStrictJSONPreservesNumbersAndRejectsExcessiveNesting(t *testing.T) {
	var result map[string]any
	if err := decodeStrictJSON([]byte(`{"id":9007199254740993}`), &result); err != nil || result["id"] != json.Number("9007199254740993") {
		t.Fatalf("source number changed: %#v %v", result, err)
	}
	for _, raw := range []string{`{} {}`, `{"x":1,"x":2}`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		var value any
		if err := decodeStrictJSON([]byte(raw), &value); err == nil {
			t.Fatal("ambiguous or unbounded JSON accepted")
		}
	}
}

func TestMCPReadReturnsSmallAttributedExcerptAndAdvertisesContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{"resource": map[string]any{"id": "large", "type": "document", "name": "Guide"}, "content": map[string]any{"document": strings.Repeat("reference body ", 50000)}}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	config, source := mcpFixtureSource(t, "dokki", server.URL)
	input := initializeFixture + "\n" + strings.Join([]string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_dokki_resource","arguments":{"resource_id":"large","offset":0,"limit":2000}}}`,
	}, "\n")
	var output bytes.Buffer
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	replies := decodeMCPReplies(t, output.Bytes())
	readTool := replies[1]["result"].(map[string]any)["tools"].([]any)[2].(map[string]any)
	properties := readTool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if properties["offset"] == nil || properties["limit"] == nil || properties["expected_content_sha256"] == nil || !strings.Contains(readTool["description"].(string), "next_offset") {
		t.Fatal("read continuation is undiscoverable")
	}
	result := replies[2]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("large read failed instead of returning requested excerpt: %s", output.String())
	}
	structured := result["structuredContent"].(map[string]any)
	coverage := structured["_agent_host"].(map[string]any)["coverage"].(map[string]any)
	if coverage["truncated"] != true || coverage["returned_characters"] != float64(2000) || coverage["next_offset"] != float64(2000) || len(coverage["full_content_sha256"].(string)) != 64 {
		t.Fatalf("partial coverage missing: %#v", coverage)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"_agent_host"`) || !strings.Contains(text, `"full_content_sha256"`) {
		t.Fatal("inline tool text lost retrieval receipt")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		if len(line) > maxToolResultBytes {
			t.Fatal("MCP response exceeded hard output bound")
		}
	}
	if len(output.Bytes()) > 16<<10 {
		t.Fatalf("large source leaked into native tool output: %d bytes", len(output.Bytes()))
	}
}

func TestMCPEnforcesEncodedWireBoundIncludingDuplicateStructuredContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{"id": "42", "type": "page", "title": "Guide", "body": map[string]any{"storage": map[string]any{"value": strings.Repeat("🙂", maxReadCharacters)}}}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	config, source := mcpFixtureSource(t, "confluence", server.URL)
	input := initializeFixture + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_confluence_page","arguments":{"id":"42","limit":12000}}}`
	var output bytes.Buffer
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	replies := decodeMCPReplies(t, output.Bytes())
	result := replies[1]["result"].(map[string]any)
	if result["isError"] != true || result["structuredContent"] != nil || !strings.Contains(output.String(), "64 KiB output limit") {
		t.Fatal("wire expansion bypassed output bound")
	}
	if len(output.Bytes()) > maxToolResultBytes {
		t.Fatal("oversized failure response")
	}
}
