package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localFixture(t *testing.T) (string, Configuration, ResolvedSource) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	writeKnowledgePrivate(t, filepath.Join(root, "docs", "service.md"), "# Service guide\nCBS 同步服务。部署定位用于参考，不能证明当前运行状态。\n")
	c := Configuration{Version: 1, Sources: []Source{{Name: "local_docs", Provider: "local_markdown", Root: root, Paths: []string{"docs"}, Principal: "local-owner", Description: "Existing project reference", Projects: []string{"default", "alpha"}}}}
	config := filepath.Join(root, "knowledge.json")
	writeKnowledgeConfig(t, config, c)
	r, err := ResolveConfiguredSource(c.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	return config, c, r
}

func TestLocalSourceSelectionAppliesAudienceProjectAndInstanceBeforeAccess(t *testing.T) {
	config, c, _ := localFixture(t)
	private := c.Sources[0]
	private.Name, private.Audiences = "owner_notes", []string{"owner"}
	private.Root = filepath.Join(filepath.Dir(config), "unavailable-notes")
	c.Sources = append(c.Sources, private)
	writeKnowledgeConfig(t, config, c)
	if _, err := Load(config); err != nil {
		t.Fatalf("unselected local metadata was eagerly inspected: %v", err)
	}
	for _, audience := range []string{"group-one", "group-two"} {
		selected, err := SelectAgentAudienceProjects(config, []string{"alpha"}, nil, audience)
		if err != nil || len(selected) != 1 || selected[0].Source.Name != "local_docs" {
			t.Fatalf("group inherited owner notes or invalid root blocked it: %+v %v", selected, err)
		}
	}
	if _, err := SelectAgentAudienceProjects(config, []string{"alpha"}, nil, "owner"); err == nil {
		t.Fatal("selected owner's unavailable notes were silently omitted")
	}
	selected, err := SelectAgentAudienceProjects(config, []string{"alpha"}, []string{"local_docs"}, "owner")
	if err != nil || len(selected) != 1 {
		t.Fatalf("instance narrowing failed before root inspection: %+v %v", selected, err)
	}
	selected, err = SelectAgentAudienceProjects(config, []string{"beta"}, nil, "owner")
	if err != nil || len(selected) != 0 {
		t.Fatalf("unselected project root inspected: %+v %v", selected, err)
	}
	private.Root = c.Sources[0].Root
	c.Sources[1] = private
	writeKnowledgeConfig(t, config, c)
	for _, selectOwner := range []func() ([]ResolvedSource, error){
		func() ([]ResolvedSource, error) { return Select(config, "alpha") },
		func() ([]ResolvedSource, error) { return SelectProjects(config, []string{"alpha"}) },
		func() ([]ResolvedSource, error) { return SelectAgentProjects(config, []string{"alpha"}, nil) },
		func() ([]ResolvedSource, error) {
			return SelectAgentAudienceProjects(config, []string{"alpha"}, nil, "")
		},
	} {
		selected, err := selectOwner()
		if err != nil || len(selected) != 2 {
			t.Fatalf("legacy local owner default changed: %+v %v", selected, err)
		}
	}
	if _, err := ResolveConfiguredSource(private); err != nil {
		t.Fatalf("operator cannot explicitly check restricted source: %v", err)
	}
}

func TestLocalConfigurationRejectsBroadPathsAndMixedProviders(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		change func(*Source)
	}{
		{"no paths", func(s *Source) { s.Paths = nil }},
		{"all root", func(s *Source) { s.Paths = []string{"."} }},
		{"traversal", func(s *Source) { s.Paths = []string{"docs/../notes"} }},
		{"absolute", func(s *Source) { s.Paths = []string{"/docs"} }},
		{"hidden", func(s *Source) { s.Paths = []string{"docs/.git"} }},
		{"legacy hidden", func(s *Source) { s.Paths = []string{".local-legacy"} }},
		{"separator", func(s *Source) { s.Paths = []string{`docs\notes`} }},
		{"duplicates", func(s *Source) { s.Paths = []string{"docs", "docs"} }},
		{"control", func(s *Source) { s.Paths = []string{"docs/line\nbreak"} }},
		{"local credential", func(s *Source) { s.CredentialFile = "/secret/token" }},
		{"local endpoint", func(s *Source) { s.Endpoint = "https://example.test" }},
		{"local citation base", func(s *Source) { s.SourceURLBase = "https://example.test" }},
		{"relative root", func(s *Source) { s.Root = "docs" }},
		{"duplicate audience", func(s *Source) { s.Audiences = []string{"owner", "owner"} }},
		{"unsafe audience", func(s *Source) { s.Audiences = []string{"../owner"} }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			config, c, _ := localFixture(t)
			mutation.change(&c.Sources[0])
			writeKnowledgeConfig(t, config, c)
			if _, err := Load(config); err == nil {
				t.Fatal("unsafe or ignored local configuration accepted")
			}
		})
	}
	config, _, _ := localFixture(t)
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"projects":`), []byte(`"audiences":[],"projects":`), 1)
	if err := os.WriteFile(config, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(config); err == nil {
		t.Fatal("explicit empty audience list accepted")
	}
	remotePath, remote := knowledgeConfigFixture(t)
	remote.Sources[0].Root = "/unused"
	writeKnowledgeConfig(t, remotePath, remote)
	if _, err := Load(remotePath); err == nil {
		t.Fatal("remote provider ignored local fields")
	}
}

func TestLocalSearchAndReadPreserveUnicodeSourceAndChangedContinuation(t *testing.T) {
	_, c, source := localFixture(t)
	writeKnowledgePrivate(t, filepath.Join(c.Sources[0].Root, "docs", "other.md"), "# Other\nUnrelated material.")
	writeKnowledgePrivate(t, filepath.Join(c.Sources[0].Root, "docs", "事件.md"), "# 同步事件\n服务 CBS reported resolved by a participant. 🌟 同步状态待确认。")
	client := newSourceClient(source)
	search, err := client.searchLocalMarkdown(context.Background(), "cBs 同步", 10)
	if err != nil || search["returned_count"] != 2 {
		t.Fatalf("Unicode/case-insensitive search failed: %+v %v", search, err)
	}
	rows := search["results"].([]any)
	for _, item := range rows {
		row := item.(map[string]any)
		if !strings.HasPrefix(row["path"].(string), "docs/") || len(row["content_sha256"].(string)) != 64 {
			t.Fatalf("source-relative provenance missing: %+v", row)
		}
	}
	raw, _ := json.Marshal(search)
	if strings.Contains(string(raw), source.Source.Root) || strings.Contains(string(raw), "knowledge.json") {
		t.Fatal("local root leaked through search receipt")
	}
	first, err := client.readLocalMarkdown(context.Background(), "docs/事件.md", 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	coverage := first["_agent_host"].(map[string]any)["coverage"].(map[string]any)
	if coverage["returned_characters"] != 10 || coverage["next_offset"] != 10 || coverage["digest_scope"] != "local_document_utf8" || coverage["visibility"] != "configured_paths" {
		t.Fatalf("Unicode excerpt coverage missing: %+v", coverage)
	}
	digest := coverage["full_content_sha256"].(string)
	second, err := client.readLocalMarkdown(context.Background(), "docs/事件.md", 10, 1000, digest)
	if err != nil || !strings.Contains(second["content"].(string), "reported resolved by a participant") {
		t.Fatalf("raw attribution altered or continuation failed: %+v %v", second, err)
	}
	if _, err := client.readLocalMarkdown(context.Background(), "docs/事件.md", 10, 1000, ""); err == nil {
		t.Fatal("continuation without version accepted")
	}
	writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", "事件.md"), "# Changed\nA new document body")
	if _, err := client.readLocalMarkdown(context.Background(), "docs/事件.md", 10, 1000, digest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed document silently mixed into continuation: %v", err)
	}
	zero, err := client.searchLocalMarkdown(context.Background(), "unmatched-fixture-value", 10)
	if err != nil || zero["returned_count"] != 0 || zero["_agent_host"].(map[string]any)["coverage"].(map[string]any)["limitation"] == nil {
		t.Fatalf("zero hits lack explicit coverage: %+v %v", zero, err)
	}
}

func TestLocalReadRejectsTraversalHiddenSymlinkSizeAndUnsupportedFiles(t *testing.T) {
	_, c, source := localFixture(t)
	client := newSourceClient(source)
	writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "outside.md"), "Private source outside configured paths")
	writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", "data.txt"), "Unconfigured file type")
	for _, relative := range []string{"../outside.md", "/etc/passwd", "docs/../outside.md", "outside.md", "docs/.hidden.md", "docs\\service.md", "docs/data.txt", "docs"} {
		if _, err := client.readLocalMarkdown(context.Background(), relative, 0, 100, ""); err == nil {
			t.Fatalf("forbidden path accepted: %q", relative)
		}
	}
	if err := os.Symlink(filepath.Join(source.Source.Root, "outside.md"), filepath.Join(source.Source.Root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.readLocalMarkdown(context.Background(), "docs/link.md", 0, 100, ""); err == nil {
		t.Fatal("symlink read escaped allowlist")
	}
	if _, err := client.searchLocalMarkdown(context.Background(), "private", 10); err == nil {
		t.Fatal("scan silently followed or omitted unsafe symlink")
	}
	if err := os.Remove(filepath.Join(source.Source.Root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source.Source.Root, filepath.Join(source.Source.Root, "docs", "nested")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.readLocalMarkdown(context.Background(), "docs/nested/outside.md", 0, 100, ""); err == nil {
		t.Fatal("directory symlink escaped allowlist")
	}
	if err := os.Remove(filepath.Join(source.Source.Root, "docs", "nested")); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(source.Source.Root, "docs", "large.md")
	writeKnowledgePrivate(t, oversized, strings.Repeat("x", maxLocalFileBytes+1))
	// Assembly checks metadata, without reading document content or sizing every file.
	if _, err := ResolveConfiguredSource(c.Sources[0]); err != nil {
		t.Fatalf("assembly unexpectedly read document bodies: %v", err)
	}
	if _, err := client.readLocalMarkdown(context.Background(), "docs/large.md", 0, 100, ""); err == nil {
		t.Fatal("oversized source read")
	}
	if _, err := client.searchLocalMarkdown(context.Background(), "x", 10); err == nil {
		t.Fatal("oversized source search treated as complete")
	}
	if err := os.Remove(oversized); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"invalid\x00text", "invalid\xfftext"} {
		writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", "invalid.md"), body)
		if _, err := client.readLocalMarkdown(context.Background(), "docs/invalid.md", 0, 100, ""); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
}

func TestLocalPinIncludesAuthorizedPathsRootAndAudienceButNotContent(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*testing.T, *Configuration)
	}{
		{"audience", func(_ *testing.T, c *Configuration) { c.Sources[0].Audiences = []string{"owner"} }},
		{"paths", func(_ *testing.T, c *Configuration) { c.Sources[0].Paths = []string{"docs/service.md"} }},
		{"root", func(t *testing.T, c *Configuration) {
			_, other, _ := localFixture(t)
			c.Sources[0].Root = other.Sources[0].Root
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			config, c, before := localFixture(t)
			change.mutate(t, &c)
			writeKnowledgeConfig(t, config, c)
			if _, err := ResolveSource(config, "local_docs", before.Fingerprint); err == nil {
				t.Fatal("stale source authorization accepted")
			}
		})
	}
	config, _, source := localFixture(t)
	writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", "service.md"), "# Updated reference")
	if current, err := ResolveSource(config, "local_docs", source.Fingerprint); err != nil || current.Fingerprint != source.Fingerprint {
		t.Fatalf("document edit rotated whole runtime instead of excerpt version: %v", err)
	}
	if err := os.Rename(filepath.Join(source.Source.Root, "docs"), filepath.Join(source.Source.Root, "removed")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSource(config, "local_docs", source.Fingerprint); err == nil {
		t.Fatal("removed configured path remained available")
	}
}

func TestLocalSearchRetainsBoundedCoverageAndHonorsCancellation(t *testing.T) {
	_, _, source := localFixture(t)
	client := newSourceClient(source)
	for i := 0; i < maxLocalScanFiles+5; i++ {
		writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", fmt.Sprintf("entry-%04d.md", i)), "# Entry\nneedle source data")
	}
	result, err := client.searchLocalMarkdown(context.Background(), "needle", 3)
	if err != nil {
		t.Fatal(err)
	}
	coverage := result["_agent_host"].(map[string]any)["coverage"].(map[string]any)
	if result["returned_count"] != 3 || coverage["scan_limit_reached"] != true || coverage["truncated"] != true || coverage["scanned_files"].(int) > maxLocalScanFiles {
		t.Fatalf("bounded scan overclaimed coverage: %+v", coverage)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.searchLocalMarkdown(ctx, "needle", 3); err == nil {
		t.Fatal("canceled scan performed work")
	}
	if _, err := client.readLocalMarkdown(ctx, "docs/service.md", 0, 100, ""); err == nil {
		t.Fatal("canceled read performed work")
	}
}

func TestLocalSearchCountsHiddenEntriesWithoutReadingThem(t *testing.T) {
	_, _, source := localFixture(t)
	// The limit also counts excluded names, otherwise a large hidden subtree's
	// directory entries could make a nominally bounded search scan indefinitely.
	for i := 0; i < maxLocalScanEntries+5; i++ {
		writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", fmt.Sprintf(".hidden-%04d.md", i)), "\xff")
	}
	result, err := newSourceClient(source).searchLocalMarkdown(context.Background(), "CBS", 10)
	if err != nil {
		t.Fatalf("hidden source bodies were read: %v", err)
	}
	coverage := result["_agent_host"].(map[string]any)["coverage"].(map[string]any)
	if coverage["scan_limit_reached"] != true || coverage["scanned_entries"].(int) > maxLocalScanEntries {
		t.Fatalf("excluded names bypassed bounded enumeration: %+v", coverage)
	}
}

func TestLocalRootAndConfiguredPathSymlinksFailDuringResolution(t *testing.T) {
	_, c, _ := localFixture(t)
	link := c.Sources[0].Root + "-link"
	if err := os.Symlink(c.Sources[0].Root, link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	s := c.Sources[0]
	s.Root = link
	if _, err := ResolveConfiguredSource(s); err == nil {
		t.Fatal("root symlink accepted")
	}
	if err := os.Symlink("docs", filepath.Join(s.Root, "docs-link")); err != nil {
		t.Fatal(err)
	}
	s.Root, s.Paths = c.Sources[0].Root, []string{"docs-link"}
	if _, err := ResolveConfiguredSource(s); err == nil {
		t.Fatal("configured directory symlink accepted")
	}
}

func TestLocalMCPAdvertisesReadOnlyToolsWithoutReadingBodies(t *testing.T) {
	config, _, source := localFixture(t)
	// Invalid source content proves initialize/list only checks access metadata.
	writeKnowledgePrivate(t, filepath.Join(source.Source.Root, "docs", "invalid.md"), "\xff")
	input := initializeFixture + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	var output bytes.Buffer
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	replies := decodeMCPReplies(t, output.Bytes())
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "search_local_markdown" || tools[1].(map[string]any)["name"] != "read_local_markdown" {
		t.Fatalf("local tools missing: %+v", tools)
	}
	if strings.Contains(output.String(), source.Source.Root) || strings.Contains(output.String(), "invalid.md") {
		t.Fatal("startup exposed source content/absolute paths")
	}
	if err := os.Remove(filepath.Join(source.Source.Root, "docs", "invalid.md")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	input = initializeFixture + "\n" + strings.Join([]string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_local_markdown","arguments":{"query":"CBS"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_local_markdown","arguments":{"path":"docs/service.md","limit":100}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_local_markdown","arguments":{"path":"../private.md"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"search_confluence","arguments":{"query":"x"}}}`,
	}, "\n")
	if err := Serve(context.Background(), config, source.Source.Name, source.Fingerprint, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	replies = decodeMCPReplies(t, output.Bytes())
	if replies[1]["result"].(map[string]any)["isError"] != false || replies[2]["result"].(map[string]any)["isError"] != false || replies[3]["result"].(map[string]any)["isError"] != true || replies[4]["error"] == nil {
		t.Fatalf("search/read/error boundaries wrong: %s", output.String())
	}
	if !strings.Contains(output.String(), `source_instance`) || !strings.Contains(output.String(), `local_markdown`) || strings.Contains(output.String(), source.Source.Root) {
		t.Fatal("local MCP receipts missing or absolute root exposed")
	}
}
