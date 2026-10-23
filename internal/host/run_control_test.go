package host

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
