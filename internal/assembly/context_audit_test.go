package assembly

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-host/internal/state"
)

func TestProcedureContextKeepsAvoidanceWithoutRevisionAudit(t *testing.T) {
	memories := []state.Memory{{ID: "procedure", Text: "Use the verified destination", Steps: []string{"Resolve the destination", "Send and inspect status"}, Avoid: []string{"Do not replay the failed destination"}, RevisionDecision: json.RawMessage(`{"old_incident":"PRIVATE-AUDIT-ONLY"}`)}}
	prompt := Prompt("Explain the procedure", memories, nil)
	if strings.Contains(prompt, "PRIVATE-AUDIT-ONLY") || strings.Contains(prompt, "revision_decision") || !strings.Contains(prompt, "Do not replay the failed destination") {
		t.Fatal("procedure context omitted avoidance or included the audit")
	}
	if len(memories[0].RevisionDecision) == 0 {
		t.Fatal("context assembly mutated the saved record")
	}
}
