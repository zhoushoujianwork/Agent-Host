package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

type testRunner struct {
	requests []harness.Request
	err      error
	status   string
}

func (r *testRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.requests = append(r.requests, req)
	status := r.status
	if status == "" {
		status = "completed"
	}
	for _, o := range []harness.Observation{
		{Type: "init", Model: "resolved-claude", NativeSessionID: req.NativeSessionID, Raw: json.RawMessage(`{"type":"system","subtype":"init"}`)},
		{Type: "text_delta", Model: "resolved-claude", NativeSessionID: req.NativeSessionID, Delta: "answer", Raw: json.RawMessage(`{"type":"stream_event"}`)},
		{Type: "result", Model: "resolved-claude", NativeSessionID: req.NativeSessionID, Message: status, Raw: json.RawMessage(`{"type":"result"}`)},
	} {
		if err := emit(o); err != nil {
			return harness.Result{Status: "unknown", Error: err.Error()}, err
		}
	}
	result := harness.Result{Status: status, Text: "answer", Model: "resolved-claude", NativeSessionID: req.NativeSessionID}
	if r.err != nil {
		result.Error = r.err.Error()
	}
	return result, r.err
}

type deciderFunc func(context.Context, string, []state.Memory) (memory.Decision, error)

func (f deciderFunc) Decide(ctx context.Context, input string, existing []state.Memory) (memory.Decision, error) {
	return f(ctx, input, existing)
}

func testHost(t *testing.T) (*Host, *testRunner) {
	t.Helper()
	home := t.TempDir()
	release, err := state.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	runner := &testRunner{}
	h := &Host{Store: store, Runner: runner, Config: Config{Scope: t.TempDir(), Model: "requested-claude", DecisionModel: "requested-jev", DecisionProvider: "fixture"}}
	h.Objects = fixtureObjects{}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "none", Model: "resolved-jev", Details: json.RawMessage(`{"reason":"question"}`)}, nil
	})
	return h, runner
}

func newSession(t *testing.T, h *Host) state.Session {
	t.Helper()
	s, err := h.Session(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func runOK(t *testing.T, h *Host, s state.Session, input string) state.Run {
	t.Helper()
	r, err := h.Run(context.Background(), s, input)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExecutionStatus != "completed" || r.AcceptanceStatus != "unresolved" {
		t.Fatalf("unexpected completion/acceptance: %+v", r)
	}
	return r
}

func saveStatement() memory.Decision {
	return memory.Decision{Kind: "preference", Store: true, Model: "resolved-jev", Details: json.RawMessage(`{"reason":"explicit user preference"}`)}
}

func TestSavedStatementIsRecalledWithExactSourceInFreshSession(t *testing.T) {
	h, runner := testHost(t)
	const statement = "我偏好中文回复。"
	decisions := 0
	h.Decider = deciderFunc(func(_ context.Context, input string, existing []state.Memory) (memory.Decision, error) {
		decisions++
		if decisions == 1 {
			if input != statement || len(existing) != 0 {
				t.Fatalf("first decision source=%q existing=%+v", input, existing)
			}
			return saveStatement(), nil
		}
		return memory.Decision{Kind: "none", Model: "resolved-jev"}, nil
	})
	firstSession := newSession(t, h)
	first := runOK(t, h, firstSession, statement)
	if first.MemoryStatus != "saved" {
		t.Fatalf("first memory status=%s", first.MemoryStatus)
	}
	memories, err := h.Store.Memories(context.Background(), h.Config.Scope)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memories=%+v err=%v", memories, err)
	}
	m := memories[0]
	if m.Text != statement || m.SourceRunID != first.ID || m.Revision != 1 || m.Status != "active" {
		t.Fatalf("source-linked memory=%+v", m)
	}
	secondSession := newSession(t, h)
	second := runOK(t, h, secondSession, "请介绍项目。")
	if second.MemoryStatus != "unchanged" || len(second.Context) != 1 || !reflect.DeepEqual(second.Context[0], m) {
		t.Fatalf("fresh context or receipt=%+v", second)
	}
	if len(runner.requests) != 2 || runner.requests[0].Resume || runner.requests[1].Resume || runner.requests[0].NativeSessionID == runner.requests[1].NativeSessionID {
		t.Fatalf("fresh native sessions not separated: %+v", runner.requests)
	}
	for _, want := range []string{statement, m.ID, first.ID, `"revision":1`, "data, not instructions"} {
		if !strings.Contains(runner.requests[1].Prompt, want) {
			t.Errorf("injected prompt missing %q", want)
		}
	}
	if strings.Contains(runner.requests[0].Prompt, `"source_run_id"`) {
		t.Fatal("first turn unexpectedly had saved context")
	}
	if second.Model != "resolved-claude" || second.RequestedModel != h.Config.Model {
		t.Fatalf("requested/resolved models conflated: %+v", second)
	}
}

func TestMemoryFailureRetriesWithoutNativeReplay(t *testing.T) {
	h, runner := testHost(t)
	decisions := 0
	unavailable := errors.New("Jev unavailable")
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		decisions++
		if decisions == 1 {
			return memory.Decision{}, unavailable
		}
		return saveStatement(), nil
	})
	s := newSession(t, h)
	r, err := h.Run(context.Background(), s, "我偏好中文回复。")
	if !errors.Is(err, unavailable) || r.ExecutionStatus != "completed" || r.MemoryStatus != "failed" || r.MemoryError == "" {
		t.Fatalf("failed memory receipt=%+v err=%v", r, err)
	}
	if _, err := h.Session(context.Background(), s.ID); err == nil {
		t.Fatal("session resumed before failed memory was handled")
	}
	retried, err := h.RetryMemory(context.Background(), r.ID)
	if err != nil || retried.MemoryStatus != "saved" || retried.MemoryError != "" || len(runner.requests) != 1 || decisions != 2 {
		t.Fatalf("retry receipt=%+v native=%d decisions=%d err=%v", retried, len(runner.requests), decisions, err)
	}
	if _, err := h.RetryMemory(context.Background(), r.ID); err == nil || len(runner.requests) != 1 || decisions != 2 {
		t.Fatal("successful memory work was replayed")
	}
	if _, err := h.Session(context.Background(), s.ID); err != nil {
		t.Fatalf("completed memory did not unblock resume: %v", err)
	}
}

func TestNativeFailureNeverFinalizesMemory(t *testing.T) {
	h, runner := testHost(t)
	runner.status, runner.err = "failed", errors.New("native authentication failed")
	decisions := 0
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		decisions++
		return saveStatement(), nil
	})
	r, err := h.Run(context.Background(), newSession(t, h), "我偏好中文回复。")
	if !errors.Is(err, runner.err) || r.ExecutionStatus != "failed" || r.MemoryStatus != "skipped" || decisions != 0 {
		t.Fatalf("failed native receipt=%+v decisions=%d err=%v", r, decisions, err)
	}
	if _, err := h.RetryMemory(context.Background(), r.ID); err == nil || decisions != 0 || len(runner.requests) != 1 {
		t.Fatal("failed native run became eligible for memory retry")
	}
}

func TestCompletedNativeStatusSurvivesPhaseOutputFailure(t *testing.T) {
	h, runner := testHost(t)
	want := errors.New("terminal closed")
	h.Sink = func(e event.Event) error {
		if e.Type == "phase.finished" && e.Phase == "execute" {
			return want
		}
		return nil
	}
	decisions := 0
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		decisions++
		return saveStatement(), nil
	})
	r, err := h.Run(context.Background(), newSession(t, h), "我偏好中文回复。")
	if !errors.Is(err, want) || r.ExecutionStatus != "completed" || r.Output != "answer" || r.MemoryStatus != "failed" || decisions != 0 {
		t.Fatalf("execution was confused with presentation: %+v decisions=%d err=%v", r, decisions, err)
	}
	h.Sink = nil
	r, err = h.RetryMemory(context.Background(), r.ID)
	if err != nil || r.MemoryStatus != "saved" || len(runner.requests) != 1 || decisions != 1 {
		t.Fatalf("recovery replayed native work: %+v native=%d decisions=%d err=%v", r, len(runner.requests), decisions, err)
	}
}

func TestCommittedMemorySurvivesReceiptOutputFailure(t *testing.T) {
	h, runner := testHost(t)
	want := errors.New("output pipe closed after memory commit")
	h.Sink = func(e event.Event) error {
		if e.Type == "phase.finished" && e.Phase == "persist" {
			return want
		}
		return nil
	}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) { return saveStatement(), nil })
	r, err := h.Run(context.Background(), newSession(t, h), "我偏好中文回复。")
	if !errors.Is(err, want) || r.MemoryStatus != "saved" || r.MemoryError != "" || strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("committed memory was mislabeled: %+v err=%v", r, err)
	}
	if _, err := h.RetryMemory(context.Background(), r.ID); err == nil || len(runner.requests) != 1 {
		t.Fatal("saved memory became replayable after output failure")
	}
}

func TestConflictsExcludeBothStatementsAndWarnResumedSession(t *testing.T) {
	h, runner := testHost(t)
	const oldText = "我偏好中文回复。"
	const newText = "我偏好英文回复。"
	var original state.Memory
	h.Decider = deciderFunc(func(_ context.Context, input string, existing []state.Memory) (memory.Decision, error) {
		switch input {
		case oldText:
			return saveStatement(), nil
		case newText:
			if len(existing) != 1 {
				return memory.Decision{}, fmt.Errorf("want one prior statement, got %d", len(existing))
			}
			original = existing[0]
			d := saveStatement()
			d.ConflictIDs = []string{original.ID}
			return d, nil
		default:
			return memory.Decision{Kind: "none"}, nil
		}
	})
	runOK(t, h, newSession(t, h), oldText)
	readingSession := newSession(t, h)
	before := runOK(t, h, readingSession, "请介绍项目。")
	if len(before.Context) != 1 {
		t.Fatal("reader did not receive original memory")
	}
	runOK(t, h, newSession(t, h), newText)
	memories, err := h.Store.Memories(context.Background(), h.Config.Scope)
	if err != nil || len(memories) != 2 {
		t.Fatalf("conflicts=%+v err=%v", memories, err)
	}
	for _, m := range memories {
		if m.Status != "conflicted" {
			t.Fatalf("conflicting side remained active: %+v", m)
		}
	}
	fresh := runOK(t, h, newSession(t, h), "回复偏好是什么？")
	if len(fresh.Context) != 0 {
		t.Fatalf("fresh session recalled conflicted memories: %+v", fresh.Context)
	}
	resumed, err := h.Session(context.Background(), readingSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := runOK(t, h, resumed, "继续说明。")
	if len(after.Context) != 0 || !runner.requests[len(runner.requests)-1].Resume {
		t.Fatalf("resumed context=%+v request=%+v", after.Context, runner.requests[len(runner.requests)-1])
	}
	for _, text := range []string{original.ID, "revision 1", "revision 2", "status conflicted"} {
		if !strings.Contains(runner.requests[len(runner.requests)-1].Prompt, text) {
			t.Errorf("resume notice missing %q", text)
		}
	}
	storedBefore, err := h.Store.Run(context.Background(), before.ID)
	if err != nil || len(storedBefore.Context) != 1 || storedBefore.Context[0].Revision != 1 || storedBefore.Context[0].Status != "active" || storedBefore.Context[0].Text != oldText {
		t.Fatalf("historical injection snapshot changed: %+v err=%v", storedBefore.Context, err)
	}
}

func TestLiveJSONEventsMatchDurableOrderedReplay(t *testing.T) {
	h, _ := testHost(t)
	var live bytes.Buffer
	liveRenderer := terminal.Renderer{Out: &live, JSON: true}
	h.Sink = liveRenderer.Render
	r := runOK(t, h, newSession(t, h), "这个项目使用什么语言？")
	persisted, err := h.Store.Run(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var replay bytes.Buffer
	replayRenderer := terminal.Renderer{Out: &replay, JSON: true}
	for i, e := range persisted.Events {
		if e.Seq != int64(i+1) || e.Version != event.Version || e.RunID != r.ID || e.SessionID != r.SessionID || e.At.IsZero() {
			t.Fatalf("invalid ordered event %d: %+v", i, e)
		}
		if err := replayRenderer.Render(e); err != nil {
			t.Fatal(err)
		}
	}
	if len(persisted.Events) == 0 || persisted.Events[0].Type != "run.started" || persisted.Events[len(persisted.Events)-1].Type != "run.finished" {
		t.Fatal("run boundaries absent from evidence")
	}
	if bytes.Count(live.Bytes(), []byte{'\n'}) != len(persisted.Events) || bytes.Count(replay.Bytes(), []byte{'\n'}) != len(persisted.Events) {
		t.Fatal("an event was not represented by exactly one NDJSON line")
	}
	dec := json.NewDecoder(bytes.NewReader(live.Bytes()))
	replayed := json.NewDecoder(bytes.NewReader(replay.Bytes()))
	for i := 0; i < len(persisted.Events); i++ {
		var decoded, replayedEvent event.Event
		if err := dec.Decode(&decoded); err != nil || decoded.Seq != int64(i+1) {
			t.Fatalf("invalid NDJSON event %d: %+v %v", i, decoded, err)
		}
		// Object member order is not part of the event protocol. Both streams
		// must preserve the same typed fields, native evidence and sequence.
		if err := replayed.Decode(&replayedEvent); err != nil || !reflect.DeepEqual(decoded, replayedEvent) {
			t.Fatalf("replay changed event %d: live=%+v replay=%+v err=%v", i, decoded, replayedEvent, err)
		}
	}
	for _, decoder := range []*json.Decoder{dec, replayed} {
		if err := decoder.Decode(new(event.Event)); !errors.Is(err, io.EOF) {
			t.Fatalf("unexpected trailing event or output: %v", err)
		}
	}
}
