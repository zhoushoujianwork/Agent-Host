package state

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func externalSubmission() ExternalSubmission {
	now := time.Now().UTC()
	return ExternalSubmission{SchemaVersion: 1, Key: "decision-1", Source: ExternalSource{Entry: "codex", ObservedAt: now}, Items: []ExternalItem{{Kind: "statement", Text: "Use source-only distribution for the release.", EvidenceIDs: []string{"u1"}}}, Evidence: []ExternalEvidence{{ID: "u1", Role: "user", Author: "owner", Text: "Use source-only distribution for the release.", ObservedAt: now}}}
}

func admitExternal(t *testing.T, s *Store, id string) ExternalConnection {
	t.Helper()
	c := ExternalConnection{ID: id, AgentID: DefaultAgentID, AudienceID: OwnerAudienceID, Scope: "project:release", Enabled: true}
	if err := s.SaveExternalConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func externalPublication(t *testing.T, s *Store, r ExternalReceipt, id string) ExternalPublication {
	t.Helper()
	generation, err := s.MemoryFingerprint(t.Context(), []string{r.Connection.Scope})
	if err != nil {
		t.Fatal(err)
	}
	m := Memory{ID: id, AgentID: r.Connection.AgentID, AudienceID: r.Connection.AudienceID, Scope: r.Connection.Scope, Kind: "decision", Text: r.Submission.Items[0].Text, Status: "active", Revision: 1, CreatedAt: time.Now().UTC(), SourceType: "user_statement", ExternalReceiptID: r.ID, EvidenceIDs: []string{"u1"}}
	return ExternalPublication{Memory: &m, ExpectedGeneration: generation, Result: ExternalItemResult{Status: "published", Audit: json.RawMessage(`{"selected":true}`)}}
}

func TestExternalReceiptIdempotencyEvidenceAndRecall(t *testing.T) {
	s, home, _, cache := fileStore(t)
	c := admitExternal(t, s, "codex")
	v := externalSubmission()
	r, err := s.ReceiveExternal(t.Context(), c.ID, "receipt-1", v)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ReceiveExternal(t.Context(), c.ID, "unused-id", v)
	if err != nil || again.ID != r.ID {
		t.Fatal(again, err)
	}
	v.Source.Turn = "changed"
	if _, err := s.ReceiveExternal(t.Context(), c.ID, "receipt-2", v); err == nil {
		t.Fatal("changed payload reused key")
	}
	if err := s.FinishExternalItem(t.Context(), r.ID, 0, externalPublication(t, s, r, "decision-memory")); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExternalReceipt(t.Context(), c.ID, "", r.Submission.Key)
	if err != nil || got.ProcessingStatus != "completed" || got.Results[0].MemoryID != "decision-memory" {
		t.Fatal(got, err)
	}
	var runs int
	if err := s.db.QueryRow("SELECT count(*) FROM runs").Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("external intake manufactured %d runs: %v", runs, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	matches, err := fresh.Search(t.Context(), c.Scope, "source-only distribution", 10)
	if err != nil || len(matches) != 1 || matches[0].ExternalReceiptID != r.ID || matches[0].SourceRunID != "" {
		t.Fatal(matches, err)
	}
	evidence, err := fresh.ContextMemoryEvidence(t.Context(), c.Scope, matches[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(evidence)
	var observed map[string]any
	_ = json.Unmarshal(b, &observed)
	if observed["role"] != "external_agent_supplied_evidence" {
		t.Fatal(string(b))
	}
	if _, err := fresh.WithPartition("other", "owner").ContextMemoryEvidence(t.Context(), c.Scope, matches[0].ID, 1); err == nil {
		t.Fatal("cross-Agent evidence exposed")
	}
	if found, err := fresh.WithPartition("default", "group-a").Search(t.Context(), c.Scope, "distribution", 10); err != nil || len(found) != 0 {
		t.Fatal(found, err)
	}
	if _, err := fresh.ContextMemoryEvidence(t.Context(), "other-project", matches[0].ID, 1); err == nil {
		t.Fatal("cross-project evidence exposed")
	}
	if err := fresh.InvalidateMemory(t.Context(), matches[0].ID, "obsolete"); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.ContextMemoryEvidence(t.Context(), c.Scope, matches[0].ID, 1); err == nil {
		t.Fatal("disabled source still readable")
	}
	if _, err := fresh.RollbackFileMemory(t.Context()); err != nil {
		t.Fatal("rollback lost external memory", err)
	}
}

func TestExternalPublicationRecoversReceiptAfterSQLRollback(t *testing.T) {
	s, home, _, _ := fileStore(t)
	c := admitExternal(t, s, "codex")
	r, err := s.ReceiveExternal(t.Context(), c.ID, "receipt-crash", externalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	before := r
	if err := s.FinishExternalItem(t.Context(), r.ID, 0, externalPublication(t, s, r, "published-before-crash")); err != nil {
		t.Fatal(err)
	}
	// Simulate SQL rollback after the authoritative manifest was published.
	if err := os.Remove(s.files.ackPath(s.files.manifest.Generation)); err != nil {
		t.Fatal(err)
	}
	if err := s.sqlTransaction(t.Context(), func(tx *sql.Tx) error { return writeExternalReceipt(t.Context(), tx, before) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	got, err := fresh.ExternalReceipt(t.Context(), c.ID, r.ID, "")
	if err != nil || got.Results[0].MemoryID != "published-before-crash" || got.ProcessingStatus != "completed" {
		t.Fatal(got, err)
	}
	again, err := fresh.ReceiveExternal(t.Context(), c.ID, "another", r.Submission)
	if err != nil || again.ID != r.ID || again.Results[0].Status != "published" {
		t.Fatal(again, err)
	}
	if _, err := fresh.VerifyFileMemory(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalConnectionRevocationAndStalePublication(t *testing.T) {
	s, _, _, _ := fileStore(t)
	c := admitExternal(t, s, "codex")
	other := admitExternal(t, s, "other")
	r, err := s.ReceiveExternal(t.Context(), c.ID, "r1", externalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExternalReceipt(t.Context(), other.ID, r.ID, ""); err == nil {
		t.Fatal("foreign receipt exposed")
	}
	p := externalPublication(t, s, r, "m1")
	p.ExpectedGeneration = "stale"
	if err := s.FinishExternalItem(t.Context(), r.ID, 0, p); err == nil {
		t.Fatal("stale review published")
	}
	c.Enabled = false
	if err := s.SaveExternalConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExternalReceipt(t.Context(), c.ID, r.ID, ""); err == nil {
		t.Fatal("revoked receipt exposed")
	}
	if err := s.FinishExternalItem(t.Context(), r.ID, 0, externalPublication(t, s, r, "m1")); err == nil {
		t.Fatal("revoked review published")
	}
	c.Enabled = true
	if err := s.SaveExternalConnection(t.Context(), c); err == nil {
		t.Fatal("revoked identity restored implicitly")
	}
	legacy, err := Open(filepath.Join(t.TempDir(), "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := legacy.SaveExternalConnection(t.Context(), other); err == nil {
		t.Fatal("legacy backend admitted external writes")
	}
}

func TestExternalSubmissionRejectsInventedOrUnboundedEvidence(t *testing.T) {
	cases := map[string]func(*ExternalSubmission){
		"summary-as-user":     func(v *ExternalSubmission) { v.Items[0].Text = "Generated summary" },
		"assistant-as-user":   func(v *ExternalSubmission) { v.Evidence[0].Role = "assistant" },
		"missing-source":      func(v *ExternalSubmission) { v.Items[0].EvidenceIDs = []string{"missing"} },
		"oversize":            func(v *ExternalSubmission) { v.Evidence[0].Text = string(make([]byte, MaxExternalBytes+1)) },
		"unsupported-version": func(v *ExternalSubmission) { v.SchemaVersion = 2 },
		"missing-time":        func(v *ExternalSubmission) { v.Source.ObservedAt = time.Time{} },
		"summary-as-procedure": func(v *ExternalSubmission) {
			v.Items[0] = ExternalItem{Kind: "procedure", EvidenceIDs: []string{"u1"}, Procedure: &ExperienceDraft{Title: "Deploy", Summary: "Deployment succeeded", Conditions: "Always", Steps: []string{"Deploy"}, EvidenceIDs: []string{"u1"}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := externalSubmission()
			mutate(&v)
			if err := v.Validate(); err == nil {
				t.Fatal("invalid submission accepted")
			}
		})
	}
}

func TestExternalSchemaUpgradeAllowsReadOnlyMemoryStartupPreflight(t *testing.T) {
	s, home, _, _ := fileStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec("DROP TABLE external_memory_receipts; DROP TABLE external_memory_connections; PRAGMA user_version=14;"); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	if strict, err := Open(home, true); err == nil {
		strict.Close()
		t.Fatal("ordinary read bypassed schema upgrade")
	}
	preflight, err := OpenForMemoryInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preflight.VerifyFileMemory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := preflight.ExternalConnection(t.Context(), "absent"); err == nil {
		t.Fatal("preflight unexpectedly accessed absent external tables")
	}
	if err := preflight.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	admitExternal(t, upgraded, "after-upgrade")
}
