package memory

import (
	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
	"context"
	"testing"
)

func TestActorProvenanceReachesBothDecisionsAndDoesNotMergeMembers(t *testing.T) {
	prior := existing("alice-memory", "I prefer short answers.", "active")
	prior.SenderID = "alice"
	f := &fakeEvaluator{responses: []jev.Response{classification("preference", .99), relations(decisionAnswer("same", .99))}, inspect: func(call int, input any, q map[string]jev.Question) {
		s := input.(map[string]any)
		if call == 0 {
			if s["sender_id"] != "bob" || s["statement"] != "I prefer short answers." {
				t.Fatal("classification actor or exact statement lost", s)
			}
		} else {
			if s["candidate"].(map[string]any)["sender_id"] != "bob" || s["existing"].(map[string]any)["m0"].(map[string]any)["sender_id"] != "alice" {
				t.Fatal("relation authors lost", s)
			}
		}
	}}
	d, err := (&Processor{client: f}).Decide(WithActor(context.Background(), "bob"), "I prefer short answers.", []state.Memory{prior})
	if err != nil || !d.Store || len(d.DuplicateIDs) != 0 || len(d.ConflictIDs) != 0 {
		t.Fatalf("member assertions merged %+v %v", d, err)
	}
}

func TestDecisionRejectsMixedAudienceBeforeModelCall(t *testing.T) {
	a, b := existing("a", "First statement", "active"), existing("b", "Second statement", "active")
	a.AgentID, a.AudienceID = "dev", "a"
	b.AgentID, b.AudienceID = "dev", "b"
	f := &fakeEvaluator{}
	if _, err := (&Processor{client: f}).Decide(context.Background(), "Remember this preference", []state.Memory{a, b}); err == nil || f.calls != 0 {
		t.Fatal("mixed audience sent to classifier", err, f.calls)
	}
}
