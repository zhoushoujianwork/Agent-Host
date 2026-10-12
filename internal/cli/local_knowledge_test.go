package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/knowledge"
)

func localKnowledgeConfig(t *testing.T) (string, knowledge.Configuration) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var sources []knowledge.Source
	for _, name := range []string{"project_notes", "private_notes"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# Historical note\nA source-attributed statement.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		source := knowledge.Source{Name: name, Provider: "local_markdown", Root: dir, Paths: []string{"note.md"}, Principal: "fixture", Description: "Reference knowledge", Projects: []string{"default", "api"}}
		if name == "private_notes" {
			source.Audiences = []string{"owner"}
		}
		sources = append(sources, source)
	}
	path := filepath.Join(root, "knowledge.json")
	config := knowledge.Configuration{Version: 1, Sources: sources}
	writeLocalKnowledgeConfig(t, path, config)
	return path, config
}

func writeLocalKnowledgeConfig(t *testing.T, path string, config knowledge.Configuration) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalKnowledgeAssemblyUsesActualAudienceAndProject(t *testing.T) {
	path, config := localKnowledgeConfig(t)
	for _, project := range []string{"", "api"} {
		for _, audience := range []string{"owner", "group-a", "group-b"} {
			t.Run(project+"/"+audience, func(t *testing.T) {
				o := options{knowledgeConfig: path, projectID: project, audienceID: audience}
				if err := resolveKnowledge(&o); err != nil {
					t.Fatal(err)
				}
				want := 1
				if audience == "owner" {
					want = 2
				}
				if len(o.mcpServers) != want || len(o.knowledgeHints) != want {
					t.Fatalf("wrong source mounts for %s: %+v", audience, o.mcpServers)
				}
				for _, hint := range o.knowledgeHints {
					if audience != "owner" && hint.Name == "private_notes" {
						t.Fatal("private source exposed to group")
					}
					if strings.Contains(hint.Description, config.Sources[0].Root) || len(hint.Tools) != 2 {
						t.Fatal("local source navigation exposed its root or wrong tools")
					}
				}
			})
		}
	}
	if err := os.RemoveAll(config.Sources[1].Root); err != nil {
		t.Fatal(err)
	}
	o := options{knowledgeConfig: path, audienceID: "group-a"}
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 1 {
		t.Fatalf("excluded private source blocked group: %v", err)
	}
	o.audienceID = "owner"
	if err := resolveKnowledge(&o); err == nil {
		t.Fatal("unavailable selected private source was silently ignored")
	}
	o.knowledgeNames = []string{"project_notes"}
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 1 {
		t.Fatalf("instance source filter did not narrow before resolution: %v", err)
	}
	o.projectID = "other"
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 {
		t.Fatal("unauthorized project received a local source", err)
	}
}

func TestLocalKnowledgeRevocationAndToolFreeExecution(t *testing.T) {
	path, config := localKnowledgeConfig(t)
	o := options{knowledgeConfig: path, audienceID: "owner"}
	if err := resolveKnowledge(&o); err != nil {
		t.Fatal(err)
	}
	validate := knowledgeValidator(o)
	before := knowledge.Fingerprint(o.knowledgeSources)
	config.Sources[1].Audiences = []string{"group-a"}
	writeLocalKnowledgeConfig(t, path, config)
	if err := validate(); err == nil {
		t.Fatal("audience revocation accepted stale source assembly")
	}
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 1 || knowledge.Fingerprint(o.knowledgeSources) == before {
		t.Fatal("revocation did not change the mounted source set", err)
	}
	o.noTools = true
	if err := os.RemoveAll(config.Sources[0].Root); err != nil {
		t.Fatal(err)
	}
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 || len(o.knowledgeHints) != 0 {
		t.Fatal("tool-free execution loaded local knowledge", err)
	}
}

func TestLocalKnowledgeOperatorCheckIncludesRestrictedAudience(t *testing.T) {
	path, config := localKnowledgeConfig(t)
	config.Sources = config.Sources[1:]
	config.Sources[0].Audiences = []string{"group-a"}
	writeLocalKnowledgeConfig(t, path, config)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"knowledge", "check", "--config", path}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"private_notes"`) || !strings.Contains(out.String(), `"audiences":["group-a"]`) || strings.Contains(out.String(), config.Sources[0].Root) {
		t.Fatal("operator check omitted restricted source or exposed root", out.String())
	}
	if err := os.RemoveAll(config.Sources[0].Root); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"knowledge", "check", "--config", path}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("operator check failed to validate unavailable restricted source")
	}
}
