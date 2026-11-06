package dream

import (
	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/state"
	"context"
	"errors"
	"testing"
	"time"
)

func TestIncrementalReceiptsIsolateSameSourceAcrossPeople(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	extractor := &fakeExtractor{now: now}
	l := Lifecycle{Store: store.WithPartition("agent", state.OwnerAudienceID), Extractor: extractor}
	ids := map[string]bool{}
	for _, person := range []string{"alice", "bob"} {
		p := state.DreamPartition{PersonID: person, AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
		source := canonicalSource{Work: state.DreamWorkItem{ID: "work-" + person, Partition: p, SourceKey: "handoff:same", Source: "same", EvidenceDigest: "same-digest", PolicyVersion: state.DreamWorkPolicyVersion}, Input: ExtractionInput{Partition: p}, AllowedRefs: map[string]bool{"artifact:result.json": true}}
		// Empty extraction tests receipt identity without model-specific candidate text.
		l.Extractor = emptyIncrementalExtractor{}
		attempts := 0
		receipt, _, err := l.processSource(context.Background(), source, now, &attempts)
		if err != nil {
			t.Fatal(err)
		}
		if ids[receipt.ID] {
			t.Fatal("receipt reused across people")
		}
		ids[receipt.ID] = true
	}
}

type emptyIncrementalExtractor struct{}

func (emptyIncrementalExtractor) Extract(context.Context, ExtractionInput) ([]Draft, error) {
	return nil, nil
}

func TestCanonicalEvidenceRecoveryChangesDigest(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := seedCanonicalRun(t, store, "completed")
	record, err := store.WithPartition(p.AgentID, p.AudienceID).DreamCanonicalRunByID(context.Background(), p, "run-one")
	if err != nil {
		t.Fatal(err)
	}
	missing, err := canonicalRunSource(record, p, nil, errors.New("temporary evidence read failure"))
	if err != nil {
		t.Fatal(err)
	}
	complete, err := canonicalRunSource(record, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Work.EvidenceDigest == complete.Work.EvidenceDigest {
		t.Fatal("recovered evidence does not invalidate previous missing-evidence checkpoint")
	}
	record.Session.NativeSessionID = "later-native-session"
	later, err := canonicalRunSource(record, p, nil, nil)
	if err != nil || later.Work.EvidenceDigest != complete.Work.EvidenceDigest {
		t.Fatal("later session state changed historical run evidence", err)
	}
}

func TestExpiredCandidateDoesNotRegenerateFromUnchangedRun(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := seedCanonicalRun(t, store, "completed")
	now := time.Now().UTC()
	extractor := &canonicalExtractor{now: now}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	lifecycle := Lifecycle{Store: partitioned, Extractor: extractor, Now: func() time.Time { return now }, Batch: 1}
	source := privateRoot(t)
	destination := Destination{PersonID: p.PersonID, Agent: p.AgentID, Audience: p.AudienceID, MemoryScope: p.Scope}
	if _, err = lifecycle.Process(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if n, e := partitioned.RetireExpiredDreamCandidates(context.Background(), p, now, 10); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, err = lifecycle.Process(context.Background(), source, destination); err != nil || extractor.calls != 1 {
		t.Fatal("retired candidate was regenerated", extractor.calls, err)
	}
}

func TestCanonicalReferenceRetainsSkillAndFileDigestsSeparately(t *testing.T) {
	p := state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	manifest := harness.SkillManifest{Sources: []harness.SkillSource{{SkillID: "skill", Name: "ops", Revision: 2, ContentDigest: "catalog-digest"}}}
	record := state.DreamCanonicalRun{Run: state.Run{ID: "run", ExecutionStatus: "completed", AcceptanceStatus: "accepted", Events: []event.Event{
		{Seq: 1, Type: "skills.catalog.provided", Status: "completed", Data: map[string]any{"manifest": manifest}},
		{Seq: 2, Type: "skills.reference.observed", Status: "completed", Data: map[string]any{"file": harness.SkillFile{Path: "skills/ops/references/runbook.md", SHA256: "file-digest"}}},
	}}}
	source, err := canonicalRunSource(record, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range source.Work.SkillEvidence {
		if e.Kind == "reference_read" {
			if e.SkillID != "skill" || e.Revision != 2 || e.Digest != "catalog-digest" || e.ReferenceDigest != "file-digest" {
				t.Fatalf("reference identity lost: %+v", e)
			}
			return
		}
	}
	t.Fatal("reference evidence missing")
}
