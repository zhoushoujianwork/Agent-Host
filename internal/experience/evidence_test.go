package experience

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func TestLearningEvidenceRedactsCredentialsWithoutChangingSourceOrNumbers(t *testing.T) {
	source := []state.ToolEvidence{{ID: "one", Input: json.RawMessage(`{"command":"tccli cbs DescribeDisks --secret-key cli-secret"}`), Result: json.RawMessage(`{"secretId":"id-secret","nested":"{\"secretKey\":\"nested-secret\"}","text":"secretKey = text-secret\nAuthorization: Bearer bearer-secret","NextToken":17881710190357204}`)}}
	original := string(source[0].Result)
	got := LearningEvidence(source)
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"cli-secret", "id-secret", "nested-secret", "text-secret", "bearer-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("credential leaked: %s", secret)
		}
	}
	if string(source[0].Result) != original || !strings.Contains(string(got[0].Result), "17881710190357204") || got[0].ID != "one" {
		t.Fatal("source or evidence identity/precision changed")
	}
}

func TestRefinementUsesOnlyCitedEvidenceAndRejectsExpandedReferences(t *testing.T) {
	evidence := fixtureEvidence()
	other := evidence[0]
	other.ID = "unrelated"
	other.Result = json.RawMessage(`"unrelated-result"`)
	evidence = append(evidence, other)
	x := NewExtractor(runnerFunc(func(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
		if !req.ToolsDisabled || strings.Contains(req.Prompt, "unrelated-result") || strings.Contains(req.Prompt, fixtureRun().Output) || !strings.Contains(req.Prompt, "ONE smaller lesson") {
			t.Fatal("refinement expanded evidence or context")
		}
		draft := fixtureDraft()
		draft.EvidenceIDs = append(draft.EvidenceIDs, "unrelated")
		return successfulResult(draftJSON(t, []state.ExperienceDraft{draft})), nil
	}), "fixture-model")
	if _, err := x.Refine(t.Context(), fixtureRun(), fixtureDraft(), evidence); err == nil {
		t.Fatal("refinement cited previously uncited evidence")
	}
}
