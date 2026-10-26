package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func TestRunControlCancelsOnlySelectedExecutionAndPersistsReceipt(t *testing.T) {
	h, _ := testHost(t)
	a, b := newSession(t, h), newSession(t, h)
	runner := &roomRunner{started: make(chan harness.Request, 2), finish: map[string]chan struct{}{a.NativeSessionID: make(chan struct{}), b.NativeSessionID: make(chan struct{})}}
	h.Runner = runner
	done := make(chan state.Run, 2)
	for _, s := range []state.Session{a, b} {
		go func() { r, _ := h.Run(t.Context(), s, "bounded fixture"); done <- r }()
	}
	for range 2 {
		select {
		case <-runner.started:
		case <-time.After(10 * time.Second):
			t.Fatal("fixture did not start")
		}
	}
	run, err := h.Store.LatestSessionRun(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.Store.Home(), ".run-control-"+run.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("capability is not private")
	}
	var control struct {
		Port  int
		Token string
	}
	if err := json.Unmarshal(raw, &control); err != nil {
		t.Fatal(err)
	}
	request := func(method, token string) int {
		t.Helper()
		req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d/stop", control.Port), nil)
		req.Header.Set("X-Run-Token", token)
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if request("POST", "wrong") != 403 || request("GET", control.Token) != 405 {
		t.Fatal("control accepted an unauthorized request")
	}
	select {
	case <-done:
		t.Fatal("invalid request stopped a run")
	default:
	}
	if request("POST", control.Token) != 200 {
		t.Fatal("stop rejected")
	}
	select {
	case stopped := <-done:
		if stopped.ID != run.ID || stopped.ExecutionStatus != "canceled" {
			t.Fatalf("wrong stop outcome: %+v", stopped)
		}
		stored, err := h.Store.Run(t.Context(), run.ID)
		if err != nil || stored.Events[len(stored.Events)-1].Type != "run.finished" {
			t.Fatal("missing durable receipt", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not propagate")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("control capability survived completion")
	}
	select {
	case <-done:
		t.Fatal("sibling was canceled")
	default:
	}
	close(runner.finish[b.NativeSessionID])
	select {
	case completed := <-done:
		if completed.SessionID != b.ID || completed.ExecutionStatus != "completed" {
			t.Fatalf("sibling failed: %+v", completed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sibling did not finish")
	}
}

// The Web viewer is a Python consumer of the Go-created capability. This uses
// the real startRunControl endpoint and the real viewer module, with isolated
// fixture contexts only; no task or harness is started.
func TestRunControlCapabilityIsUsableByWebViewerModule(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("the Web viewer run-control check requires python3:", err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir()) // the viewer rejects symlinked paths
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := filepath.Abs(filepath.Join("..", "..", "tools", "run-viewer"))
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		id    string
		ctx   context.Context
		close func()
	}
	start := func() fixture {
		ctx, cancel := context.WithCancel(t.Context())
		id := ID()
		closeControl, err := startRunControl(home, id, cancel)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeControl(); cancel() })
		return fixture{id: id, ctx: ctx, close: closeControl}
	}
	target, sibling := start(), start()
	// Lock the producer shape that the viewer must accept, without printing it.
	var capability struct{ Token string }
	raw, err := os.ReadFile(filepath.Join(home, ".run-control-"+target.id+".json"))
	if err != nil || json.Unmarshal(raw, &capability) != nil {
		t.Fatal("capability is unreadable", err)
	}
	uuid4 := `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`
	if !regexp.MustCompile("^(?:" + uuid4 + "){2}$").MatchString(capability.Token) {
		t.Fatal("Host token is not two canonical UUIDv4 values")
	}
	script := "import json, sys\n" +
		"from pathlib import Path\n" +
		"sys.path.insert(0, sys.argv[1])\n" +
		"import run_control\n" +
		"home, run_id, action = Path(sys.argv[2]), sys.argv[3], sys.argv[4]\n" +
		"result = {'available': run_control.available(home, run_id)}\n" +
		"if action == 'stop':\n" +
		"    run_control.stop(home, run_id)\n" +
		"    result['stopped'] = True\n" +
		"print(json.dumps(result))\n"
	call := func(id, action string) map[string]bool {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), python, "-B", "-c", script, viewer, home, id, action)
		out, err := cmd.Output()
		if err != nil {
			var stderr []byte
			if exit, ok := err.(*exec.ExitError); ok {
				stderr = exit.Stderr
			}
			t.Fatalf("viewer %s failed: %v\n%s", action, err, stderr)
		}
		var result map[string]bool
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal("unexpected viewer output:", err)
		}
		return result
	}
	if !call(target.id, "check")["available"] || !call(sibling.id, "check")["available"] {
		t.Fatal("viewer rejected a Go-created capability")
	}
	if got := call(target.id, "stop"); !got["available"] || !got["stopped"] {
		t.Fatalf("viewer stop was not confirmed: %v", got)
	}
	select {
	case <-target.ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not cancel the selected fixture")
	}
	select {
	case <-sibling.ctx.Done():
		t.Fatal("stop canceled a sibling fixture")
	default:
	}
	target.close()
	if call(target.id, "check")["available"] {
		t.Fatal("viewer accepted a capability after the run ended")
	}
	if !call(sibling.id, "check")["available"] {
		t.Fatal("sibling capability was removed")
	}
}
