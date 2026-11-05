package state

import (
	"context"
	"testing"
	"time"
)

func TestDreamProcedureCursorRotatesPastBlockedAndPreservesOtherCursors(t *testing.T) {
	store, e := Open(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	p := DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: OwnerAudienceID, Scope: "scope"}
	s := store.WithPartition(p.AgentID, p.AudienceID)
	ctx := context.Background()
	now := time.Now().UTC()
	r := DreamReceipt{ID: "r", IdempotencyKey: "key", Partition: p, Source: "source", SourceDigest: "digest", CreatedAt: now, Status: "extracted"}
	var candidates []DreamCandidate
	for _, id := range []string{"a-task", "b-blocked", "c-ready"} {
		kind := "procedure"
		if id == "a-task" {
			kind = "task"
		}
		candidates = append(candidates, DreamCandidate{ID: id, ReceiptID: r.ID, Partition: p, Kind: kind, Text: id, SourceRefs: []string{"artifact:result"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	}
	if _, e = s.SaveDreamExtraction(ctx, r, candidates); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordDreamSkillDraftBlocked(ctx, p, "b-blocked", "same-evidence", "missing managed skill", now); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveDreamScanCursor(ctx, DreamScanCursor{Partition: p, AfterRowID: 42, HandoffAfter: "handoff-test"}); e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"b-blocked", "c-ready", "b-blocked"} {
		page, e := s.PendingDreamProcedurePage(ctx, p, 1)
		if e != nil || len(page) != 1 || page[0].ID != want {
			t.Fatal("procedure starvation", want, page, e)
		}
	}
	cursor, e := s.DreamScanCursor(ctx, p)
	if e != nil || cursor.AfterRowID != 42 || cursor.HandoffAfter != "handoff-test" {
		t.Fatal("other cursors overwritten", cursor, e)
	}
}
