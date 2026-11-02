//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func TestOffloadInteractiveTextOutputAndPromptSeparation(t *testing.T) {
	_, args := chatOffloadFixture(t)
	var out, prompts bytes.Buffer
	if err := Run(t.Context(), append(args, "--format", "text"), strings.NewReader("first\nsecond\n"), &out, &prompts); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "answer-1") != 1 || strings.Count(out.String(), "answer-2") != 1 || strings.Contains(out.String(), "you> ") {
		t.Fatalf("answers or prompt stream changed: %s", out.String())
	}
	if strings.Count(prompts.String(), "you> ") != 3 {
		t.Fatalf("prompts %q", prompts.String())
	}
}

func TestOffloadInteractiveChatContinuity(t *testing.T) {
	for _, transport := range []string{"cli", "package"} {
		t.Run(transport, func(t *testing.T) {
			var o options
			var args []string
			if transport == "package" {
				o, args, _ = pkgChatFixture(t)
			} else {
				o, args = chatOffloadFixture(t)
			}
			events, err := chatOffloadInputEvents(t, args, strings.NewReader("\nfirst\n  \nsecond\n"))
			if err != nil {
				t.Fatal(err)
			}
			store, err := state.Open(o.home, true)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var previous state.Run
			count := 0
			for _, e := range events {
				if e.Type == "offload.result" {
					t.Fatal("private evidence leaked")
				}
				if e.Type != "run.started" {
					continue
				}
				if count >= 2 {
					t.Fatal("unexpected extra interactive turn")
				}
				run, err := store.Run(t.Context(), e.RunID)
				if err != nil || run.ExecutionStatus != "completed" || run.Room != nil || run.Source != nil {
					t.Fatalf("%+v %v", run, err)
				}
				if count > 0 && (run.SessionID != previous.SessionID || run.NativeSessionID != previous.NativeSessionID) {
					t.Fatal("interactive native continuity lost")
				}
				if run.Input != []string{"first", "second"}[count] {
					t.Fatalf("input changed: %q", run.Input)
				}
				previous = run
				count++
			}
			if count != 2 {
				t.Fatalf("expected two turns, got %d", count)
			}
			// Restart the interactive entry with the same Host session, after its lease releases.
			events, err = chatOffloadInputEvents(t, append(args, "--resume", previous.SessionID), strings.NewReader("third\n"))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if e.Type != "run.started" {
					continue
				}
				run, err := store.Run(t.Context(), e.RunID)
				if err != nil || run.SessionID != previous.SessionID || run.NativeSessionID != previous.NativeSessionID || run.ExecutionStatus != "completed" {
					t.Fatalf("resumed %+v %v", run, err)
				}
				count++
			}
			if count != 3 {
				t.Fatalf("resumed turn count %d", count)
			}
			dir := filepath.Dir(o.offloadConfig)
			data, err := os.ReadFile(filepath.Join(dir, "count"))
			if err != nil || strings.TrimSpace(string(data)) != "3" {
				t.Fatalf("blank input/EOF invoked backend: %s %v", data, err)
			}
			if transport == "package" {
				if _, err := os.Stat(filepath.Join(dir, "cli-started")); !os.IsNotExist(err) {
					t.Fatal("Offload CLI launched")
				}
			}
		})
	}
}

func TestOffloadInteractiveIdleCancellationAndEmptyInput(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "EOF"}[empty], func(t *testing.T) {
			o, args := chatOffloadFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()
			var reader io.Reader = input
			if empty {
				reader = strings.NewReader("\n  \n")
			}
			done := make(chan error, 1)
			prompts := make(interactivePrompts, 4)
			go func() { done <- Run(ctx, args, reader, io.Discard, prompts) }()
			if !empty {
				select {
				case <-prompts:
				case <-time.After(5 * time.Second):
					t.Fatal("interactive prompt did not open")
				}
				cancel()
			}
			select {
			case err := <-done:
				if empty && err != nil || !empty && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("idle CLI did not exit")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.offloadConfig), "count")); !os.IsNotExist(err) {
				t.Fatal("idle input launched native execution")
			}
		})
	}
}

type interactivePrompts chan struct{}

func (p interactivePrompts) Write(b []byte) (int, error) {
	select {
	case p <- struct{}{}:
	default:
	}
	return len(b), nil
}

func TestOffloadInteractiveRejectsOversizedInputBeforeExecution(t *testing.T) {
	o, args := chatOffloadFixture(t)
	events, err := chatOffloadInputEvents(t, args, strings.NewReader(strings.Repeat("x", 16*1024+1)+"\nnever dispatch\n"))
	if err == nil {
		t.Fatal("oversized input accepted")
	}
	for _, e := range events {
		if e.Type == "run.started" {
			t.Fatal("oversized input started a run")
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.offloadConfig), "count")); !os.IsNotExist(err) {
		t.Fatal("oversized input launched native execution")
	}
}

func TestOffloadInteractiveFailureDoesNotConsumeNextTurn(t *testing.T) {
	o, args := chatOffloadFixture(t)
	dir := filepath.Dir(o.offloadConfig)
	script := "#!/bin/sh\ncat > '" + dir + "/failed-input'\nprintf x >> '" + dir + "/calls'\nprintf '%s\n' '{\"status\":\"error\",\"exit_code\":7,\"error\":\"fixture failure\"}'\nexit 7\n"
	if err := os.WriteFile(o.offloadProfile.Command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	events, err := chatOffloadInputEvents(t, args, strings.NewReader("first\nnever run this\n"))
	if err == nil {
		t.Fatal("native failure hidden")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if string(data) != "x" {
		t.Fatalf("failed turn replayed or queued turn dispatched: %q", data)
	}
	finished := 0
	for _, e := range events {
		if e.Type == "run.finished" {
			finished++
			if e.Status != "failed" {
				t.Fatalf("outcome %+v", e)
			}
		}
	}
	if finished != 1 {
		t.Fatalf("terminal receipts %d", finished)
	}
}
