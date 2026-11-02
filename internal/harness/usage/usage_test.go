package usage

import (
	"agent-host/internal/event"
	"testing"
)

func TestACPUnknownBillingIsNotInferredFromExtractor(t *testing.T) {
	out := Summarize([]event.Event{
		{RunID: "one", Phase: "execute", Component: "harness.acp", Type: "agent.result", Data: map[string]any{"raw": map[string]any{"protocol": "acp", "stopReason": "end_turn"}}},
		{RunID: "one", Phase: "experience", Component: "harness.claude.extractor", Type: "agent.result", Data: map[string]any{"raw": map[string]any{"type": "result", "total_cost_usd": 0.02}}},
	})
	if out.Calls != 2 || out.MissingCosts != 1 || out.CostUSD == nil || *out.CostUSD != 0.02 {
		t.Fatalf("usage=%+v", out)
	}
}

func TestOffloadNativeResultsRetainUsageWithoutDoubleCountingPrivateRecord(t *testing.T) {
	raw := map[string]any{"type": "result", "uuid": "native-result", "usage": map[string]any{"input_tokens": 4, "output_tokens": 2}, "total_cost_usd": 0.01}
	out := Summarize([]event.Event{
		{RunID: "run", Phase: "execute", Component: "harness.offload", Type: "agent.result", Data: map[string]any{"raw": raw}},
		{RunID: "run", Phase: "execute", Component: "harness.offload", Type: "offload.result", Data: map[string]any{"raw": map[string]any{"status": "ok", "usage": raw["usage"]}}},
	})
	if out.Calls != 1 || out.CostUSD == nil || *out.CostUSD != 0.01 || out.Tokens["input_tokens"] == nil || *out.Tokens["input_tokens"] != 4 {
		t.Fatalf("%+v", out)
	}
}
