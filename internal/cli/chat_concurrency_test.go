//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func TestIndependentCLIProcessesExecuteConcurrently(t *testing.T) {
	const helper = "AGENT_HOST_CONCURRENT_CHAT_ARGS"
	if raw := os.Getenv(helper); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		if err := Run(ctx, args, nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, room := range []bool{false, true} {
		t.Run(fmt.Sprint("room=", room), func(t *testing.T) {
			fixture, args, _ := fileResumeFixture(t)
			barrier := t.TempDir()
			t.Setenv("AGENT_HOST_CONCURRENT_BARRIER", barrier)
			var binary string
			for i, arg := range args {
				if arg == "--claude" {
					binary = args[i+1]
				}
			}
			script, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			// Both native children must reach this barrier before either may finish.
			// A serialized implementation cannot pass by eventually running each task.
			blocked := strings.Replace(string(script), "cat >/dev/null", `cat >/dev/null
touch "$AGENT_HOST_CONCURRENT_BARRIER/$session"
while [ ! -f "$AGENT_HOST_CONCURRENT_BARRIER/release" ]; do sleep 0.02; done`, 1)
			if err := os.WriteFile(binary, []byte(blocked), 0700); err != nil {
				t.Fatal(err)
			}
			var commands []*exec.Cmd
			var outputs []*bytes.Buffer
			for _, message := range []string{"first independent task", "second independent task"} {
				taskArgs := append(append([]string(nil), args...), "-m", message)
				if room {
					taskArgs = append(taskArgs, "--room", "same-room", "--request-id", message)
				}
				encoded, _ := json.Marshal(taskArgs)
				cmd := exec.Command(os.Args[0], "-test.run=^TestIndependentCLIProcessesExecuteConcurrently$")
				cmd.Env = append(os.Environ(), helper+"="+string(encoded))
				output := &bytes.Buffer{}
				cmd.Stdout, cmd.Stderr = output, output
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				commands, outputs = append(commands, cmd), append(outputs, output)
			}
			t.Cleanup(func() {
				os.WriteFile(filepath.Join(barrier, "release"), nil, 0600)
				for _, cmd := range commands {
					if cmd.ProcessState == nil {
						cmd.Wait()
					}
				}
			})
			deadline := time.Now().Add(30 * time.Second)
			for {
				entries, err := os.ReadDir(barrier)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) == 2 {
					break
				}
				if time.Now().After(deadline) {
					for _, entry := range entries {
						t.Logf("barrier entry: %s", entry.Name())
					}
					os.WriteFile(filepath.Join(barrier, "release"), nil, 0600)
					for i, cmd := range commands {
						err := cmd.Wait()
						t.Logf("child %d: %v\n%s", i, err, outputs[i])
					}
					for _, run := range fileResumeRuns(t, fixture.home) {
						t.Logf("run: %s session=%s native=%s status=%s error=%s", run.ID, run.SessionID, run.NativeSessionID, run.ExecutionStatus, run.Error)
					}
					t.Fatal("both native executions did not overlap")
				}
				time.Sleep(10 * time.Millisecond)
			}
			runs := fileResumeRuns(t, fixture.home)
			if len(runs) != 2 || runs[0].ExecutionStatus != "running" || runs[1].ExecutionStatus != "running" || runs[0].SessionID == runs[1].SessionID || runs[0].NativeSessionID == runs[1].NativeSessionID {
				t.Fatalf("independent live run isolation: %+v", runs)
			}
			if room && (runs[0].Room == nil || runs[1].Room == nil || runs[0].Room.RoomID != runs[1].Room.RoomID || runs[0].Room.SenderID != "local-owner") {
				t.Fatal("CLI did not preserve the room and actor", runs)
			}
			resume := append(append([]string(nil), args...), "--resume", runs[0].SessionID, "-m", "duplicate")
			if err := Run(t.Context(), resume, nil, io.Discard, io.Discard); !errors.Is(err, state.ErrSessionBusy) {
				t.Fatal("same native conversation executed twice", err)
			}
			if err := os.WriteFile(filepath.Join(barrier, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			for i, cmd := range commands {
				if err := cmd.Wait(); err != nil {
					t.Fatalf("CLI process %d: %v\n%s", i, err, outputs[i])
				}
			}
			for _, run := range fileResumeRuns(t, fixture.home) {
				if run.ExecutionStatus != "completed" || run.MemoryStatus != "unchanged" {
					t.Fatalf("concurrent terminal evidence: %+v", run)
				}
			}
		})
	}
}
