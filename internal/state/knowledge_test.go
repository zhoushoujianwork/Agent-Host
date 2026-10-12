package state

import "testing"

func TestKnowledgeReadCannotSupportExecutionExperience(t *testing.T) {
	evidence := []ToolEvidence{{ID: "page", Name: "mcp__knowledge_docs__read_confluence_page"}, {ID: "test", Name: "Bash"}}
	eligible := ExecutionExperienceEvidence(evidence)
	if len(eligible) != 1 || eligible[0].ID != "test" || len(evidence) != 2 {
		t.Fatal("source evidence lost or reference promoted")
	}
	draft := ExperienceDraft{Title: "Repair", Summary: "Observed test", Conditions: "Fixture", Steps: []string{"Run the check"}, EvidenceIDs: []string{"page"}}
	if err := validateDraft(draft, evidence); err == nil {
		t.Fatal("document read accepted as execution experience")
	}
	draft.EvidenceIDs = []string{"test"}
	if err := validateDraft(draft, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeDiscoveryAndReadsCannotSupportExecutionExperience(t *testing.T) {
	evidence := []ToolEvidence{
		{ID: "catalog", Name: "ToolSearch"},
		{ID: "search", Name: "mcp__knowledge_docs__search_confluence"},
		{ID: "page", Name: "mcp__knowledge_docs__read_confluence_page"},
	}
	if eligible := ExecutionExperienceEvidence(evidence); len(eligible) != 0 {
		t.Fatalf("knowledge-only discovery became execution evidence: %+v", eligible)
	}
	draft := ExperienceDraft{Title: "Find a procedure", Summary: "Read documentation", Conditions: "Catalog is available", Steps: []string{"Find and read the document"}}
	for _, id := range []string{"catalog", "search", "page"} {
		draft.EvidenceIDs = []string{id}
		if err := validateDraft(draft, evidence); err == nil {
			t.Fatalf("catalog/reference %s accepted for candidate publication", id)
		}
	}
	evidence = append(evidence, ToolEvidence{ID: "execution", Name: "Bash"})
	if eligible := ExecutionExperienceEvidence(evidence); len(eligible) != 1 || eligible[0].ID != "execution" {
		t.Fatalf("mixed execution evidence changed: %+v", eligible)
	}
	draft.EvidenceIDs = []string{"execution"}
	if err := validateDraft(draft, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestConversationHistoryCannotManufactureNewExecutionExperience(t *testing.T) {
	evidence := []ToolEvidence{
		{ID: "search", Name: "mcp__conversation_history__search_conversation_history"},
		{ID: "read", Name: "mcp__conversation_history__read_conversation_history"},
		{ID: "execution", Name: "Bash"},
	}
	eligible := ExecutionExperienceEvidence(evidence)
	if len(eligible) != 1 || eligible[0].ID != "execution" || len(evidence) != 3 {
		t.Fatal("history retrieval became observed execution evidence", eligible)
	}
	draft := ExperienceDraft{Title: "Earlier deployment", Summary: "Read a reported historical success", Conditions: "Retained chat exists", Steps: []string{"Search and read earlier messages"}}
	for _, id := range []string{"search", "read"} {
		draft.EvidenceIDs = []string{id}
		if err := validateDraft(draft, evidence); err == nil {
			t.Fatal("rereading history authorized recursive experience publication", id)
		}
	}
	draft.EvidenceIDs = []string{"execution"}
	if err := validateDraft(draft, evidence); err != nil {
		t.Fatal("new execution was excluded alongside history", err)
	}
}
