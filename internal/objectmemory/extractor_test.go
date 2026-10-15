package objectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type runnerFunc func(context.Context, harness.Request, func(harness.Observation) error) (harness.Result, error)

func (f runnerFunc) Run(c context.Context, r harness.Request, e func(harness.Observation) error) (harness.Result, error) {
	return f(c, r, e)
}

func toolInput() Input {
	now := time.Now().UTC()
	failed := false
	return Input{Text: "Use the supported --profile option for CloudSDK disk queries.", Kind: "procedure", Source: state.ExternalSource{Entry: "test", ObservedAt: now}, Evidence: []state.ExternalEvidence{{ID: "call", Role: "tool", Author: "test", Text: "Observed command and disk response", ObservedAt: now, Tool: &state.ExternalTool{Name: "Bash", Input: json.RawMessage(`{"command":"CloudSDK disk list --profile default"}`), Result: json.RawMessage(`{"disks":[]}`), IsError: &failed}}}}
}

func TestExtractionUsesToolEvidenceAndIsIsolated(t *testing.T) {
	var workspace string
	runner := runnerFunc(func(_ context.Context, r harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
		if !r.ToolsDisabled || !r.Extraction || r.Resume || len(r.MCPServers) > 0 || len(r.AllowedTools) > 0 || r.NativeSessionID == "" || !json.Valid([]byte(r.JSONSchema)) {
			t.Fatalf("unsafe request: %+v", r)
		}
		if !strings.Contains(r.Prompt, "CloudSDK disk list") || strings.Contains(r.Prompt, "HostInstructions") {
			t.Fatal("missing source")
		}
		workspace = r.Workspace
		if _, err := os.Stat(workspace); err != nil {
			t.Fatal(err)
		}
		return harness.Result{Status: "completed", Model: "resolved", StructuredOutput: json.RawMessage(`{"status":"identified","reason":"The observed CLI is the subject.","objects":[{"name":"CloudSDK","kind":"tool","evidence_id":"call"}]}`)}, nil
	})
	out, err := New(runner, "requested").Extract(t.Context(), toolInput())
	if err != nil || len(out.Objects) != 1 || out.Objects[0].Name != "CloudSDK" || out.Objects[0].Evidence.Role != "tool" || !json.Valid(out.Audit) {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace retained", err)
	}
}

func TestExtractionFailsClosed(t *testing.T) {
	for _, output := range []string{
		`{"status":"unresolved","reason":"No subject supplied","objects":[]}`,
		`{"status":"identified","reason":"empty","objects":[]}`,
		`{"status":"identified","reason":"made up","objects":[{"name":"InventedCloud","kind":"tool","evidence_id":"call"}]}`,
		`{"status":"identified","reason":"bad reference","objects":[{"name":"CloudSDK","kind":"tool","evidence_id":"unknown"}]}`,
		`{"status":"identified","reason":"missing objects"}`,
		`not json`,
	} {
		runner := runnerFunc(func(context.Context, harness.Request, func(harness.Observation) error) (harness.Result, error) {
			return harness.Result{Status: "completed", Model: "resolved", StructuredOutput: json.RawMessage(output)}, nil
		})
		if _, err := New(runner, "test").Extract(t.Context(), toolInput()); err == nil {
			t.Fatal("invalid result admitted", output)
		}
	}
	runner := runnerFunc(func(_ context.Context, _ harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
		return harness.Result{}, emit(harness.Observation{Tool: &harness.ToolObservation{}})
	})
	if _, err := New(runner, "test").Extract(t.Context(), toolInput()); err == nil {
		t.Fatal("tool attempt permitted")
	}
	if _, err := New(nil, "test").Extract(t.Context(), toolInput()); err == nil {
		t.Fatal("missing provider permitted")
	}
}
