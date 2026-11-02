package offload

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
)

type executorFunc func(context.Context, offloadexec.Request) (offloadexec.Result, error)

func (f executorFunc) Run(ctx context.Context, req offloadexec.Request) (offloadexec.Result, error) {
	return f(ctx, req)
}

func TestAdapterFreshAndContinuationPreserveEvidence(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "resume"}[resume], func(t *testing.T) {
			req := harness.Request{CallerRunID: "agh:run", Prompt: "visible task", HostInstructions: "private context", Workspace: "/workspace", WriteIntent: true, Resume: resume}
			want := offloadexec.Request{CallerRunID: "agh:run", Prompt: "visible task", Instructions: "private context", Workspace: "/workspace", Write: true}
			if resume {
				req.NativeSessionID, req.NativeSessionProfile = "session", "canonical"
				want.Session, want.ResumeProfile = "session", "canonical"
			}
			raw := json.RawMessage(`{"task":"visible task","status":"ok"}`)
			usage := json.RawMessage(`{"input_tokens":12}`)
			calls, observations := 0, 0
			adapter := New(executorFunc(func(_ context.Context, got offloadexec.Request) (offloadexec.Result, error) {
				calls++
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("request %+v; want %+v", got, want)
				}
				return offloadexec.Result{Status: "ok", Text: "answer", Model: "model", Session: "session", Profile: "canonical", Backend: "codex", RunID: "execution", CallerRunID: "agh:run", Raw: raw, Usage: usage}, nil
			}))
			result, err := adapter.Run(context.Background(), req, func(o harness.Observation) error {
				observations++
				if o.Type != "result" || o.Message != "completed" || len(o.Raw) != 0 || o.Tool != nil || o.Delta != "" {
					t.Fatalf("invented streaming event or exposed private evidence: %+v", o)
				}
				return nil
			})
			if err != nil || result.Status != "completed" || result.Text != "answer" || result.NativeSessionID != "session" || calls != 1 || observations != 1 {
				t.Fatalf("result %+v, error %v, calls %d, observations %d", result, err, calls, observations)
			}
			if e := result.Evidence; e == nil || e.RunID != "execution" || e.CallerRunID != req.CallerRunID || e.Profile != "canonical" || e.Backend != "codex" || string(e.Raw) != string(raw) || string(e.Usage) != string(usage) {
				t.Fatalf("lost evidence: %+v", e)
			}
			if identity := harness.RunnerIdentity(adapter); identity.Component != "harness.offload" || identity.Engine != "offload" || identity.Transport != "exec" {
				t.Fatalf("identity %+v", identity)
			}
		})
	}
}

func TestAdapterRejectsUnsupportedControlsBeforeDispatch(t *testing.T) {
	cases := map[string]harness.Request{
		"native profile": {NativeProfile: "/native"}, "permission": {PermissionMode: "bypassPermissions"},
		"tools disabled": {ToolsDisabled: true}, "restricted": {RestrictedReadOnly: true},
		"allowlist": {AllowedTools: []string{"Read"}}, "mcp": {MCPServers: []harness.MCPServer{{Name: "tool"}}},
		"schema": {JSONSchema: "{}"}, "extraction": {Extraction: true},
		"skill package": {SkillPluginDir: "/skills"}, "skill digest": {SkillPluginDigest: "digest"},
		"model override": {Model: "other"}, "image": {Images: []harness.Image{{Name: "image"}}},
		"directories": {AdditionalDirectories: []string{"/other"}}, "fork": {ForkFromNativeSessionID: "parent"},
		"resume without identity": {Resume: true}, "resume without profile": {Resume: true, NativeSessionID: "session"},
		"resume without session": {Resume: true, NativeSessionProfile: "canonical"},
		"fresh bound session":    {NativeSessionID: "session"}, "fresh profile": {NativeSessionProfile: "canonical"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			adapter := New(executorFunc(func(context.Context, offloadexec.Request) (offloadexec.Result, error) {
				t.Fatal("unsupported policy reached the executor")
				return offloadexec.Result{}, nil
			}))
			result, err := adapter.Run(context.Background(), req, nil)
			var executionErr *harness.ExecutionError
			if result.Status != "failed" || !errors.As(err, &executionErr) || executionErr.Kind != harness.FailureStartup {
				t.Fatalf("result %+v, error %v", result, err)
			}
		})
	}
}

func TestAdapterFailureClassificationWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name, status, errorKind string
		cause                   error
		want                    harness.FailureKind
	}{
		{"start", "error", offloadexec.KindStart, errors.New("start failed"), harness.FailureStartup},
		{"invalid output", "error", offloadexec.KindInvalidOutput, errors.New("bad output"), harness.FailureProcess},
		{"timeout", "timeout", offloadexec.KindOffloadTimeout, errors.New("timeout"), harness.FailureDeadline},
		{"canceled", "canceled", offloadexec.KindCanceled, context.Canceled, harness.FailureCanceled},
		{"deadline", "timeout", offloadexec.KindDeadline, context.DeadlineExceeded, harness.FailureDeadline},
		{"inconsistent success", "ok", "", errors.New("failed despite ok"), harness.FailureProcess},
		{"missing failure", "error", "", nil, harness.FailureProcess},
		{"unknown", "unknown", "", nil, harness.FailureProcess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			adapter := New(executorFunc(func(context.Context, offloadexec.Request) (offloadexec.Result, error) {
				calls++
				return offloadexec.Result{Status: tc.status, ErrorKind: tc.errorKind, ExitCode: 1, Raw: json.RawMessage(`{"partial":true}`)}, tc.cause
			}))
			result, err := adapter.Run(context.Background(), harness.Request{}, nil)
			var executionErr *harness.ExecutionError
			if result.Status == "completed" || calls != 1 || !errors.As(err, &executionErr) || executionErr.Kind != tc.want || result.Evidence == nil || len(result.Evidence.Raw) == 0 {
				t.Fatalf("result %+v, error %v, calls %d", result, err, calls)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatal("original error chain lost")
			}
			if (tc.want == harness.FailureCanceled) != (result.Status == "canceled") {
				t.Fatalf("cancellation status %+v", result)
			}
		})
	}
}

func TestAdapterPreCanceledAndMissingExecutor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter := New(executorFunc(func(context.Context, offloadexec.Request) (offloadexec.Result, error) {
		t.Fatal("canceled request dispatched")
		return offloadexec.Result{}, nil
	}))
	result, err := adapter.Run(ctx, harness.Request{}, nil)
	if result.Status != "canceled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("result %+v, error %v", result, err)
	}
	if _, err := New(nil).Run(context.Background(), harness.Request{}, nil); err == nil {
		t.Fatal("missing executor accepted")
	}
}

func TestAdapterObserverFailurePreservesCompletedExecution(t *testing.T) {
	observerErr := errors.New("observer failed")
	adapter := New(executorFunc(func(context.Context, offloadexec.Request) (offloadexec.Result, error) {
		return offloadexec.Result{Status: "ok", Text: "answer"}, nil
	}))
	result, err := adapter.Run(context.Background(), harness.Request{}, func(harness.Observation) error { return observerErr })
	if result.Status != "completed" || result.Text != "answer" || !errors.Is(err, observerErr) {
		t.Fatalf("result %+v, error %v", result, err)
	}
}
