package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Explicitly selected credentials, one bounded search and at most one read.
// This gate never creates, updates or publishes remote content.
func TestConfiguredKnowledgeReadOnlyLive(t *testing.T) {
	if os.Getenv("AGH_KNOWLEDGE_READ_LIVE") != "1" {
		t.Skip("explicit read-only live acceptance only")
	}
	path, name, query := os.Getenv("AGH_KNOWLEDGE_READ_CONFIG"), os.Getenv("AGH_KNOWLEDGE_READ_SOURCE"), os.Getenv("AGH_KNOWLEDGE_READ_QUERY")
	if path == "" || name == "" || query == "" {
		t.Fatal("live acceptance requires explicit config, source and query")
	}
	sources, err := Select(path, "default")
	if err != nil {
		t.Fatal(err)
	}
	var selected *ResolvedSource
	for i := range sources {
		if sources[i].Source.Name == name {
			selected = &sources[i]
		}
	}
	if selected == nil {
		t.Fatal("selected source is not available")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	search, read, id := "", "", ""
	switch selected.Source.Provider {
	case "confluence":
		search, read, id = "search_confluence", "read_confluence_page", "id"
	case "dokki":
		search, read, id = "search_dokki", "read_dokki_resource", "resource_id"
	case "local_markdown":
		search, read, id = "search_local_markdown", "read_local_markdown", "path"
	default:
		t.Fatal("unsupported read-only acceptance provider")
	}
	raw, _ := json.Marshal(map[string]any{"query": query, "limit": 2})
	result, err := callSourceTool(ctx, *selected, search, raw)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := result["results"].([]any)
	if len(rows) == 0 {
		t.Logf("LIVE_READ provider=%s search=true returned=0 read=not_exercised", selected.Source.Provider)
		return
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatal("invalid search row")
	}
	reference, _ := row[id].(string)
	if reference == "" {
		t.Fatal("search result lacks readable identity")
	}
	if selected.Source.Provider == "confluence" && row["type"] == "blogpost" {
		read = "read_confluence_blogpost"
	}
	raw, _ = json.Marshal(map[string]any{id: reference, "limit": 128})
	if _, err := callSourceTool(ctx, *selected, read, raw); err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE_READ provider=%s search=true returned=%d read=true writes=0", selected.Source.Provider, len(rows))
}
