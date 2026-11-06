package state

import (
	"context"
	"testing"
	"time"
)

func TestDreamWorkRestartDedupChangeBackoffAndPartitionIsolation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	home := t.TempDir()
	store, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	p := DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: OwnerAudienceID, Scope: "scope"}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	base := DreamWorkItem{ID: "work", Partition: p, SourceKey: "host-run:one", Source: "host-run:one", RunID: "one", SessionID: "session", EvidenceDigest: "digest-one", PolicyVersion: DreamWorkPolicyVersion}
	item, ready, err := partitioned.PrepareDreamWork(ctx, base, now)
	if err != nil || !ready {
		t.Fatalf("first observation: %+v %v %v", item, ready, err)
	}
	item, err = partitioned.FinishDreamWork(ctx, item, "processed", "", []string{"receipt"}, []string{"candidate"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	partitioned = store.WithPartition(p.AgentID, p.AudienceID)
	if got, again, e := partitioned.PrepareDreamWork(ctx, base, now.Add(time.Minute)); e != nil || again || got.State != "processed" {
		t.Fatalf("restart repeated work: %+v %v %v", got, again, e)
	}
	changed := base
	changed.EvidenceDigest = "digest-two"
	item, ready, err = partitioned.PrepareDreamWork(ctx, changed, now.Add(2*time.Minute))
	if err != nil || !ready || item.PriorEvidenceDigest != "digest-one" {
		t.Fatalf("changed evidence not linked: %+v %v %v", item, ready, err)
	}
	item, err = partitioned.FinishDreamWork(ctx, item, "retryable", "fixture failure", nil, nil, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, again, e := partitioned.PrepareDreamWork(ctx, changed, now.Add(2*time.Minute+30*time.Second)); e != nil || again {
		t.Fatalf("backoff ignored: %v %v", again, e)
	}
	if _, again, e := partitioned.PrepareDreamWork(ctx, changed, now.Add(4*time.Minute)); e != nil || !again {
		t.Fatalf("retry did not reopen: %v %v", again, e)
	}
	wrong := p
	wrong.PersonID = "other"
	if items, e := partitioned.DreamWorkItems(ctx, wrong, "all", 10); e != nil || len(items) != 0 {
		t.Fatalf("cross-person work leaked: %+v %v", items, e)
	}
	session := Session{AgentID: p.AgentID, AudienceID: p.AudienceID, ID: "session", Scope: p.Scope, NativeSessionID: "native"}
	if err = partitioned.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	run := Run{AgentID: p.AgentID, AudienceID: p.AudienceID, ID: "run", SessionID: session.ID, Scope: p.Scope, Input: "task", NativeSessionID: session.NativeSessionID}
	if err = partitioned.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err = partitioned.DreamCanonicalRunByID(ctx, wrong, run.ID); err == nil {
		t.Fatal("source-less owner run was attributed to another person")
	}
}

func TestFinishDreamWorkRejectsStaleEvidence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	store, err := Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: OwnerAudienceID, Scope: "scope"}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	oldObservation := DreamWorkItem{ID: "work", Partition: p, SourceKey: "host-run:one", Source: "host-run:one", EvidenceDigest: "old", PolicyVersion: DreamWorkPolicyVersion}
	oldWork, ready, err := partitioned.PrepareDreamWork(ctx, oldObservation, now)
	if err != nil || !ready {
		t.Fatalf("prepare old evidence: ready=%v err=%v", ready, err)
	}
	newObservation := oldObservation
	newObservation.EvidenceDigest = "new"
	newWork, ready, err := partitioned.PrepareDreamWork(ctx, newObservation, now.Add(time.Minute))
	if err != nil || !ready {
		t.Fatalf("prepare new evidence: ready=%v err=%v", ready, err)
	}
	if _, err = partitioned.FinishDreamWork(ctx, oldWork, "processed", "", []string{"stale-receipt"}, nil, now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale evidence overwrote newer observation")
	}
	got, err := partitioned.DreamWorkItem(ctx, p, newWork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EvidenceDigest != "new" || got.State != "pending" || len(got.ReceiptIDs) != 0 {
		t.Fatalf("new evidence changed by stale finish: %+v", got)
	}
	oldPolicy := DreamWorkItem{ID: "work-policy", Partition: p, SourceKey: "host-run:policy", Source: "host-run:policy", EvidenceDigest: "same", PolicyVersion: "old-policy"}
	oldPolicyWork, ready, err := partitioned.PrepareDreamWork(ctx, oldPolicy, now)
	if err != nil || !ready {
		t.Fatalf("prepare old policy: ready=%v err=%v", ready, err)
	}
	newPolicy := oldPolicy
	newPolicy.PolicyVersion = "new-policy"
	newPolicyWork, ready, err := partitioned.PrepareDreamWork(ctx, newPolicy, now.Add(time.Minute))
	if err != nil || !ready {
		t.Fatalf("prepare new policy: ready=%v err=%v", ready, err)
	}
	if _, err = partitioned.FinishDreamWork(ctx, oldPolicyWork, "processed", "", []string{"stale-policy-receipt"}, nil, now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale policy overwrote newer observation")
	}
	got, err = partitioned.DreamWorkItem(ctx, p, newPolicyWork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PolicyVersion != "new-policy" || got.State != "pending" || len(got.ReceiptIDs) != 0 {
		t.Fatalf("new policy changed by stale finish: %+v", got)
	}
}
