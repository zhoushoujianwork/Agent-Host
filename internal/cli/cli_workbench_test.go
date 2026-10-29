//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

type workbenchReady chan string

func (w workbenchReady) Write(p []byte) (int, error) {
	select {
	case w <- string(p):
	default:
	}
	return len(p), nil
}

func TestCLIWorkbenchObservesOfficialChatAfterStartup(t *testing.T) {
	source, err := filepath.Abs("../../tools/run-viewer")
	if err != nil {
		t.Fatal(err)
	}
	root := channelDirectoryFixture(t)
	t.Chdir(root)
	directory := filepath.Join(root, ".agent-host", "config")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(listener.Addr().String(), ":")[1]
	listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(workbenchReady, 1)
	done := make(chan error, 1)
	go func() {
		done <- runService(ctx, []string{"workbench", "--config-dir", directory, "--viewer-source", source, "--viewer-port", port, "--no-browser"}, io.Discard, ready)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("CLI workbench did not stop")
		}
	})
	select {
	case message := <-ready:
		if !strings.Contains(message, "Execution workbench:") {
			t.Fatal(message)
		}
	case err := <-done:
		done <- err
		t.Fatal("CLI-only startup failed", err)
	case <-time.After(10 * time.Second):
		t.Fatal("CLI-only startup timed out")
	}
	address := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path string, value any) {
		t.Helper()
		response, err := client.Get(address + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("workbench response", path, response.StatusCode)
		}
		if err := json.NewDecoder(response.Body).Decode(value); err != nil {
			t.Fatal(err)
		}
	}
	var index struct {
		Records []struct{ Key string }
		Sources []struct{ Channel, Status, Home string }
	}
	get("/api/records", &index)
	home := filepath.Join(root, ".agent-host", "cli-state")
	if len(index.Records) != 0 || len(index.Sources) != 1 || index.Sources[0].Channel != "cli" || index.Sources[0].Status != "missing_database" || index.Sources[0].Home != home {
		t.Fatal("missing state must be explicit and already mounted", index)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("observer created chat state", err)
	}
	// Exercise the public chat entry with its ordinary cwd-based default home.
	// Native execution and Jev are deterministic fixtures, not live credentials.
	_, fixtureArgs, _ := fileResumeFixture(t)
	args := []string{fixtureArgs[0]}
	for i := 1; i < len(fixtureArgs); i++ {
		if fixtureArgs[i] == "--home" {
			i++
			continue
		}
		args = append(args, fixtureArgs[i])
	}
	query := "Reply with OK."
	if err := Run(t.Context(), append(append([]string(nil), args...), "-m", query), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	runs := fileResumeRuns(t, home)
	if len(runs) != 1 || runs[0].ExecutionStatus != "completed" {
		t.Fatal("chat did not persist completion", runs)
	}
	first := runs[0]
	if err := Run(t.Context(), append(append([]string(nil), args...), "--resume", first.SessionID, "-m", query), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for i, value := range args {
		if value == "--claude" {
			if err := os.WriteFile(args[i+1], []byte("#!/bin/sh\ncat >/dev/null\nexit 7\n"), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Run(t.Context(), append(append([]string(nil), args...), "-m", query), nil, io.Discard, io.Discard); err == nil {
		t.Fatal("native failure was hidden")
	}
	marker := filepath.Join(root, "native-started")
	t.Setenv("AGENT_HOST_WORKBENCH_STARTED", marker)
	for i, value := range args {
		if value == "--claude" {
			if err := os.WriteFile(args[i+1], []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '2.1.277 (Claude Code)'; exit 0; fi\nif [ \"$1\" = \"--help\" ]; then echo '--plugin-dir <path>'; exit 0; fi\ncat >/dev/null\ntouch \"$AGENT_HOST_WORKBENCH_STARTED\"\nexec sleep 30\n"), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	turnCtx, cancelTurn := context.WithCancel(t.Context())
	defer cancelTurn()
	turnDone := make(chan error, 1)
	go func() {
		turnDone <- Run(turnCtx, append(append([]string(nil), args...), "-m", query), nil, io.Discard, io.Discard)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-turnDone:
			t.Fatal("cancellation turn stopped before native execution", err)
		default:
		}
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native cancellation fixture did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelTurn()
	select {
	case err := <-turnDone:
		if err == nil {
			t.Fatal("native cancellation was hidden")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("native cancellation did not stop")
	}
	get("/api/records", &index)
	if len(index.Records) != 4 || index.Sources[0].Status != "ready" {
		t.Fatal("refresh did not observe new CLI runs", index)
	}
	statuses := map[string]int{}
	for _, row := range index.Records {
		var detail struct {
			Run    state.Run
			Events []json.RawMessage
			Source struct{ Channel string }
		}
		get("/api/record?key="+row.Key, &detail)
		if detail.Source.Channel != "cli" || detail.Run.Input != query || len(detail.Events) == 0 || detail.Run.AcceptanceStatus != "unresolved" {
			t.Fatal("lost source, request, evidence or acceptance", detail)
		}
		statuses[detail.Run.ExecutionStatus]++
		if detail.Run.ExecutionStatus == "completed" && (detail.Run.SessionID != first.SessionID || detail.Run.Output != "Fixture plan." || detail.Run.Model != "resume-native-fixture") {
			t.Fatal("lost continuation, output or actual model", detail.Run)
		}
	}
	if statuses["completed"] != 2 || statuses["failed"] != 1 || statuses["canceled"] != 1 {
		t.Fatal("incorrect execution outcomes", statuses)
	}
}
