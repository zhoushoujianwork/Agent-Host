package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfluenceMCPCreateUpdateAndReadback(t *testing.T) {
	version := 0
	title, content := "", ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-private-token" {
			t.Error("missing credential")
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["type"] != "page" || r.Header.Get("Content-Type") != "application/json" {
				t.Error("incorrect write payload")
			}
			if r.Method == http.MethodPost {
				if r.URL.Path != "/wiki/rest/api/content" || body["space"].(map[string]any)["key"] != "~owner" || body["ancestors"].([]any)[0].(map[string]any)["id"] != "7" {
					t.Error("incorrect create target")
				}
			} else if r.URL.Path != "/wiki/rest/api/content/42" || body["id"] != "42" || body["version"].(map[string]any)["number"] != float64(version+1) {
				w.WriteHeader(http.StatusConflict)
				return
			}
			storage := body["body"].(map[string]any)["storage"].(map[string]any)
			if storage["representation"] != "storage" {
				t.Error("incorrect body format")
			}
			title, content = body["title"].(string), storage["value"].(string)
			version++
		case http.MethodGet:
		default:
			t.Error("unexpected method")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "42", "type": "page", "title": title, "version": map[string]any{"number": version}, "body": map[string]any{"storage": map[string]any{"value": content}}, "_links": map[string]any{"webui": "/pages/viewpage.action?pageId=42"}})
	}))
	defer server.Close()
	config, source := mcpFixtureSource(t, "confluence", server.URL+"/wiki/rest/api")
	requests := []string{
		initializeFixture,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_confluence_page","arguments":{"space_key":"~owner","parent_id":"7","title":"Test","content":"<p>创建</p>"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"update_confluence_page","arguments":{"id":"42","expected_version":1,"title":"Updated","content":"<p>更新</p>"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_confluence_page","arguments":{"id":"42"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"update_confluence_page","arguments":{"id":"42","expected_version":1,"title":"Stale","content":"stale"}}}`,
	}
	var output bytes.Buffer
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(strings.Join(requests, "\n")), &output); err != nil {
		t.Fatal(err)
	}
	replies := decodeMCPReplies(t, output.Bytes())
	for _, reply := range replies[1:4] {
		if reply["result"].(map[string]any)["isError"] != false {
			t.Fatalf("write/read failed: %s", output.String())
		}
	}
	read := replies[3]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if read["content"] != "<p>更新</p>" || read["title"] != "Updated" || version != 2 || replies[4]["result"].(map[string]any)["isError"] != true || !strings.Contains(output.String(), "HTTP 409") {
		t.Fatalf("readback or stale write handling failed: %s", output.String())
	}
}

func TestConfluenceWritesRejectInvalidArgumentsBeforeNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid write reached server") }))
	defer server.Close()
	c := fixtureClient("confluence", server.URL)
	cases := []struct{ name, args string }{
		{"create_confluence_page", `{"space_key":"X","title":"T"}`},
		{"create_confluence_page", `{"space_key":"X","title":"T","content":"","id":""}`},
		{"create_confluence_page", `{"space_key":"X","title":"T","content":"","parent_id":"../7"}`},
		{"update_confluence_page", `{"id":"42","title":"T","content":"","expected_version":0}`},
		{"update_confluence_page", `{"id":"42","title":"T","content":"","expected_version":1,"endpoint":"https://other"}`},
		{"update_confluence_page", `{"id":"42","title":"T","content":"","expected_version":null}`},
		{"update_confluence_page", `{"id":"42","title":"T","content":"","expected_version":1.5}`},
		{"update_confluence_page", `{"id":"../42","title":"T","content":"","expected_version":1}`},
	}
	for _, tc := range cases {
		if _, err := callSourceTool(context.Background(), c.source, tc.name, json.RawMessage(tc.args)); err == nil {
			t.Errorf("invalid write accepted: %s", tc.args)
		}
	}
	if _, err := c.writeConfluencePage(context.Background(), confluenceWriteArgs{SpaceKey: "X", Title: "T", Content: strings.Repeat("x", maxConfluenceWriteBytes+1)}, false); err == nil {
		t.Error("oversized body accepted")
	}
}

func TestConfluenceWriteFailuresAreExplicitAndNeverRetried(t *testing.T) {
	for _, status := range []int{403, 409, 500, 302, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"id":"wrong","type":"page","title":"fixture-private-token"}`)
			}))
			defer server.Close()
			_, err := fixtureClient("confluence", server.URL).writeConfluencePage(context.Background(), confluenceWriteArgs{SpaceKey: "X", Title: "T", Content: ""}, false)
			if err == nil || calls != 1 || !strings.Contains(err.Error(), "inspect the target") || strings.Contains(err.Error(), "fixture-private-token") {
				t.Fatalf("failure hidden, retried or leaked: calls=%d err=%v", calls, err)
			}
		})
	}
}
