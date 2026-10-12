package host

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/harness"
)

func TestRuntimePhasesFollowCallsResultsAndContinuation(t *testing.T) {
	var events []event.Event
	p := newRuntimePhases(func(e event.Event) error { events = append(events, e); return nil })
	observe := func(id, name, stage string, failed bool) {
		t.Helper()
		tool := &harness.ToolObservation{ID: id, Name: name, Stage: stage, IsError: failed}
		if err := p.observe(event.Event{Type: "tool.activity", Phase: "execute"}, tool); err != nil {
			t.Fatal(err)
		}
	}
	observe("s1", "mcp__knowledge_docs__search_dokki", "call", false)
	observe("r1", "mcp__knowledge_wiki__read_confluence_page", "call", false)
	observe("r1", "mcp__knowledge_wiki__read_confluence_page", "result", false)
	observe("s1", "mcp__knowledge_docs__search_dokki", "result", true)
	if err := p.observe(event.Event{Type: "text.delta", Delta: "Lookup failed; trying another source."}, nil); err != nil {
		t.Fatal(err)
	}
	observe("s2", "mcp__knowledge_docs__search_dokki", "call", false)
	observe("s2", "mcp__knowledge_docs__search_dokki", "result", false)
	observe("shell", "Bash", "call", false)
	if err := p.finish("completed"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, e := range events {
		if !strings.HasPrefix(e.Type, "phase.") {
			continue
		}
		got = append(got, e.Type+":"+e.Phase+":"+e.Status)
		if e.Type == "phase.started" && (i == 0 || events[i-1].Type != "tool.activity") {
			t.Fatal("phase preceded its source event")
		}
		if e.Type == "phase.finished" && e.Data["duration_ms"] == nil {
			t.Fatal("missing observed duration")
		}
	}
	want := []string{"phase.started:knowledge_search:running", "phase.started:knowledge_read:running", "phase.finished:knowledge_read:completed", "phase.resumed:knowledge_search:running", "phase.finished:knowledge_search:failed", "phase.resumed:execute:running", "phase.started:knowledge_search:running", "phase.finished:knowledge_search:completed", "phase.resumed:execute:running"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRuntimePhasesDoNotInventSuccessOrUnobservedSearch(t *testing.T) {
	for _, status := range []string{"completed", "canceled", "failed"} {
		t.Run(status, func(t *testing.T) {
			var events []event.Event
			p := newRuntimePhases(func(e event.Event) error { events = append(events, e); return nil })
			if err := p.observe(event.Event{Type: "text.delta", Delta: "search_dokki succeeded"}, nil); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatal("assistant text created a phase")
			}
			call := &harness.ToolObservation{ID: "missing", Name: "mcp__knowledge_docs__read_dokki_resource", Stage: "call"}
			if err := p.observe(event.Event{Type: "tool.activity"}, call); err != nil {
				t.Fatal(err)
			}
			if err := p.finish(status); err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			want := status
			if status == "completed" {
				want = "unknown"
			}
			if last.Type != "phase.finished" || last.Status != want {
				t.Fatalf("unpaired tool became success: %+v", last)
			}
		})
	}
	want := errors.New("journal failure")
	p := newRuntimePhases(func(event.Event) error { return want })
	if err := p.observe(event.Event{}, nil); !errors.Is(err, want) {
		t.Fatal("journal failure swallowed")
	}
}
