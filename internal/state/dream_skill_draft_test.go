package state

import (
	"context"
	"testing"
	"time"
)

func TestPendingDreamProceduresFiltersBeforeLimitAndPersistsBlockedKey(t *testing.T) {
	store, err := Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := DreamPartition{PersonID: "owner", AgentID: "agent", AudienceID: OwnerAudienceID, Scope: "scope"}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	now := time.Now().UTC()
	receipt := DreamReceipt{ID: "receipt", IdempotencyKey: "key", Source: "host-run:run", SourceDigest: "digest", Partition: p, Status: "extracted", CreatedAt: now}
	candidates := []DreamCandidate{
		{ID: "a-task", ReceiptID: receipt.ID, Partition: p, Kind: "task", Text: "Task", SourceRefs: []string{"artifact:a"}, ExpiresAt: now.Add(time.Hour)},
		{ID: "b-task", ReceiptID: receipt.ID, Partition: p, Kind: "knowledge", Text: "Knowledge", SourceRefs: []string{"artifact:b"}, ExpiresAt: now.Add(time.Hour)},
		{ID: "z-procedure", ReceiptID: receipt.ID, Partition: p, Kind: "procedure", Text: "Procedure", SourceRefs: []string{"artifact:z"}, ExpiresAt: now.Add(time.Hour)},
	}
	if _, err = partitioned.SaveDreamExtraction(context.Background(), receipt, candidates); err != nil {
		t.Fatal(err)
	}
	procedures, err := partitioned.PendingDreamProcedurePage(context.Background(), p, 1)
	if err != nil || len(procedures) != 1 || procedures[0].ID != "z-procedure" {
		t.Fatalf("procedure was starved before LIMIT: %+v %v", procedures, err)
	}
	blocked, err := partitioned.RecordDreamSkillDraftBlocked(context.Background(), p, "z-procedure", "evidence:catalog", "target is stale", now)
	if err != nil || blocked.SkillDraftStatus != "blocked" || blocked.SkillDraftAttempts != 1 {
		t.Fatalf("blocked result not persisted: %+v %v", blocked, err)
	}
	blocked, err = partitioned.RecordDreamSkillDraftBlocked(context.Background(), p, "z-procedure", "evidence:catalog", "target is stale", now.Add(time.Minute))
	if err != nil || blocked.SkillDraftAttempts != 1 {
		t.Fatalf("identical blocked result retried: %+v %v", blocked, err)
	}
}
