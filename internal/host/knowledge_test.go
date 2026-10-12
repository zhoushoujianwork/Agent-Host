package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/knowledge"
)

type knowledgeOnlyRunner struct{}

func (knowledgeOnlyRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	for _, tool := range []struct{ id, name string }{
		{"catalog", "ToolSearch"},
		{"search", "mcp__knowledge_docs__search_confluence"},
		{"page", "mcp__knowledge_docs__read_confluence_page"},
	} {
		for _, observation := range []harness.ToolObservation{
			{ID: tool.id, Name: tool.name, Stage: "call", Input: json.RawMessage(`{}`)},
			{ID: tool.id, Name: tool.name, Stage: "result", Result: json.RawMessage(`{"reference":"source material"}`)},
		} {
			raw, _ := json.Marshal(observation)
			if err := emit(harness.Observation{Type: "native", NativeSessionID: req.NativeSessionID, Tool: &observation, Raw: raw}); err != nil {
				return harness.Result{Status: "unknown"}, err
			}
		}
	}
	return harness.Result{Status: "completed", Text: "The document describes a possible procedure.", NativeSessionID: req.NativeSessionID}, nil
}

func TestKnowledgeContextAndRevocationBeforeDispatch(t *testing.T) {
	h, runner := testHost(t)
	h.Config.MCPServers = []harness.MCPServer{{Name: "knowledge_docs", Command: "/fixture/agent-host"}}
	h.Config.KnowledgeHints = []knowledge.Hint{{Name: "docs", Provider: "dokki", Description: "Reference only", Tools: []string{"search_dokki"}}}
	s := newSession(t, h)
	runOK(t, h, s, "Find the API reference.")
	if len(runner.requests) != 1 || len(runner.requests[0].MCPServers) != 1 || !strings.Contains(runner.requests[0].Prompt, "Reference only") || !strings.Contains(runner.requests[0].Prompt, "not execution evidence") {
		t.Fatalf("missing source catalog/mount: %+v", runner.requests)
	}
	h.Config.ValidateKnowledge = func() error { return errors.New("source revoked") }
	r, err := h.Run(context.Background(), s, "Continue.")
	if err == nil || len(runner.requests) != 1 || r.ExecutionStatus != "failed" {
		t.Fatalf("revoked source dispatched: %s %v", r.ExecutionStatus, err)
	}
}

func TestKnowledgeOnlyRunKeepsEvidenceWithoutExtractingExperience(t *testing.T) {
	h, _ := testHost(t)
	h.Runner = knowledgeOnlyRunner{}
	// A reference-only turn must finish without requiring either model stage.
	h.Extractor, h.Judge = nil, nil
	r := runOK(t, h, newSession(t, h), "Find and read the documented procedure.")
	if r.ExperienceStatus != "empty" || r.MemoryStatus != "unchanged" {
		t.Fatalf("knowledge-only turn produced experience: %+v", r)
	}
	evidence, err := h.Store.ToolEvidence(context.Background(), r.ID)
	if err != nil || len(evidence) != 3 {
		t.Fatalf("reference evidence was discarded: %+v %v", evidence, err)
	}
	candidates, err := h.Store.Candidates(context.Background(), h.Config.Scope, r.ID, "all")
	if err != nil || len(candidates) != 0 {
		t.Fatalf("reference-only candidate persisted: %+v %v", candidates, err)
	}
}
