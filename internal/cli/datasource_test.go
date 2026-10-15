package cli

import (
	"encoding/json"
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

func TestDatasourceGlobalMounts(t *testing.T) {
	_, dir := unifiedFixture(t)
	local := datasourceDocumentFixture(t, dir, "local-documents", "notes")
	remote := datasourceDocumentFixture(t, dir, "confluence", "notes")
	for _, channel := range []string{"dingtalk", "feishu", ""} {
		for _, audience := range []string{"owner", "group-fixture"} {
			o := options{knowledgeConfigs: []string{local, remote}, knowledgeChannel: channel,
				audienceID: audience, projectID: "unrelated", knowledgeNames: []string{}, pluginNames: []string{}}
			if err := resolveKnowledge(&o); err != nil {
				t.Fatal(err)
			}
			if len(o.knowledgeSources) != 2 || len(o.knowledgeHints) != 2 {
				t.Fatal("global sources missing", channel, audience)
			}
			seen := map[string]bool{}
			for _, server := range o.mcpServers {
				seen[server.Name] = true
			}
			if !seen["knowledge_local-documents_notes"] || !seen["knowledge_confluence_notes"] {
				t.Fatal("missing mounts", seen)
			}
			for _, source := range o.knowledgeSources {
				if _, err := knowledge.ResolveSource(source.ConfigPath, source.Source.Name, source.Fingerprint); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	catalog, err := knowledge.Catalog([]string{local, remote}, "feishu")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := knowledge.ResolveNames(catalog, []string{"notes"}); err == nil {
		t.Fatal("ambiguous explicit source identity accepted")
	}
}

func TestDatasourceGlobalConfigurationAndBinding(t *testing.T) {
	_, dir := unifiedFixture(t)
	path := datasourceDocumentFixture(t, dir, "local-documents", "notes")
	// Old access/project/audience fields must not silently restrict global plugins.
	var envelope map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	sources := envelope["settings"].(map[string]any)["sources"].([]any)
	sources[0].(map[string]any)["audiences"] = []string{"owner"}
	writeBotJSON(t, path, envelope)
	o := options{knowledgeConfigs: []string{path}, audienceID: "group", knowledgeChannel: "feishu", projectID: "other"}
	if err := resolveKnowledge(&o); err != nil || len(o.knowledgeSources) != 1 {
		t.Fatal(err, o.knowledgeSources)
	}
	validate := knowledgeValidator(o)
	if validate == nil {
		t.Fatal("missing configuration validator")
	}
	delete(envelope, "access")
	delete(sources[0].(map[string]any), "projects")
	delete(sources[0].(map[string]any), "audiences")
	writeBotJSON(t, path, envelope)
	if err := validate(); err != nil {
		t.Fatal("obsolete scope fields changed binding", err)
	}
	sources[0].(map[string]any)["description"] = "Updated global notes"
	writeBotJSON(t, path, envelope)
	if err := validate(); err == nil {
		t.Fatal("changed source configuration accepted")
	}
}

func TestDatasourceGlobalProviderFailureIsExplicit(t *testing.T) {
	_, dir := unifiedFixture(t)
	remote := datasourceDocumentFixture(t, dir, "confluence", "remote")
	if err := os.Remove(filepath.Join(dir, "credentials", "confluence.json")); err != nil {
		t.Fatal(err)
	}
	o := options{knowledgeConfigs: []string{remote}, pluginNames: []string{}, audienceID: "group"}
	if err := resolveKnowledge(&o); err == nil {
		t.Fatal("enabled global provider failure hidden")
	}
	o.noTools = true
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 {
		t.Fatal("tool-free invocation loaded plugins", err)
	}
}

func TestDatasourceLegacyFilesRequireExplicitMigration(t *testing.T) {
	_, dir := unifiedFixture(t)
	writeBotJSON(t, filepath.Join(dir, "plugins.email.json"), map[string]any{"version": 1, "enabled": false})
	if _, err := discoverUnifiedChannels(dir); err == nil || !strings.Contains(err.Error(), "migration") {
		t.Fatal("legacy silently loaded", err)
	}
}
