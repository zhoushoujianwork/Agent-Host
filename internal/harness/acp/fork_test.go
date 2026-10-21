package acp

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

func TestForkCreatesAndReconnectsDistinctChildWithoutChangingParent(t *testing.T) {
	a, req := fixture(t)
	emit := func(harness.Observation) error { return nil }
	parent, err := a.Run(t.Context(), req, emit)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, _, _ := a.binding(req)
	before, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	req.ForkFromNativeSessionID = req.NativeSessionID
	req.NativeSessionID = "host-child"
	child, err := a.Run(t.Context(), req, emit)
	if err != nil || child.Status != "completed" || child.NativeSessionID == parent.NativeSessionID {
		t.Fatalf("fork=%+v %v", child, err)
	}
	method, _ := os.ReadFile(filepath.Join(req.Workspace, "session-method"))
	forkParent, _ := os.ReadFile(filepath.Join(req.Workspace, "fork-parent"))
	if string(method) != "session/load" || string(forkParent) != parent.NativeSessionID {
		t.Fatalf("child was not loaded after fork: %s %s", method, forkParent)
	}
	after, _ := os.ReadFile(parentPath)
	if string(before) != string(after) {
		t.Fatal("fork overwrote parent binding")
	}
	if _, err = a.Run(t.Context(), req, emit); err == nil {
		t.Fatal("uncertain child binding was reused")
	}
}

// Opt-in native acceptance. All transcripts/bindings use a temporary profile;
// provider credentials are inherited from the invoking test environment.
func TestLiveCompletedParentForksConcurrently(t *testing.T) {
	configPath := os.Getenv("AGH_TEST_ACP_CONFIG")
	if configPath == "" {
		t.Skip("set AGH_TEST_ACP_CONFIG for native fork acceptance")
	}
	config, _, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	native, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	a := New(config, native)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := harness.Request{Workspace: root, NativeProfile: filepath.Join(root, "native"), NativeSessionID: "parent", ToolsDisabled: true, Model: os.Getenv("AGH_TEST_MODEL"), Prompt: "Remember the room marker AGH_ROOM_FORK_CEDAR_7319. Do not use tools. Reply only READY."}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	emit := func(harness.Observation) error { return nil }
	parent, err := a.Run(ctx, req, emit)
	if err != nil || parent.Status != "completed" {
		t.Fatalf("parent=%+v %v", parent, err)
	}
	type outcome struct {
		result harness.Result
		err    error
	}
	results := make(chan outcome, 2)
	for _, name := range []string{"Alice", "Bob"} {
		child := req
		child.ForkFromNativeSessionID = req.NativeSessionID
		child.NativeSessionID = name
		child.Prompt = "My authenticated speaker label for this message is " + name + ". Without tools, reply with my label and the room marker from the previous user message."
		go func() { r, err := a.Run(ctx, child, emit); results <- outcome{r, err} }()
	}
	ids := map[string]bool{parent.NativeSessionID: true}
	for range 2 {
		x := <-results
		if x.err != nil || x.result.Status != "completed" || !strings.Contains(x.result.Text, "AGH_ROOM_FORK_CEDAR_7319") || ids[x.result.NativeSessionID] {
			t.Fatalf("child=%+v %v", x.result, x.err)
		}
		ids[x.result.NativeSessionID] = true
		t.Logf("verified child %s: %s", x.result.NativeSessionID, x.result.Text)
	}
}
