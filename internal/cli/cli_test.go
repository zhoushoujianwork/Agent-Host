package cli

import (
	"agent-host/internal/state"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestIdlePromptCancelsWithoutInput(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic-key")
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"chat", "--home", t.TempDir(), "--workspace", t.TempDir()}, input, io.Discard, io.Discard)
	}()
	time.AfterFunc(50*time.Millisecond, cancel)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle prompt blocked cancellation")
	}
}

func TestBlankExplicitMessageDoesNotBecomeInteractive(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic-key")
	err := Run(context.Background(), []string{"chat", "--home", t.TempDir(), "--workspace", t.TempDir(), "-m", ""}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "message cannot be empty") {
		t.Fatalf("got %v", err)
	}
}

func TestHelpAndConfigurationFailures(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"retry-memory", "NDJSON", "JEV_API_KEY", "TYPESAFE_API_KEY"} {
		if !strings.Contains(out.String(), word) {
			t.Fatalf("missing %s", word)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"chat", "--format", "web"}, {"chat", "--jev-provider", "auto"}, {"chat", "--jev-provider", "patsnap"}, {"chat", "--jev-provider", "custom"},
		{"chat", "--jev-endpoint", "https://gateway.example.test/v1"}, {"chat", "--jev-provider", "custom", "--jev-endpoint", "http://gateway.example.test/v1"}, {"chat", "--timeout", "0s"}, {"memory", "show"}} {
		if err := Run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}

func TestNativeToolPermissionsAreExplicit(t *testing.T) {
	o, _, err := parse("chat", []string{"--workspace", t.TempDir(), "--allowed-tools", "Read,Edit", "--allowed-tools", "Bash(go test *)"}, io.Discard)
	if err != nil || len(o.allowedTools) != 3 || o.allowedTools[2] != "Bash(go test *)" {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"--no-tools", "--allowed-tools", "Read"}, {"--allowed-tools", "Read,,Edit"}} {
		if _, _, err := parse("chat", args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestCandidateInspectionDoesNotRequireCredentials(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	home, workspace := t.TempDir(), t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = Run(context.Background(), []string{"memory", "candidates", "--home", home, "--workspace", workspace}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var candidates []state.Candidate
	if err = json.Unmarshal(out.Bytes(), &candidates); err != nil || len(candidates) != 0 {
		t.Fatalf("%s %v", out.String(), err)
	}
	if err = Run(context.Background(), []string{"memory", "candidates", "--home", home, "--workspace", workspace, "--status", "unknown"}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("accepted invalid candidate filter")
	}
}

func TestReviewInputValidationPrecedesModelCredentials(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"memory", "accept", "--expires-at", "yesterday", "candidate"}, "expires-at"},
		{[]string{"memory", "reject", "candidate"}, "reason"},
		{[]string{"memory", "invalidate", "memory"}, "reason"},
		{[]string{"memory", "restore", "--undo", "memory"}, "reason"},
	} {
		err := Run(context.Background(), tc.args, nil, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", tc.args, err)
		}
	}
}

func TestUndoFlagIsRestrictedToRestoration(t *testing.T) {
	o, pos, err := parse("memory restore", []string{"--undo", "--reason", "mistaken restoration", "memory"}, io.Discard)
	if err != nil || !o.undoRestore || len(pos) != 1 || pos[0] != "memory" {
		t.Fatalf("restoration undo arguments: %+v %v %v", o, pos, err)
	}
	if _, _, err := parse("memory invalidate", []string{"--undo", "memory"}, io.Discard); err == nil {
		t.Fatal("undo accepted for another operation")
	}
}
