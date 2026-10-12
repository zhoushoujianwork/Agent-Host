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
	"agent-host/internal/state"
)

func knowledgeFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	credential := filepath.Join(root, "token.json")
	if err := os.WriteFile(credential, []byte(`{"api_key":"private-test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "knowledge.json")
	raw, _ := json.Marshal(knowledge.Configuration{Version: 1, Sources: []knowledge.Source{{Name: "docs", Provider: "dokki", Endpoint: "https://example.test/api/v1", CredentialFile: credential, Principal: "owner", Description: "API reference", Projects: []string{"api"}}}})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return config, credential
}

func TestKnowledgeCheckAndServiceConfiguration(t *testing.T) {
	config, _ := knowledgeFixture(t)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"knowledge", "check", "--config", config}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"remote_verified":false`) || strings.Contains(out.String(), "private-test-token") || strings.Contains(out.String(), "token.json") {
		t.Fatalf("unsafe check: %s", out.String())
	}
	c := serviceConfig{KnowledgeConfig: config}
	args := strings.Join(c.arguments(serviceLayout{Config: t.TempDir()}, "serve"), " ")
	if !strings.Contains(args, "--knowledge-config "+config) {
		t.Fatal(args)
	}
}

func TestKnowledgeAssemblyScopeAndCredentialChange(t *testing.T) {
	config, credential := knowledgeFixture(t)
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RegisterProject(context.Background(), "api", "API", workspace); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := options{projectID: "api", binary: binary, model: "fixture", knowledgeConfig: config}
	if err := resolveOptions(context.Background(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	before := o.runtimeFingerprint
	validate := knowledgeValidator(o)
	if len(o.mcpServers) != 1 || o.mcpServers[0].Name != "knowledge_docs" || len(o.knowledgeHints) != 1 || strings.Contains(strings.Join(o.mcpServers[0].Args, " "), "private-test-token") {
		t.Fatalf("incorrect mount assembly: %+v", o.mcpServers)
	}
	if err := os.WriteFile(credential, []byte(`{"api_key":"changed-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validate(); err == nil {
		t.Fatal("stale assembled access accepted")
	}
	if err := resolveOptions(context.Background(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.runtimeFingerprint == before {
		t.Fatal("credential change did not rotate runtime")
	}
	o.noTools = true
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	if err := resolveOptions(context.Background(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	if len(o.mcpServers) != 0 || len(o.knowledgeHints) != 0 {
		t.Fatal("tool-free execution exposed knowledge")
	}
	o.noTools = false
	o.projectID = ""
	o.projects = nil
	if err := resolveKnowledge(&o); err != nil || len(o.mcpServers) != 0 {
		t.Fatalf("unselected source leaked or blocked: %v", err)
	}
}

func TestDingTalkCheckResolvesRegisteredWorkspaceBeforeSources(t *testing.T) {
	config, credential := knowledgeFixture(t)
	bot, _, _ := writeBotFixture(t)
	t.Setenv("TYPESAFE_API_KEY", "fixture-key")
	home := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RegisterProject(context.Background(), "api", "API", workspace); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"dingtalk", "check", "--config", bot, "--home", home, "--workspace", workspace, "--claude", binary, "--knowledge-config", config}
	if err := Run(context.Background(), args, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), args, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("registered workspace check ignored its source credential")
	}
}
