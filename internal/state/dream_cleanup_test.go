package state

import (
	"context"
	"testing"
	"time"
)

func TestRetireExpiredDreamCandidatesPreservesEvidenceAndPartition(t *testing.T) {
	s, e := Open(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: OwnerAudienceID, Scope: "scope"}
	s = s.WithPartition(p.AgentID, p.AudienceID)
	now := time.Now().UTC()
	ctx := context.Background()
	receipt := DreamReceipt{ID: "receipt", IdempotencyKey: "key", Partition: p, Source: "source", SourceDigest: "digest", CreatedAt: now.Add(-2 * time.Hour), Status: "extracted"}
	candidates := []DreamCandidate{{ID: "expired", ReceiptID: receipt.ID, Partition: p, Kind: "task", Text: "old task", SourceRefs: []string{"artifact:result"}, CreatedAt: receipt.CreatedAt, ExpiresAt: now.Add(-time.Hour)}, {ID: "fresh", ReceiptID: receipt.ID, Partition: p, Kind: "task", Text: "new task", SourceRefs: []string{"artifact:result"}, CreatedAt: receipt.CreatedAt, ExpiresAt: now.Add(time.Hour)}}
	if _, e = s.SaveDreamExtraction(ctx, receipt, candidates); e != nil {
		t.Fatal(e)
	}
	wrong := p
	wrong.PersonID = "other"
	if count, e := s.RetireExpiredDreamCandidates(ctx, wrong, now, 10); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if count, e := s.RetireExpiredDreamCandidates(ctx, p, now, 10); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if count, e := s.RetireExpiredDreamCandidates(ctx, p, now, 10); e != nil || count != 0 {
		t.Fatal("repeated retirement", count, e)
	}
	retired, e := s.DreamCandidate(ctx, "expired")
	if e != nil || retired.Status != "expired" {
		t.Fatal(retired, e)
	}
	fresh, e := s.DreamCandidate(ctx, "fresh")
	if e != nil || fresh.Status != "pending" {
		t.Fatal(fresh, e)
	}
	if _, e = s.DreamReceipt(ctx, receipt.ID); e != nil {
		t.Fatal("retirement removed evidence receipt", e)
	}
}
