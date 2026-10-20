package usage

import (
	"encoding/json"
	"math"

	"agent-host/internal/event"
)

// Summary covers observed harness results, excluding Jev decisions. Missing
// ACP billing remains unknown. Cost is a native estimate, not a provider bill.
type Summary struct {
	MissingACPCosts int
	Calls           int
	CostUSD         *float64
	MissingCosts    int
	Tokens          map[string]*int64
	MissingTokens   map[string]int
}

// SummarizeUsage projects retained native evidence without changing the journal.
// Streaming snapshots and modelUsage are not additive to result-level usage.
func Summarize(events []event.Event) Summary {
	fields := []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"}
	out := Summary{Tokens: map[string]*int64{}, MissingTokens: map[string]int{}}
	seen := map[[4]string]bool{}
	for _, e := range events {
		if e.Component == "harness.acp" && e.Type == "agent.result" {
			out.Calls++
			out.MissingCosts++
			out.MissingACPCosts++
			for _, field := range fields {
				out.MissingTokens[field]++
			}
			continue
		}
		if e.Component != "harness.claude" && e.Component != "harness.claude.extractor" {
			continue
		}
		encoded, err := json.Marshal(e.Data["raw"])
		if err != nil {
			continue
		}
		var raw struct {
			Type  string          `json:"type"`
			UUID  string          `json:"uuid"`
			Usage json.RawMessage `json:"usage"`
			Cost  json.RawMessage `json:"total_cost_usd"`
		}
		if json.Unmarshal(encoded, &raw) != nil || raw.Type != "result" {
			continue
		}
		identity := raw.UUID
		if identity == "" {
			identity = string(encoded)
		}
		key := [4]string{e.RunID, e.Phase, e.Component, identity}
		if seen[key] {
			continue
		}
		seen[key] = true
		out.Calls++
		var cost *float64
		if json.Unmarshal(raw.Cost, &cost) == nil && cost != nil && *cost >= 0 && !math.IsInf(*cost, 0) && !math.IsNaN(*cost) {
			if out.CostUSD == nil {
				out.CostUSD = new(float64)
			}
			if !math.IsInf(*out.CostUSD+*cost, 0) {
				*out.CostUSD += *cost
			} else {
				out.MissingCosts++
			}
		} else {
			out.MissingCosts++
		}
		var tokens map[string]json.RawMessage
		_ = json.Unmarshal(raw.Usage, &tokens)
		for _, field := range fields {
			var value *int64
			if json.Unmarshal(tokens[field], &value) != nil || value == nil || *value < 0 {
				out.MissingTokens[field]++
				continue
			}
			if out.Tokens[field] == nil {
				out.Tokens[field] = new(int64)
			}
			if *out.Tokens[field] > math.MaxInt64-*value {
				out.MissingTokens[field]++
				continue
			}
			*out.Tokens[field] += *value
		}
	}
	return out
}
