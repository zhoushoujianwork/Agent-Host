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

	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

func adoptedCLIEntry(t *testing.T, root string) managedskills.Entry {
	t.Helper()
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: exact-ticket\ndescription: Check exact original ticket identity\n---\nReuse the known instance_id and read current comments and tasks.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = Run(t.Context(), []string{"skills", "import", "--root", root, "--source", source, "--id", "exact-ticket", "--actor", "operator", "--applicability", "quoted ticket checks", "--agents", "default", "--audiences", "owner", "--scopes", "*", "--provenance", "operator reviewed source"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var c managedskills.Catalog
	if err = json.Unmarshal(out.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	e := c.Records[0].Revisions[0].Entry
	for _, action := range []string{"review", "publish", "enable"} {
		args := []string{"skills", action, "--root", root, "--id", e.ID, "--revision", "1", "--digest", e.Digest, "--actor", "reviewer"}
		if action == "review" {
			args = append(args, "--note", "reviewed original identity handling")
		}
		if err = Run(t.Context(), args, nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestManagedRuntimeToolFreeAndAgentIsolation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "state")
	s, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	skillRoot := filepath.Join(root, "skills")
	adoptedCLIEntry(t, skillRoot)
	base := options{home: home, skillsRoot: skillRoot, agentID: "default", audienceID: "owner", scope: "project-a"}
	if err = refreshManagedRuntime(t.Context(), s, &base); err != nil {
		t.Fatal(err)
	}
	if len(base.managedEntries) != 1 || base.managedFingerprint == "" {
		t.Fatal("adopted revision not effective")
	}
	for _, mode := range []string{"tool-free", "other-agent"} {
		o := base
		if mode == "tool-free" {
			o.noTools = true
		} else {
			o.agentID = "other"
		}
		if err = refreshManagedRuntime(t.Context(), s, &o); err != nil {
			t.Fatal(err)
		}
		if len(o.managedEntries) != 0 || o.managedFingerprint != "" {
			t.Fatal("managed catalog crossed mode or Agent policy")
		}
	}
	validator := managedValidator(s, base)
	e := base.managedEntries[0]
	if _, err = managedskills.Update(skillRoot, "disable", e.ID, "operator", e.Digest, "", 1, nil); err != nil {
		t.Fatal(err)
	}
	if err = validator(context.Background()); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("disabled snapshot admitted", err)
	}
}

func TestPluginGuidanceMatchesMountedToolsWithoutCredentials(t *testing.T) {
	o := options{mcpServers: []harness.MCPServer{{Name: "dingtalk", Command: "/private/adapter", Args: []string{"opaque-config-location"}}}}
	entries, plugins, err := pluginSkillEntries(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(plugins["dingtalk"].Tools) == 0 {
		t.Fatal("missing plugin contract")
	}
	raw, _ := json.Marshal(entries)
	if bytes.Contains(raw, []byte("opaque-config-location")) || bytes.Contains(raw, []byte("/private/adapter")) {
		t.Fatal("adapter configuration copied into guidance")
	}
	if !strings.Contains(entries[0].Files["references/contract.json"], "read_ticket") || !strings.Contains(entries[0].Files["SKILL.md"], "instance_id") {
		t.Fatal("missing ticket identity guidance")
	}
	disabled, tools, err := pluginSkillEntries(options{})
	if err != nil || len(disabled) != 0 || len(tools) != 0 {
		t.Fatal("disabled plugin still exposed")
	}
	_, _, err = pluginSkillEntries(options{mcpServers: []harness.MCPServer{{Name: "unsupported"}}})
	if err == nil {
		t.Fatal("missing plugin guidance treated as ready")
	}
}
