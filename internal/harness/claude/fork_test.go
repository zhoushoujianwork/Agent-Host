//go:build unix

package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
)

func TestForkArgumentsKeepParentAndChildDistinct(t *testing.T) {
	_, req := fixtureAdapter(t, "success")
	req.ForkFromNativeSessionID = "parent-session"
	args, err := arguments(req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--session-id " + req.NativeSessionID, "--resume parent-session", "--fork-session"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, args)
		}
	}
	req.Resume = true
	if _, err = arguments(req); err == nil {
		t.Fatal("fork accepted a mutable resumed destination")
	}
}

func TestLivePrintForkPreservesHistoryAndAssignedChildID(t *testing.T) {
	if os.Getenv("AGH_TEST_CLAUDE_FORK") == "" {
		t.Skip("set AGH_TEST_CLAUDE_FORK for native print fork acceptance")
	}
	native, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := harness.Request{Workspace: root, NativeProfile: filepath.Join(root, "native"), NativeSessionID: "27baf2aa-44aa-4240-9500-111111111111", ToolsDisabled: true, Model: os.Getenv("AGH_TEST_MODEL"), Prompt: "Remember room marker AGH_PRINT_FORK_MAPLE_5721. Reply only READY. Use no tools."}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	emit := func(harness.Observation) error { return nil }
	a := New(native)
	parent, err := a.Run(ctx, req, emit)
	if err != nil || parent.Status != "completed" {
		t.Fatalf("parent=%+v %v", parent, err)
	}
	req.ForkFromNativeSessionID = req.NativeSessionID
	req.NativeSessionID = "27baf2aa-44aa-4240-9500-222222222222"
	req.Prompt = "Without tools, repeat the room marker from the previous user message."
	child, err := a.Run(ctx, req, emit)
	if err != nil || child.Status != "completed" || child.NativeSessionID != req.NativeSessionID || !strings.Contains(child.Text, "AGH_PRINT_FORK_MAPLE_5721") {
		t.Fatalf("child=%+v %v", child, err)
	}
}
