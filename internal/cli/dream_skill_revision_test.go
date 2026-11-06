package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func TestDreamPromoteAcceptsExplicitTargetFlagsButRequiresCanonicalEvidence(t *testing.T) {
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	p := state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	now := time.Now().UTC()
	receipt := state.DreamReceipt{ID: "receipt", IdempotencyKey: "receipt-key", Source: "/private/handoff-one", SourceDigest: "digest", Partition: p, Status: "extracted", CreatedAt: now}
	candidate := state.DreamCandidate{ID: "dream-candidate-one", ReceiptID: receipt.ID, Partition: p, Kind: "procedure", Text: "Correction", SourceRefs: []string{"artifact:stdout.txt"}, ExpiresAt: now.Add(time.Hour)}
	if _, err = partitioned.SaveDreamExtraction(context.Background(), receipt, []state.DreamCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runDreamLifecycle([]string{"promote", "--home", home, "--id", candidate.ID, "--skills-root", home + "/skills", "--actor", "operator", "--person", p.PersonID, "--agent", p.AgentID, "--audience", p.AudienceID, "--memory-scope", p.Scope, "--target-skill-id", "managed", "--target-skill-revision", "2", "--target-skill-digest", "exact", "--format", "json"}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "canonical Host load evidence") {
		t.Fatalf("explicit target flags were ignored or admitted external evidence: %v", err)
	}
	store, err = state.Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.WithPartition(p.AgentID, p.AudienceID).DreamCandidate(context.Background(), candidate.ID)
	if err != nil || got.Status != "pending" {
		t.Fatalf("failed target promotion changed candidate: %+v %v", got, err)
	}
}
