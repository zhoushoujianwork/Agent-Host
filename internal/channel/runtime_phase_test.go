package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type runtimeStageRunner struct{}

func (runtimeStageRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	for i, name := range []string{"mcp__knowledge_docs__search_dokki", "mcp__knowledge_wiki__read_confluence_page", "mcp__knowledge_docs__search_dokki"} {
		id := fmt.Sprintf("lookup-%d", i)
		for _, o := range []harness.Observation{
			{Type: "tool_start", Tool: &harness.ToolObservation{ID: id, Name: name, Stage: "call", Input: json.RawMessage(`{"query":"fixture"}`)}},
			{Type: "tool_result", Tool: &harness.ToolObservation{ID: id, Name: name, Stage: "result", Result: json.RawMessage(`{"content":"fixture result"}`), IsError: i == 2}},
			{Type: "text_delta", Delta: "Continuing with the available evidence."},
		} {
			if err := emit(o); err != nil {
				return harness.Result{Status: "unknown"}, err
			}
		}
	}
	return harness.Result{Status: "completed", Text: "One source lookup failed; only available evidence was used.", Model: "fixture", NativeSessionID: req.NativeSessionID}, nil
}

func TestRuntimeKnowledgePhasesStayLocalWithGenericPublicProgress(t *testing.T) {
	f := newServiceFixture(t)
	f.host.Runner = runtimeStageRunner{}
	f.host.Extractor = phaseExtractor(func(context.Context, state.Run, []state.ToolEvidence, func(harness.Observation) error) (experience.Extraction, error) {
		return experience.Extraction{Model: "fixture"}, nil
	})
	f.host.Judge = &phaseJudge{}
	input := f.message(t, "runtime-knowledge", "查一下资料。", "text", "runtime-dm")
	messageID := Key("dingtalk", f.service.ClientID, input.ID)
	var labels []string
	f.host.Sink = func(e event.Event) error {
		label, render := event.PhaseReaction(e)
		if !render {
			return nil
		}
		rows, err := f.service.Store.ChannelReactions(context.Background(), messageID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Stage != "received" && row.Stage != "processing" {
				return fmt.Errorf("native knowledge phase leaked: %+v", row)
			}
		}
		if e.Data["parent_phase"] == "execute" || e.Type == "phase.resumed" {
			labels = append(labels, label)
		}
		drainReactions(t, f.service)
		return nil
	}
	f.process(t, input)
	drainReactions(t, f.service)
	want := []string{"检索知识", "AI执行", "阅读资料", "AI执行", "检索知识", "检索失败", "AI执行"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("runtime labels %v want %v", labels, want)
	}
	all := addedReactionLabels(t, f, input.ConversationID, input.ID)
	counts := map[string]int{}
	for _, label := range all {
		counts[label]++
	}
	if len(counts) != 3 || counts["已收到"] != 1 || counts["处理中"] != 1 || counts["已完成"] != 1 {
		t.Fatalf("reaction delivery lost phase occurrences: %v", all)
	}
}
