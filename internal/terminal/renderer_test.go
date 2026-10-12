package terminal

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
)

func TestTerminalShowsStageAgentModelAndDuration(t *testing.T) {
	var output bytes.Buffer
	r := Renderer{Out: &output}
	e := event.Event{Version: event.Version, At: time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC), Type: "phase.finished", Phase: "execute", Component: "harness.claude", Agent: "local-claude", Model: "resolved-model", Status: "completed", Message: "completed", ElapsedMS: 2500, Data: map[string]any{"duration_ms": int64(1234)}}
	if err := r.Render(e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+2.50s", "execute", "harness.claude", "agent=local-claude", "model=resolved-model", "duration=1234ms"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q missing %q", output.String(), want)
		}
	}
}

func TestTerminalStreamsWithoutRepeatingTextAndStripsControls(t *testing.T) {
	var output bytes.Buffer
	r := Renderer{Out: &output}
	events := []event.Event{
		{Type: "text.delta", Agent: "claude", Delta: "你\x1b[31m好"},
		{Type: "native.evidence", Message: "whole duplicate answer", Data: map[string]any{"raw": "private evidence"}},
		{Type: "text.delta", Agent: "claude", Delta: "\x00\x07\r，世界\n"},
		{Type: "phase.finished", Message: "done\x1b\x00\nnext", Phase: "execute", Component: "harness", Status: "completed", At: time.Now()},
	}
	for _, e := range events {
		if err := r.Render(e); err != nil {
			t.Fatal(err)
		}
	}
	s := output.String()
	if strings.ContainsAny(s, "\x1b\x00\x07\r") || strings.Contains(s, "duplicate") || strings.Contains(s, "private evidence") {
		t.Fatalf("unsafe or duplicated terminal output: %q", s)
	}
	if strings.Count(s, "claude ›") != 1 || !strings.Contains(s, "你[31m好，世界\n") || !strings.Contains(s, "done ↵ next") {
		t.Fatalf("stream continuity lost: %q", s)
	}
}

func TestJSONPreservesStructuredEventAndOriginalDelta(t *testing.T) {
	var output bytes.Buffer
	r := Renderer{Out: &output, JSON: true}
	e := event.Event{Version: event.Version, Seq: 7, Type: "text.delta", RunID: "run", SessionID: "session", Delta: "a\x1bb\n", Data: map[string]any{"duration_ms": 42}}
	if err := r.Render(e); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte{'\n'}) != 1 || bytes.Contains(output.Bytes(), []byte{0x1b}) {
		t.Fatalf("event was not one escaped NDJSON line: %q", output.String())
	}
	var decoded event.Event
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &decoded); err != nil || decoded.Delta != e.Delta || decoded.Seq != e.Seq || decoded.RunID != e.RunID {
		t.Fatalf("structured output changed original evidence: %+v err=%v", decoded, err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestOutputErrorsReachCaller(t *testing.T) {
	want := errors.New("broken output")
	for _, format := range []bool{false, true} {
		r := Renderer{Out: failingWriter{want}, JSON: format}
		if err := r.Render(event.Event{Type: "text.delta", Delta: "answer"}); !errors.Is(err, want) {
			t.Fatalf("format JSON=%t swallowed output error: %v", format, err)
		}
	}
}

func TestProgressGapIsVisibleWithoutChangingOutcomeOrRawEvidence(t *testing.T) {
	const cause = "progress record failed /private/state secret=value"
	e := event.Event{Type: "phase.finished", Phase: "execute", Status: "completed", Message: "completed", Data: map[string]any{"progress_error": cause}}
	var text bytes.Buffer
	if err := (&Renderer{Out: &text}).Render(e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "completed") || !strings.Contains(text.String(), "进度提示未能完整记录") || strings.Contains(text.String(), cause) {
		t.Fatal("progress warning lost outcome or reflected raw diagnostic", text.String())
	}
	var raw bytes.Buffer
	if err := (&Renderer{Out: &raw, JSON: true}).Render(e); err != nil {
		t.Fatal(err)
	}
	var saved event.Event
	if err := json.Unmarshal(raw.Bytes(), &saved); err != nil || saved.Data["progress_error"] != cause || saved.Status != "completed" {
		t.Fatal("structured progress failure evidence was lost", err)
	}
}
