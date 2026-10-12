package terminal

import (
	"bytes"
	"strings"
	"testing"

	"agent-host/internal/event"
)

func TestRuntimePhaseLabelsShareStreamingAndChannelDefinitions(t *testing.T) {
	var out bytes.Buffer
	r := Renderer{Out: &out}
	for _, e := range []event.Event{
		{Type: "text.delta", Agent: "local-claude", Delta: "Checking documentation."},
		{Type: "phase.started", Phase: "knowledge_search", Status: "running", Message: "source=docs"},
		{Type: "phase.finished", Phase: "knowledge_search", Status: "failed", Message: "source=docs", Data: map[string]any{"duration_ms": 12}},
		{Type: "phase.resumed", Phase: "execute", Status: "running"},
		{Type: "text.delta", Agent: "local-claude", Delta: "The source was unavailable."},
	} {
		if err := r.Render(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"检索知识", "检索失败", "AI执行", "duration=12ms", "The source was unavailable."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	if strings.Count(out.String(), "local-claude ›") != 2 {
		t.Fatal("phase interrupted or duplicated stream")
	}
}
