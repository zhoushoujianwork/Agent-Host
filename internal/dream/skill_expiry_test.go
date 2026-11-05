package dream

import (
	"agent-host/internal/event"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
	"context"
	"strings"
	"testing"
	"time"
)

func TestAutomaticSkillDraftRejectsExpiredBeforeCatalogMutation(t *testing.T) {
	store, e := state.Open(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "channel-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	candidate := seedAutomaticSkillCandidate(t, store, p, "expired-procedure", SkillTarget{})
	root := canonicalDreamTemp(t)
	result, e := DraftSkillAutomatically(context.Background(), store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().Add(2*time.Hour))
	if e != nil || result.Status != "blocked" || !strings.Contains(result.Reason, "expired") {
		t.Fatal(result, e)
	}
	catalog, e := managedskills.Load(root)
	if e != nil || len(catalog.Records) != 0 {
		t.Fatal("expired proposal mutated catalog", e)
	}
}

func TestManualSkillPromotionRejectsSourceDriftWithoutAnotherDreamWake(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	candidate := seedAutomaticSkillCandidate(t, store, p, "manual-drift", SkillTarget{})
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	e := event.Event{RunID: "run-manual-drift", Type: "agent.result", Status: "completed", Data: map[string]any{"updated": true}}
	if err = partitioned.AppendEvent(context.Background(), &e); err != nil {
		t.Fatal(err)
	}
	if _, err = PromoteSkill(context.Background(), partitioned, candidate.ID, canonicalDreamTemp(t), "operator"); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal("stale manual promotion accepted", err)
	}
}
