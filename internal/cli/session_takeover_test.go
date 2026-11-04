package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCLITakeoverRejectsUnsupportedNativeForkWithoutReplay(t *testing.T) {
	fixture, args, capture := fileResumeFixture(t)
	if err := Run(t.Context(), append(append([]string(nil), args...), "--room", "original", "--request-id", "im-1", "-m", "fixture test package"), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	seed := fileResumeRuns(t, fixture.home)[0]
	for range 2 {
		err := Run(t.Context(), append(append([]string(nil), args...), "--takeover-session", seed.SessionID, "--request-id", "web-1", "-m", "fixture test package"), nil, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "cannot continue with the current execution configuration") {
			t.Fatal("unsupported isolated native fork did not fail closed", err)
		}
	}
	raw, err := os.ReadFile(capture)
	if err != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 1 || len(fileResumeRuns(t, fixture.home)) != 1 {
		t.Fatal("rejected takeover invoked or replayed native engine", string(raw), err)
	}
}
