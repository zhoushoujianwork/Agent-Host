package state

import "testing"

func TestEmailEvidenceRemainsPrivateSourceMaterial(t *testing.T) {
	evidence := []ToolEvidence{{ID: "mail", Name: "mcp__email__read_message"}, {ID: "send", Name: "mcp__email__send_message"}, {ID: "exec", Name: "Bash"}}
	out := ExecutionExperienceEvidence(evidence)
	if len(out) != 1 || out[0].ID != "exec" || len(evidence) != 3 {
		t.Fatal("mail entered reusable execution evidence", out)
	}
}
