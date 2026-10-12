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

	"agent-host/internal/state"
)

func TestLocalProjectCommandsAndRuntimeResolution(t *testing.T) {
	ctx := context.Background()
	home, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	a, _ = filepath.EvalSymlinks(a)
	b, _ = filepath.EvalSymlinks(b)
	invoke := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Run(ctx, append(args, "--home", home), nil, &out, io.Discard)
		return out.String(), err
	}
	output, err := invoke("project", "add", "--id", "api", "--name", "API", "--workspace", a)
	if err != nil {
		t.Fatal(err)
	}
	var p state.Project
	if err = json.Unmarshal([]byte(output), &p); err != nil || p.Scope != "project:api" || p.Workspace != a {
		t.Fatalf("%s %v", output, err)
	}
	if _, err = invoke("project", "add", "--id", "api", "--workspace", b); err == nil {
		t.Fatal("accepted repeated ID")
	}
	store, err := state.Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeBinary, err := filepath.Rel(cwd, binary)
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := parse("chat", []string{"--project", "api", "--claude", relativeBinary}, io.Discard)
	if err != nil || o.binary != binary {
		t.Fatalf("relative native path changed with project cwd: %+v %v", o, err)
	}
	if err = resolveOptions(ctx, store, &o, true); err != nil || o.scope != p.Scope || o.workspace != a || o.runtimeFingerprint == "" {
		t.Fatalf("%+v %v", o, err)
	}
	actualBinary, err := filepath.EvalSymlinks(binary)
	if err != nil || o.binary != actualBinary {
		t.Fatalf("native executable was not resolved before changing cwd: %s %v", o.binary, err)
	}
	byDirectory := options{workspace: a}
	if err = resolveOptions(ctx, store, &byDirectory, false); err != nil || byDirectory.projectID != "api" {
		t.Fatalf("%+v %v", byDirectory, err)
	}
	store.Close()
	release, err := state.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("project", "update", "--id", "api", "--workspace", b); err == nil || !strings.Contains(err.Error(), "stop the service") {
		t.Fatalf("lock: %v", err)
	}
	if _, err = invoke("project", "list"); err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	output, err = invoke("project", "update", "--id", "api", "--workspace", b)
	if err != nil {
		t.Fatal(err)
	}
	var moved state.Project
	if err = json.Unmarshal([]byte(output), &moved); err != nil || moved.Scope != p.Scope || moved.Name != p.Name {
		t.Fatalf("%s %v", output, err)
	}
	if err = os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("memory", "candidates", "--project", "api"); err != nil {
		t.Fatalf("inspection depends on removed project files: %v", err)
	}
	store, err = state.Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	missing := options{projectID: "api", binary: binary}
	if err = resolveOptions(ctx, store, &missing, true); err == nil {
		t.Fatal("missing cwd executed")
	}
}

func TestProjectFlagsAreExplicit(t *testing.T) {
	for _, args := range [][]string{{"--project", "api", "--workspace", t.TempDir()}, {"--project", "unknown", "--workspace", "/missing"}, {"--project", "", "--workspace", t.TempDir()}} {
		if _, _, err := parse("chat", args, io.Discard); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, _, err := parse("chat", []string{"--project", ""}, io.Discard); err == nil {
		t.Fatal("empty explicit project silently selected the default")
	}
}
