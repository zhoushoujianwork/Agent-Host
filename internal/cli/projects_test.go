package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/state"
)

func TestJointCLIAssemblesAllMembersAndKeepsNeutralEntry(t *testing.T) {
	ctx := context.Background()
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(userHome, "native"))
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"alpha", "beta"} {
		if _, err := store.RegisterProject(ctx, id, id, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	binary, _ := os.Executable()
	o, _, err := parse("chat", []string{"--project", "beta", "--project", "alpha", "--claude", binary}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveOptions(ctx, store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.projectID != "" || o.workspace != filepath.Join(userHome, "workspace") || len(o.projects) != 2 || len(o.additionalDirectories) != 2 {
		t.Fatalf("%+v", o)
	}
	fingerprint := o.runtimeFingerprint
	o.projectIDs = []string{"alpha", "beta"}
	if err := resolveOptions(ctx, store, &o, true); err != nil || o.runtimeFingerprint != fingerprint {
		t.Fatalf("repeat changed runtime: %v", err)
	}
	o.primaryProject = "alpha"
	if err := resolveOptions(ctx, store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.projectID != "alpha" || o.scope != "project:alpha" || len(o.additionalDirectories) != 1 || o.runtimeFingerprint == fingerprint {
		t.Fatalf("%+v", o)
	}
	joint := state.Run{Scope: "neutral", Projects: o.projects}
	if !runMatchesSelection(joint, options{projectID: "beta"}) || runMatchesSelection(joint, options{projectID: "gamma"}) {
		t.Fatal("joint history membership not respected")
	}
	if err := os.RemoveAll(o.projects[1].Workspace); err != nil {
		t.Fatal(err)
	}
	if err := resolveOptions(ctx, store, &o, true); err == nil {
		t.Fatal("missing secondary directory executed")
	}
}

func TestRoutingMetadataCommandsPreserveUnspecifiedFields(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	invoke := func(args ...string) state.Project {
		t.Helper()
		var out bytes.Buffer
		if err := Run(context.Background(), append(args, "--home", home), nil, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var p state.Project
		if err := json.Unmarshal(out.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := invoke("project", "add", "--id", "api", "--workspace", workspace, "--description", "Billing service", "--alias", "billing")
	b := invoke("project", "update", "--id", "api", "--name", "Payments")
	if b.Scope != a.Scope || b.Workspace != a.Workspace || b.Description != a.Description || len(b.Aliases) != 1 {
		t.Fatalf("%+v", b)
	}
	c := invoke("project", "update", "--id", "api", "--description", "")
	if c.Description != "" || c.Aliases[0] != "billing" {
		t.Fatalf("%+v", c)
	}
}
