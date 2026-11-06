package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func TestDreamWorkInspectionRequiresExactPartition(t *testing.T) {
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	p := state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	now := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	item := state.DreamWorkItem{ID: "work-one", Partition: p, SourceKey: "host-run:one", Source: "host-run:one", RunID: "one", SessionID: "session-one", EvidenceDigest: "current", PriorEvidenceDigest: "prior", PolicyVersion: state.DreamWorkPolicyVersion, SkillEvidence: []state.DreamSkillEvidence{{Kind: "loaded", SkillID: "skill", Revision: 2, Digest: "digest", Status: "completed"}}}
	item, _, err = partitioned.PrepareDreamWork(context.Background(), item, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = partitioned.FinishDreamWork(context.Background(), item, "processed", "", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"work-show", "--home", home, "--person", p.PersonID, "--agent", p.AgentID, "--audience", p.AudienceID, "--memory-scope", p.Scope, "--id", item.ID, "--format", "json"}
	var out bytes.Buffer
	if err = runDreamLifecycle(args, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"prior_evidence_digest": "prior"`, `"session_id": "session-one"`, `"kind": "loaded"`} {
		if !strings.Contains(out.String(), marker) {
			t.Fatalf("work inspection omitted %s: %s", marker, out.String())
		}
	}
	args[4] = "other"
	if err = runDreamLifecycle(args, &out, &out); err == nil {
		t.Fatal("cross-person work inspection succeeded")
	}
}
