package state

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
)

func toolEvent(id, name, stage string, seq int64) event.Event {
	o := harness.ToolObservation{ID: id, Name: name, Stage: stage}
	if stage == "call" {
		o.Input = json.RawMessage(`{"file_path":"parser.go"}`)
	} else {
		o.Result = json.RawMessage(`{"output":"parser test passed"}`)
	}
	return event.Event{Type: "native.tool", Phase: "execute", Component: "harness.claude", Seq: seq, Data: map[string]any{"tool": o}}
}

func recordTools(t *testing.T, s *Store, runID string) {
	t.Helper()
	for _, e := range []event.Event{toolEvent("edit", "Edit", "call", 1), toolEvent("edit", "Edit", "result", 2), toolEvent("test", "Bash", "call", 3), toolEvent("test", "Bash", "result", 4)} {
		e.RunID = runID
		if err := s.AppendEvent(testContext, &e); err != nil {
			t.Fatal(err)
		}
	}
}

func experienceCandidate(t *testing.T, s *Store, id, scope string) Candidate {
	t.Helper()
	run := addRun(t, s, "run-"+id, scope, "Fix the parser and run the tests", true)
	recordTools(t, s, run.ID)
	return Candidate{ID: id, RunID: run.ID, Scope: scope, Model: "test-extractor", Assessment: json.RawMessage(`{"choice":"supported"}`), Draft: ExperienceDraft{Title: "Parser boundary repair", Summary: "The parser test passed after the boundary change.", Conditions: "For this parser implementation and its existing boundary test.", Steps: []string{"Check the expected boundary behavior.", "Apply the boundary fix and run the parser tests."}, EvidenceIDs: []string{"edit", "test"}}}
}

func saveCandidate(t *testing.T, s *Store, c Candidate) {
	t.Helper()
	if err := s.SaveCandidates(testContext, c.RunID, []Candidate{c}, json.RawMessage(`{"extractor":"test"}`)); err != nil {
		t.Fatal(err)
	}
}

func acceptCandidate(t *testing.T, s *Store, id, memoryID string, conflicts, duplicates []string) Memory {
	t.Helper()
	c, err := s.GetCandidate(testContext, id)
	if err != nil {
		t.Fatal(err)
	}
	memories, err := s.Memories(testContext, c.Scope)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]int{}
	for _, m := range memories {
		snapshot[m.ID] = m.Revision
	}
	m, err := s.AcceptCandidate(testContext, id, CandidateAcceptance{MemoryID: memoryID, ExpectedRevisions: snapshot, ConflictIDs: conflicts, DuplicateIDs: duplicates, Decision: decision, Note: "Reviewed tool evidence"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEvidenceUsesMatchedNativeToolsOnly(t *testing.T) {
	events := []event.Event{toolEvent("edit", "Edit", "call", 1), toolEvent("edit", "Edit", "result", 2)}
	ignored := toolEvent("extract", "Read", "call", 3)
	ignored.Phase = "extract"
	events = append(events, ignored)
	out, err := EvidenceFromEvents(events)
	if err != nil || len(out) != 1 || out[0].ID != "edit" || out[0].CallSeq != 1 || out[0].ResultSeq != 2 || out[0].IsError {
		t.Fatalf("evidence %+v %v", out, err)
	}
	failed := toolEvent("edit", "Edit", "result", 2)
	o := failed.Data["tool"].(harness.ToolObservation)
	o.IsError = true
	failed.Data["tool"] = o
	out, err = EvidenceFromEvents([]event.Event{events[0], failed})
	if err != nil || !out[0].IsError {
		t.Fatalf("error evidence invented success %+v %v", out, err)
	}
	missingContent := toolEvent("edit", "Edit", "result", 2)
	o = missingContent.Data["tool"].(harness.ToolObservation)
	o.Result = nil
	missingContent.Data["tool"] = o
	out, err = EvidenceFromEvents([]event.Event{events[0], missingContent})
	if err != nil || len(out) != 1 || len(out[0].Result) != 0 {
		t.Fatalf("absent result content must remain absent %+v %v", out, err)
	}
	for name, events := range map[string][]event.Event{
		"missing result":          {toolEvent("a", "Edit", "call", 1)},
		"missing call":            {toolEvent("a", "Edit", "result", 2)},
		"wrong name":              {toolEvent("a", "Edit", "call", 1), toolEvent("a", "Bash", "result", 2)},
		"duplicate call":          {toolEvent("a", "Edit", "call", 1), toolEvent("a", "Edit", "call", 2)},
		"duplicate result":        {toolEvent("a", "Edit", "call", 1), toolEvent("a", "Edit", "result", 2), toolEvent("a", "Edit", "result", 3)},
		"reversed sequence":       {toolEvent("a", "Edit", "call", 2), toolEvent("a", "Edit", "result", 1)},
		"unobserved error status": {toolEvent("a", "Edit", "call", 1), {Phase: "execute", Seq: 2, Data: map[string]any{"tool": map[string]any{"id": "a", "stage": "result", "result": "ok"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EvidenceFromEvents(events); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestEvidenceEntitiesExcludeTextAndExternalPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	draft := ExperienceDraft{Title: "title", Summary: "mentions invented.go", Conditions: "conditions", Steps: []string{"Fix secret.go"}, EvidenceIDs: []string{"inside", "external", "shell", "symlink"}}
	evidence := []ToolEvidence{{ID: "inside", Input: json.RawMessage(`{"file_path":"src/parser.go"}`)}, {ID: "external", Input: json.RawMessage(`{"path":"../elsewhere.go"}`)}, {ID: "shell", Input: json.RawMessage(`{"command":"cat secret.go"}`)}, {ID: "symlink", Input: json.RawMessage(`{"filePath":"outside/secret.go"}`)}}
	entities, err := EvidenceEntities(root, draft, evidence)
	if err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"file:" + filepath.Join(realRoot, "src/parser.go"), "project:" + realRoot}
	if !reflect.DeepEqual(entities, want) {
		t.Fatalf("entities %v want %v", entities, want)
	}
}

func TestCandidatesRequireEvidenceScopeAndAtomicSave(t *testing.T) {
	s, _ := testStore(t)
	scope := t.TempDir()
	c := experienceCandidate(t, s, "c", scope)
	for name, mutate := range map[string]func(*Candidate){
		"wrong scope":         func(c *Candidate) { c.Scope = "other" },
		"wrong run":           func(c *Candidate) { c.RunID = "missing" },
		"unknown reference":   func(c *Candidate) { c.Draft.EvidenceIDs = []string{"invented"} },
		"duplicate reference": func(c *Candidate) { c.Draft.EvidenceIDs = []string{"edit", "edit"} },
		"missing conditions":  func(c *Candidate) { c.Draft.Conditions = "" },
		"already reviewed":    func(c *Candidate) { c.Status = "accepted" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			bad.ID = "bad"
			mutate(&bad)
			if err := s.SaveCandidates(testContext, c.RunID, []Candidate{c, bad}, decision); err == nil {
				t.Fatal("bad candidate accepted")
			}
			all, err := s.Candidates(testContext, scope, "", "all")
			if err != nil || len(all) != 0 {
				t.Fatalf("partial commit %+v %v", all, err)
			}
		})
	}
	if err := s.ExperienceFailure(testContext, c.RunID, "extraction unavailable"); err != nil {
		t.Fatal(err)
	}
	saveCandidate(t, s, c)
	stored, err := s.GetCandidate(testContext, c.ID)
	if err != nil || stored.Status != "pending" || len(stored.Entities) != 2 || stored.CreatedAt.IsZero() {
		t.Fatalf("candidate %+v %v", stored, err)
	}
	evidence, err := s.CandidateEvidence(testContext, c.ID)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("evidence %+v %v", evidence, err)
	}
	run, err := s.Run(testContext, c.RunID)
	if err != nil || run.ExperienceStatus != "review_pending" || run.ExperienceError != "" || run.AcceptanceStatus != "unverified" {
		t.Fatalf("run %+v %v", run, err)
	}
	if err := s.SaveCandidates(testContext, c.RunID, []Candidate{c}, decision); err == nil {
		t.Fatal("candidate replay accepted")
	}
	if err := s.ExperienceFailure(testContext, c.RunID, "renderer failed"); err == nil {
		t.Fatal("completed extraction overwritten by display failure")
	}
}

func TestReviewControlsRecallAndRetainsProvenance(t *testing.T) {
	s, _ := testStore(t)
	scope := t.TempDir()
	c := experienceCandidate(t, s, "c", scope)
	saveCandidate(t, s, c)
	found, err := s.Search(testContext, scope, "parser", 10)
	if err != nil || len(found) != 0 {
		t.Fatalf("unreviewed recalled %+v %v", found, err)
	}
	m := acceptCandidate(t, s, c.ID, "m", nil, nil)
	if m.SourceType != "reviewed_experience" || m.CandidateID != c.ID || len(m.EvidenceIDs) != 2 || m.Conditions != c.Draft.Conditions || m.Kind != "procedure" {
		t.Fatalf("provenance %+v", m)
	}
	found, err = s.Search(testContext, scope, "parser", 10)
	if err != nil || len(found) != 1 || found[0].ID != m.ID {
		t.Fatalf("accepted recall %+v %v", found, err)
	}
	run, err := s.Run(testContext, c.RunID)
	if err != nil || run.ExperienceStatus != "reviewed" || run.AcceptanceStatus != "unverified" {
		t.Fatalf("review falsely accepted business outcome %+v %v", run, err)
	}
	if _, err := s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "second", Decision: decision}); err == nil {
		t.Fatal("review replay accepted")
	}
	if err := s.RejectCandidate(testContext, c.ID, "change review"); err == nil {
		t.Fatal("accepted candidate overwritten")
	}
	r := experienceCandidate(t, s, "reject", scope)
	saveCandidate(t, s, r)
	if err := s.RejectCandidate(testContext, r.ID, "unsupported generalization"); err != nil {
		t.Fatal(err)
	}
	rc, err := s.GetCandidate(testContext, r.ID)
	if err != nil || rc.Status != "rejected" || rc.ReviewNote != "unsupported generalization" || rc.MemoryID != "" {
		t.Fatalf("rejected %+v %v", rc, err)
	}
}

func TestExperienceDuplicatesConflictsAndStaleComparisons(t *testing.T) {
	s, _ := testStore(t)
	scope := t.TempDir()
	c := experienceCandidate(t, s, "first", scope)
	saveCandidate(t, s, c)
	m := acceptCandidate(t, s, c.ID, "m", nil, nil)
	d := experienceCandidate(t, s, "duplicate", scope)
	saveCandidate(t, s, d)
	dm := acceptCandidate(t, s, d.ID, "unused", nil, []string{m.ID})
	if dm.ID != m.ID || dm.Revision != 1 || dm.SourceRunID != c.RunID {
		t.Fatalf("duplicate changed canonical source %+v", dm)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memory_evidence WHERE memory_id=?", m.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate evidence %d %v", count, err)
	}
	conflict := experienceCandidate(t, s, "conflict", scope)
	conflict.Draft.Summary = "The opposite boundary policy is needed."
	saveCandidate(t, s, conflict)
	if _, err := s.AcceptCandidate(testContext, conflict.ID, CandidateAcceptance{MemoryID: "conflicting", ExpectedRevisions: map[string]int{}, ConflictIDs: []string{m.ID}, Decision: decision}); err == nil {
		t.Fatal("stale comparison accepted")
	}
	other := acceptCandidate(t, s, conflict.ID, "conflicting", []string{m.ID}, nil)
	old, err := s.Memory(testContext, m.ID)
	if err != nil || old.Status != "conflicted" || old.Revision != 2 || other.Status != "conflicted" {
		t.Fatalf("conflicts old=%+v new=%+v %v", old, other, err)
	}
	found, err := s.Search(testContext, scope, "parser", 10)
	if err != nil || len(found) != 0 {
		t.Fatalf("conflict recalled %+v %v", found, err)
	}
	otherScope := experienceCandidate(t, s, "outside", t.TempDir())
	saveCandidate(t, s, otherScope)
	if _, err := s.AcceptCandidate(testContext, otherScope.ID, CandidateAcceptance{MemoryID: "wrong", ExpectedRevisions: map[string]int{}, ConflictIDs: []string{m.ID}, Decision: decision}); err == nil {
		t.Fatal("cross-scope conflict accepted")
	}
}

func TestExperienceCannotDowngradeStatementProvenance(t *testing.T) {
	s, _ := testStore(t)
	scope := t.TempDir()
	r := addRun(t, s, "statement", scope, "Use parser tests", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "statement")}, decision); err != nil {
		t.Fatal(err)
	}
	c := experienceCandidate(t, s, "candidate", scope)
	saveCandidate(t, s, c)
	if _, err := s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "ignored", ExpectedRevisions: map[string]int{"statement": 1}, DuplicateIDs: []string{"statement"}, Decision: decision}); err == nil {
		t.Fatal("experience linked to user statement source")
	}
	m := acceptCandidate(t, s, c.ID, "experience", nil, nil)
	u := addRun(t, s, "source-confusion", scope, "The assistant ran parser tests", true)
	update := updateFor(u, "unused")
	update.DuplicateIDs = []string{m.ID}
	if err := s.FinalizeMemory(testContext, u.ID, []MemoryUpdate{update}, decision); err == nil {
		t.Fatal("user statement linked to reviewed tool evidence")
	}
	update.DuplicateIDs = nil
	update.ConflictIDs = []string{m.ID}
	if err := s.FinalizeMemory(testContext, u.ID, []MemoryUpdate{update}, decision); err != nil {
		t.Fatal("cross-source conflict must quarantine both records", err)
	}
	old, err := s.Memory(testContext, m.ID)
	if err != nil || old.Status != "conflicted" || old.SourceType != "reviewed_experience" || old.CandidateID != c.ID {
		t.Fatalf("cross-source conflict lost provenance %+v %v", old, err)
	}
	statement, err := s.Memory(testContext, "unused")
	if err != nil || statement.Status != "conflicted" || statement.SourceType != "user_statement" {
		t.Fatalf("contradicting statement remained active %+v %v", statement, err)
	}
}

func TestExpiryInvalidationAndContextMetadataValidation(t *testing.T) {
	s, _ := testStore(t)
	scope := t.TempDir()
	c := experienceCandidate(t, s, "expires", scope)
	saveCandidate(t, s, c)
	past := time.Now().Add(-time.Hour)
	if _, err := s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "m", ExpiresAt: &past, Decision: decision}); err == nil {
		t.Fatal("expired acceptance allowed")
	}
	future := time.Now().Add(80 * time.Millisecond)
	m, err := s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "m", ExpiresAt: &future, Decision: decision})
	if err != nil {
		t.Fatal(err)
	}
	r := addRun(t, s, "consume", scope, "parser", false)
	for _, mutate := range []func(*Memory){func(m *Memory) { m.SourceType = "user_statement" }, func(m *Memory) { m.CandidateID = "forged" }, func(m *Memory) { m.Conditions = "always" }, func(m *Memory) { m.Steps = []string{"forged"} }, func(m *Memory) { m.Entities = []string{"other"} }, func(m *Memory) { m.EvidenceIDs = []string{"fake"} }, func(m *Memory) { m.ExpiresAt = nil }, func(m *Memory) { m.ReviewNote = "changed" }} {
		bad := m
		mutate(&bad)
		if err := s.SaveContext(testContext, r.ID, []Memory{bad}); err == nil {
			t.Fatal("forged context metadata allowed")
		}
	}
	time.Sleep(time.Until(future) + time.Millisecond)
	found, err := s.Search(testContext, scope, "parser", 10)
	if err != nil || len(found) != 0 {
		t.Fatalf("expired recall %+v %v", found, err)
	}
	all, err := s.Memories(testContext, scope)
	if err != nil || len(all) != 0 {
		t.Fatalf("expired comparison %+v %v", all, err)
	}
	if err := s.SaveContext(testContext, r.ID, []Memory{m}); err == nil {
		t.Fatal("expired snapshot injected")
	}
	if _, err := s.Memory(testContext, m.ID); err != nil {
		t.Fatal("expired history unavailable", err)
	}
	c2 := experienceCandidate(t, s, "invalidate", scope)
	c2.Draft.Title = "Different procedure"
	saveCandidate(t, s, c2)
	active := acceptCandidate(t, s, c2.ID, "active", nil, nil)
	if err := s.InvalidateMemory(testContext, active.ID, "contradicted by the latest test"); err != nil {
		t.Fatal(err)
	}
	invalid, err := s.Memory(testContext, active.ID)
	if err != nil || invalid.Status != "invalidated" || invalid.Revision != 2 || invalid.ReviewNote == "" {
		t.Fatalf("invalidated %+v %v", invalid, err)
	}
	found, err = s.Search(testContext, scope, "parser", 10)
	if err != nil || len(found) != 0 {
		t.Fatalf("invalidated recall %+v %v", found, err)
	}
	if err := s.SaveContext(testContext, r.ID, []Memory{active}); err == nil {
		t.Fatal("revoked snapshot injected")
	}
}

func TestExperienceLifecycleEmptyFailureAndResume(t *testing.T) {
	s, _ := testStore(t)
	scope := t.TempDir()
	r := addRun(t, s, "no-tools", scope, "hello", true)
	if err := s.FinalizeMemory(testContext, r.ID, nil, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.ExperienceFailure(testContext, r.ID, "model unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.SessionCanResume(testContext, r.SessionID); err != nil {
		t.Fatal("independent extraction blocked native resume", err)
	}
	if err := s.SaveCandidates(testContext, r.ID, nil, decision); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Run(testContext, r.ID)
	if err != nil || stored.ExperienceStatus != "empty" || stored.ExperienceError != "" {
		t.Fatalf("empty %+v %v", stored, err)
	}
	if err := s.ExperienceFailure(testContext, r.ID, "late failure"); err == nil {
		t.Fatal("empty receipt overwritten")
	}
	failed := addRun(t, s, "failed", scope, "failed tool task", false)
	if err := s.CompleteRun(testContext, failed.ID, "failed", "", "model", "error"); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Run(testContext, failed.ID)
	if err != nil || stored.ExperienceStatus != "skipped" {
		t.Fatalf("failed %+v %v", stored, err)
	}
	if err := s.SaveCandidates(testContext, failed.ID, nil, decision); err == nil {
		t.Fatal("failed business execution promoted")
	}
}

func TestSchemaMigrationPreservesV1RowsAndReadOnlyRequiresMigration(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db}
	r := addRun(t, old, "old", "project", "old preference", true)
	// Use the historical SQL shape: a v1 binary had no expiration column.
	m := Memory{ID: "old", Scope: r.Scope, Text: r.Input, Kind: "preference", Status: "active", Revision: 1, SourceRunID: r.ID}
	b, _ := json.Marshal(m)
	if _, err := db.Exec("INSERT INTO memories(id,scope,text,status,payload) VALUES(?,?,?,?,?)", m.ID, m.Scope, m.Text, m.Status, b); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if ro, err := Open(home, true); err == nil {
		ro.Close()
		t.Fatal("v1 readonly opened without migration")
	} else if !strings.Contains(err.Error(), "migration") {
		t.Fatal(err)
	}
	store, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Memory(testContext, m.ID)
	m.AgentID, m.AudienceID = DefaultAgentID, OwnerAudienceID
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("migration changed memory %+v %v", got, err)
	}
	if _, err := store.Run(testContext, r.ID); err != nil {
		t.Fatal("migration lost run", err)
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("schema %d %v", version, err)
	}
}

func TestSchemaMigrationRollsBackAllChangesOnFailure(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + "CREATE TABLE candidates(id TEXT);"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(home, false); err == nil {
		store.Close()
		t.Fatal("conflicting migration succeeded")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("partial schema version %d %v", version, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='expires_at'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial column persisted %d %v", count, err)
	}
}
