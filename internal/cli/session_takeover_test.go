package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCLITakeoverContinuesNativeContextAcrossTurnsWithoutReplay(t *testing.T) {
	fixture, args, capture := fileResumeFixture(t)
	for i, arg := range args {
		if arg != "--claude" {
			continue
		}
		path := args[i+1]
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		script := strings.Replace(string(raw), `--resume) session="$2"; resumed='true'; shift ;;`, `--resume) if [ -z "$session" ]; then session="$2"; fi; resumed='true'; shift ;;`, 1)
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(extra ...string) {
		t.Helper()
		if err := Run(t.Context(), append(append([]string(nil), args...), extra...), nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	invoke("--room", "original", "--request-id", "im-1", "-m", "fixture test package")
	seed := fileResumeRuns(t, fixture.home)[0]
	invoke("--takeover-session", seed.SessionID, "--request-id", "web-1", "-m", "fixture test package")
	runs := fileResumeRuns(t, fixture.home)
	if len(runs) != 2 || runs[1].Room.ParentSessionID != seed.SessionID || runs[1].Room.RoomID != seed.Room.RoomID || runs[1].NativeSessionID == seed.NativeSessionID {
		t.Fatalf("wrong native continuation: %+v", runs)
	}
	invoke("--takeover-session", seed.SessionID, "--request-id", "web-1", "-m", "fixture test package")
	if len(fileResumeRuns(t, fixture.home)) != 2 {
		t.Fatal("retry replayed task")
	}
	invoke("--takeover-session", runs[1].SessionID, "--request-id", "web-2", "-m", "fixture test package")
	runs = fileResumeRuns(t, fixture.home)
	if len(runs) != 3 || runs[2].Room.ParentSessionID != runs[1].SessionID {
		t.Fatal("second turn lost its parent")
	}
	raw, err := os.ReadFile(capture)
	if err != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 3 {
		t.Fatal("duplicate invoked native engine", string(raw), err)
	}
}
