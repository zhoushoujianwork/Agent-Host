package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/knowledge"
)

func datasourceDocumentFixture(t *testing.T, dir, id, name string) string {
	t.Helper()
	root := filepath.Join(filepath.Dir(dir), "documents", id)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("BuildHub retained source"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "datasource.plugins."+id+".json")
	source := map[string]any{"name": name, "principal": "owner", "description": "reference notes", "projects": []string{"default", "api"}}
	if id == "local-documents" {
		source["root"] = root
		source["paths"] = []string{"note.md"}
	} else {
		source["endpoint"] = "https://example.test/api"
		credential := filepath.Join(dir, "credentials", id+".json")
		if id == "dokki" {
			writeBotJSON(t, credential, map[string]string{"api_key": "fixture"})
		} else {
			writeBotJSON(t, credential, map[string]string{"pat": "fixture"})
		}
		source["credential_file"] = credential
	}
	writeBotJSON(t, path, map[string]any{"version": 1, "enabled": true, "access": map[string]any{"channels": []string{"dingtalk"}, "audiences": []string{"owner"}}, "settings": map[string]any{"sources": []any{source}}})
	return path
}

func TestDatasourceNamespacedMountsAndAmbiguousLegacyReference(t *testing.T) {
	_, dir := unifiedFixture(t)
	local := datasourceDocumentFixture(t, dir, "local-documents", "notes")
	remote := datasourceDocumentFixture(t, dir, "dokki", "notes")
	o := options{knowledgeConfigs: []string{local, remote}, knowledgeChannel: "dingtalk", audienceID: "owner"}
	if err := resolveKnowledge(&o); err != nil {
		t.Fatal(err)
	}
	if len(o.knowledgeSources) != 2 {
		t.Fatal(o.knowledgeSources)
	}
	seen := map[string]bool{}
	for _, server := range o.mcpServers {
		seen[server.Name] = true
	}
	if !seen["knowledge_local-documents_notes"] || !seen["knowledge_dokki_notes"] {
		t.Fatal("colliding mounts", seen)
	}
	o.knowledgeNames = []string{"notes"}
	if _, err := selectKnowledgeForOptions(o); err == nil {
		t.Fatal("ambiguous legacy reference accepted")
	}
	o.knowledgeNames = []string{"local-documents:notes"}
	selected, err := selectKnowledgeForOptions(o)
	if err != nil || len(selected) != 1 || selected[0].ConfigPath != local {
		t.Fatal(selected, err)
	}
	if _, err := knowledge.ResolveSource(local, selected[0].Source.Name, selected[0].Fingerprint); err != nil {
		t.Fatal("new MCP binding unavailable", err)
	}
	o.audienceID = "group"
	if selected, err := selectKnowledgeForOptions(o); err != nil || len(selected) != 0 {
		t.Fatal("owner source exposed", selected, err)
	}
}

func TestDatasourceAgentSubsetExcludesUnavailableCredential(t *testing.T) {
	_, dir := unifiedFixture(t)
	local := datasourceDocumentFixture(t, dir, "local-documents", "private")
	remote := datasourceDocumentFixture(t, dir, "confluence", "remote")
	if err := os.Remove(filepath.Join(dir, "credentials", "confluence.json")); err != nil {
		t.Fatal(err)
	}
	o := options{knowledgeConfigs: []string{local, remote}, pluginNames: []string{"local-documents"}, audienceID: "owner"}
	if err := resolveKnowledge(&o); err != nil || len(o.knowledgeSources) != 1 {
		t.Fatal("excluded provider was resolved", err)
	}
	validate := knowledgeValidator(o)
	if validate == nil {
		t.Fatal("missing access validator")
	}
	if err := validate(); err != nil {
		t.Fatal(err)
	}
	e, err := readEnvelope(local)
	if err != nil {
		t.Fatal(err)
	}
	e.Access.Channels = []string{"feishu"}
	writeBotJSON(t, local, e)
	if err := validate(); err == nil {
		t.Fatal("changed access binding accepted")
	}
}

func TestDatasourceLegacyFilesRequireExplicitMigration(t *testing.T) {
	_, dir := unifiedFixture(t)
	writeBotJSON(t, filepath.Join(dir, "plugins.email.json"), map[string]any{"version": 1, "enabled": false})
	if _, err := discoverUnifiedChannels(dir); err == nil || !strings.Contains(err.Error(), "migration") {
		t.Fatal("legacy silently loaded", err)
	}
}
