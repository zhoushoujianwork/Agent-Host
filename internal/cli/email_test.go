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

	"agent-host/internal/assembly"
	"agent-host/internal/email"
)

func emailFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "email.json")
	c := email.Config{StateDirectory: filepath.Join(root, "state"), Accounts: []email.Account{{ID: "primary", Provider: "163", Address: "owner@163.com", CredentialFile: filepath.Join(root, "secret")}}}
	b, _ := json.Marshal(c)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}
func TestEmailAssemblyPreservesKnowledgeAndScope(t *testing.T) {
	mail := emailFixture(t)
	knowledge, _ := knowledgeFixture(t)
	o := options{emailConfig: mail, knowledgeConfig: knowledge, projectID: "api", audienceID: "owner"}
	if e := resolveKnowledge(&o); e != nil {
		t.Fatal(e)
	}
	if len(o.mcpServers) != 2 || o.mcpServers[0].Name != "email" || o.mcpServers[1].Name != "knowledge_docs" {
		t.Fatal("email displaced knowledge", o.mcpServers)
	}
	first := o.emailFingerprint
	if first == "" || assemblyConfig(o).EmailFingerprint != first {
		t.Fatal("missing runtime binding")
	}
	if e := resolveKnowledge(&o); e != nil || len(o.mcpServers) != 2 {
		t.Fatal("duplicate mount", e)
	}
	o.projectID = ""
	if e := resolveKnowledge(&o); e != nil || len(o.mcpServers) != 1 {
		t.Fatal("email wrongly depends on project", e)
	}
	o.audienceID = "group:test"
	if e := resolveKnowledge(&o); e != nil || len(o.mcpServers) != 0 || o.emailFingerprint != "" {
		t.Fatal("personal mail exposed in group", e)
	}
	o.audienceID = "owner"
	o.noTools = true
	if e := resolveKnowledge(&o); e != nil || len(o.mcpServers) != 0 {
		t.Fatal("tool-free mail mount", e)
	}
}
func TestEmailCheckAndServiceArguments(t *testing.T) {
	path := emailFixture(t)
	var out bytes.Buffer
	if e := Run(context.Background(), []string{"email", "check", "--config", path}, nil, &out, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"remote_verified":false`) || strings.Contains(out.String(), "credential_file") {
		t.Fatal(out.String())
	}
	args := strings.Join((serviceConfig{EmailConfig: path}).arguments(serviceLayout{Config: t.TempDir()}, "serve"), " ")
	if !strings.Contains(args, "--email-config "+path) {
		t.Fatal(args)
	}
	o, _, e := parse("chat", []string{"--email-config", path}, io.Discard)
	if e != nil || o.emailConfig != path {
		t.Fatal(e)
	}
}
func TestEmailConfigurationRotatesRuntime(t *testing.T) {
	path := emailFixture(t)
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	o := options{emailConfig: path, audienceID: "owner"}
	if e = resolveKnowledge(&o); e != nil {
		t.Fatal(e)
	}
	before, e := assembly.Assemble("", "", dir, dir, assemblyConfig(o))
	if e != nil {
		t.Fatal(e)
	}
	c, _, e := email.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	c.Accounts[0].SendEnabled = true
	b, _ := json.Marshal(c)
	os.WriteFile(path, b, 0600)
	if e = resolveKnowledge(&o); e != nil {
		t.Fatal(e)
	}
	after, e := assembly.Assemble("", "", dir, dir, assemblyConfig(o))
	if e != nil || before.Fingerprint == after.Fingerprint {
		t.Fatal("email capability change reused session", e)
	}
}
