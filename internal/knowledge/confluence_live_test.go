package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfluenceAuthorizedPersonalSpaceLive(t *testing.T) {
	if os.Getenv("AGH_CONFLUENCE_WRITE_LIVE") != "1" {
		t.Skip("explicit live acceptance only")
	}
	config, space := os.Getenv("AGH_CONFLUENCE_CONFIG"), os.Getenv("AGH_CONFLUENCE_SPACE")
	if config == "" || space == "" {
		t.Fatal("live test requires AGH_CONFLUENCE_CONFIG and AGH_CONFLUENCE_SPACE")
	}
	sources, err := Select(config, "default")
	if err != nil || len(sources) != 1 {
		t.Fatalf("configured source unavailable: %v", err)
	}
	source := sources[0]
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := callSourceTool(context.Background(), source, name, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	title := "Agent-Host Confluence create-update test " + time.Now().UTC().Format("20060102T150405Z")
	body := "<p>Agent-Host plugin acceptance test. Created in the owner's personal space with explicit authorization.</p>"
	created := call("create_confluence_page", map[string]any{"space_key": space, "title": title, "content": body})
	id := stringValue(created, "id")
	t.Logf("CREATED page_id=%s source_url=%s", id, stringValue(created, "source_url"))
	read := call("read_confluence_page", map[string]any{"id": id})
	if stringValue(read, "content") != body {
		t.Fatal("created body mismatch")
	}
	updatedBody := body + "<p>Update verified: 新增和更新页面测试。</p>"
	updated := call("update_confluence_page", map[string]any{"id": id, "expected_version": 1, "title": title + " (verified)", "content": updatedBody})
	read = call("read_confluence_page", map[string]any{"id": id})
	if stringValue(read, "content") != updatedBody || stringValue(read, "title") != title+" (verified)" {
		t.Fatal("updated page readback mismatch")
	}
	raw, _ := json.Marshal(map[string]any{"id": id, "expected_version": 1, "title": title, "content": "<p>stale</p>"})
	_, err = callSourceTool(context.Background(), source, "update_confluence_page", raw)
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("stale update did not conflict: %v", err)
	}
	fmt.Printf("LIVE_VERIFIED page_id=%s version=%v source_url=%s create_readback=true update_readback=true stale_conflict=true\n", id, updated["version"].(map[string]any)["number"], stringValue(updated, "source_url"))
}
