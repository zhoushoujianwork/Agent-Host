package claude

import (
	"encoding/json"
	"testing"

	"agent-host/internal/event"
)

func TestSummarizeUsageIgnoresStreamingAndDuplicateResults(t *testing.T) {
	raw := json.RawMessage(`{"type":"result","uuid":"one","total_cost_usd":0.125,"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":30},"modelUsage":{"model":{"inputTokens":999,"costUSD":99}}}`)
	execute := event.Event{RunID: "run", Phase: "execute", Component: "harness.claude", Data: map[string]any{"raw": raw}}
	extract := event.Event{RunID: "run", Phase: "extract", Component: "harness.claude.extractor", Data: map[string]any{"raw": json.RawMessage(`{"type":"result","uuid":"two","total_cost_usd":0.25,"usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":0}}`)}}
	stream := event.Event{Component: "harness.claude", Data: map[string]any{"raw": json.RawMessage(`{"type":"stream_event","total_cost_usd":100,"usage":{"input_tokens":10000}}`)}}
	other := execute
	other.Component = "unrelated"
	events := []event.Event{stream, execute, execute, extract, other}
	for _, persisted := range []bool{false, true} {
		if persisted {
			encoded, _ := json.Marshal(events)
			if err := json.Unmarshal(encoded, &events); err != nil {
				t.Fatal(err)
			}
		}
		got := SummarizeUsage(events)
		if got.Calls != 2 || got.CostUSD == nil || *got.CostUSD != 0.375 || *got.Tokens["input_tokens"] != 14 || *got.Tokens["output_tokens"] != 5 || *got.Tokens["cache_read_input_tokens"] != 30 || got.Tokens["cache_creation_input_tokens"] != nil {
			t.Fatalf("invalid summary after persisted=%v: %+v", persisted, got)
		}
	}
}

func TestSummarizeUsageKeepsMissingAndInvalidMetricsUnknown(t *testing.T) {
	for _, raw := range []string{
		`{"type":"result"}`,
		`{"type":"result","total_cost_usd":null,"usage":null}`,
		`{"type":"result","total_cost_usd":-1,"usage":{"input_tokens":-1,"output_tokens":true,"cache_read_input_tokens":"7"}}`,
	} {
		got := SummarizeUsage([]event.Event{{Component: "harness.claude", Data: map[string]any{"raw": json.RawMessage(raw)}}})
		if got.Calls != 1 || got.CostUSD != nil || got.MissingCosts != 1 || len(got.Tokens) != 0 || got.MissingTokens["input_tokens"] != 1 {
			t.Fatalf("invented usage: %+v", got)
		}
	}
	got := SummarizeUsage([]event.Event{{Component: "harness.claude", Data: map[string]any{"raw": json.RawMessage(`{"type":"result","total_cost_usd":0,"usage":{"input_tokens":0}}`)}}})
	if got.CostUSD == nil || *got.CostUSD != 0 || got.Tokens["input_tokens"] == nil || *got.Tokens["input_tokens"] != 0 {
		t.Fatal("reported zero lost", got)
	}
	got = SummarizeUsage([]event.Event{{Component: "harness.claude", Data: map[string]any{"raw": json.RawMessage(`{"type":"result","total_cost_usd":0.25,"usage":"invalid"}`)}}})
	if got.CostUSD == nil || *got.CostUSD != 0.25 || len(got.Tokens) != 0 || got.MissingTokens["input_tokens"] != 1 {
		t.Fatal("invalid tokens hid independently reported cost", got)
	}
}
