package state

import (
	"agent-host/internal/annotation"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func assistanceFixture(t *testing.T, s *Store) (DiscoverySource, DiscoveryCollection, []DiscoveryEvidence, []MatterDraft) {
	t.Helper()
	source, collection, e := discoveryFixture(t, s)
	e.Text = "Which readiness probe should deployment use?"
	e = PrepareDiscoveryEvidence(e)
	e2 := e
	e2.MessageID = "m2"
	e2.Text = "I will prepare the billing migration checklist."
	e2.CreatedAt = e.CreatedAt.Add(time.Millisecond)
	e2 = PrepareDiscoveryEvidence(e2)
	evidence := []DiscoveryEvidence{e, e2}
	collection.Count = 2
	if _, err := s.SaveDiscoveryCollection(testContext, source, collection, evidence); err != nil {
		t.Fatal(err)
	}
	drafts := []MatterDraft{{Kind: "question", Status: "open", Title: "Deployment readiness", Summary: "A participant asked which readiness probe to use.", OpenItem: "Which readiness probe should deployment use?", EvidenceIDs: []string{e.ID}}, {Kind: "commitment", Status: "open", Title: "Billing migration", Summary: "A participant committed to prepare a billing migration checklist.", OpenItem: "Prepare billing checklist", EvidenceIDs: []string{e2.ID}}}
	if err := s.SaveMatterExtraction(testContext, source.ID, AssistanceDigest(evidence), "fixture", drafts, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	return source, collection, evidence, drafts
}

func TestMatterProgressStaysSeparateAcrossRestartAndWithdrawal(t *testing.T) {
	s, home := testStore(t)
	source, collection, evidence, _ := assistanceFixture(t, s)
	ids := []string{source.ID}
	before, err := s.SearchMatters(testContext, source.ProjectID, ids, "readiness", 10)
	if err != nil || len(before) != 1 || !before[0].Fresh {
		t.Fatalf("matter retrieval: %+v %v", before, err)
	}
	old := before[0].Matter
	answer := evidence[0]
	answer.MessageID = "answer"
	answer.Text = "Use /readyz for deployment readiness. The deployment question is answered."
	answer.CreatedAt = answer.CreatedAt.Add(time.Second)
	answer = PrepareDiscoveryEvidence(answer)
	evidence = append(evidence, answer)
	collection.Count = 3
	collection.At = time.Now().UTC()
	if _, err = s.SaveDiscoveryCollection(testContext, source, collection, evidence); err != nil {
		t.Fatal(err)
	}
	draft := old.Draft
	draft.ExpectedRevision = old.Revision
	draft.Status = "reported_resolved"
	draft.Summary = "A participant answered the readiness question; no deployment success was independently observed."
	draft.OpenItem = ""
	draft.EvidenceIDs = append(draft.EvidenceIDs, answer.ID)
	if err = s.SaveMatterExtraction(testContext, source.ID, AssistanceDigest(evidence), "fixture", []MatterDraft{draft}, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	after, err := reader.MatterView(testContext, source.ProjectID, ids, old.ID)
	if err != nil || after.Matter.Revision != 2 || after.Matter.Draft.Status != "reported_resolved" {
		t.Fatal("lost stable matter/revision", after, err)
	}
	suggestions, err := reader.Suggestions(testContext, source.ProjectID, ids)
	if err != nil || len(suggestions) != 1 || suggestions[0].MatterID == old.ID {
		t.Fatal("resolved work suggested or unrelated issue merged", suggestions, err)
	}
	for _, table := range []string{"runs", "memories", "candidates"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("matter processing wrote business/memory state", table, n, err)
		}
	}
	withdrawn := answer
	withdrawn.Availability = "withdrawn"
	withdrawn.Text = ""
	withdrawn.UpdatedAt = answer.CreatedAt.Add(time.Minute)
	withdrawn = PrepareDiscoveryEvidence(withdrawn)
	evidence[2] = withdrawn
	if _, err = s.SaveDiscoveryCollection(testContext, source, collection, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.MatterView(testContext, source.ProjectID, ids, old.ID); !errors.Is(err, ErrContextUnavailable) {
		t.Fatal("withdrawn citation exposed matter", err)
	}
	if _, err = reader.MatterEvidence(testContext, source.ProjectID, ids, old.ID); !errors.Is(err, ErrContextUnavailable) {
		t.Fatal("withdrawn source content exposed", err)
	}
}

func TestMatterAccessFailuresAndPartialCoverageSuppressActions(t *testing.T) {
	s, _ := testStore(t)
	source, collection, evidence, _ := assistanceFixture(t, s)
	ids := []string{source.ID}
	matters, _ := s.SourceMatters(testContext, source.ID)
	m := matters[0]
	for _, check := range []struct {
		store   *Store
		project string
		sources []string
	}{{s.WithPartition("default", "group:one"), source.ProjectID, ids}, {s, "another-project", ids}, {s, source.ProjectID, nil}} {
		if _, err := check.store.MatterView(testContext, check.project, check.sources, m.ID); !errors.Is(err, ErrContextUnavailable) {
			t.Fatal("out-of-scope matter read", err)
		}
		if _, err := check.store.MatterEvidence(testContext, check.project, check.sources, m.ID); !errors.Is(err, ErrContextUnavailable) {
			t.Fatal("out-of-scope citation read", err)
		}
	}
	collection.Complete = false
	collection.Partial = true
	collection.HasMore = true
	collection.At = time.Now().UTC()
	if _, err := s.SaveDiscoveryCollection(testContext, source, collection, evidence); err != nil {
		t.Fatal(err)
	}
	v, err := s.MatterView(testContext, source.ProjectID, ids, m.ID)
	if err != nil || v.Fresh || v.Limitation == "" {
		t.Fatal("partial collection concealed", v, err)
	}
	list, err := s.Suggestions(testContext, source.ProjectID, ids)
	if err != nil || len(list) != 0 {
		t.Fatal("partial coverage caused a suggestion", list, err)
	}
}

func TestMatterFeedbackDoesNotChangeMemoryOrRepeatDismissedSuggestions(t *testing.T) {
	s, _ := testStore(t)
	source, _, evidence, _ := assistanceFixture(t, s)
	ids := []string{source.ID}
	list, _ := s.Suggestions(testContext, source.ProjectID, ids)
	first := list[0]
	if err := s.FeedbackSuggestion(testContext, source.ProjectID, ids, first.ID, first.Revision, "postpone", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Suggestions(testContext, source.ProjectID, ids)
	if len(list) != 1 || list[0].ID == first.ID {
		t.Fatal("postponed suggestion still visible")
	}
	postponed, _, _ := s.Suggestion(testContext, source.ProjectID, ids, first.ID)
	if err := s.FeedbackSuggestion(testContext, source.ProjectID, ids, first.ID, first.Revision, "dismiss", time.Time{}); err == nil {
		t.Fatal("stale feedback accepted")
	}
	if err := s.FeedbackSuggestion(testContext, source.ProjectID, ids, first.ID, postponed.Revision, "dismiss", time.Time{}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.MatterView(testContext, source.ProjectID, ids, first.MatterID)
	d := m.Matter.Draft
	d.ExpectedRevision = m.Matter.Revision
	d.Summary += " Reconfirmed in the collected window."
	if err := s.SaveMatterExtraction(testContext, source.ID, AssistanceDigest(evidence), "fixture", []MatterDraft{d}, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	dismissed, _, _ := s.Suggestion(testContext, source.ProjectID, ids, first.ID)
	if dismissed.Status != "dismissed" {
		t.Fatal("new extraction revived dismissal")
	}
	list, _ = s.Suggestions(testContext, source.ProjectID, ids)
	if err := s.FeedbackSuggestion(testContext, source.ProjectID, ids, list[0].ID, list[0].Revision, "resolve", time.Time{}); err != nil {
		t.Fatal(err)
	}
	resolved, view, _ := s.Suggestion(testContext, source.ProjectID, ids, list[0].ID)
	if resolved.Status != "withdrawn" || !view.Matter.OwnerResolved || view.Matter.Draft.Status != "open" {
		t.Fatal("owner resolution confused inference with outcome", resolved, view)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n)
	if n != 0 {
		t.Fatal("feedback published memory")
	}
}

func TestPreparationClaimIsAtomicAndNeverReplayedAfterRecovery(t *testing.T) {
	s, _ := testStore(t)
	source, _, _, _ := assistanceFixture(t, s)
	ids := []string{source.ID}
	list, _ := s.Suggestions(testContext, source.ProjectID, ids)
	choice := list[0]
	v, _ := s.MatterView(testContext, source.ProjectID, ids, choice.MatterID)
	p, _ := s.Project(testContext, source.ProjectID)
	refs := []ProjectRef{{ID: p.ID, Name: p.Name, Scope: p.Scope, Workspace: p.Workspace}}
	session := Session{ID: "chosen-session", Scope: p.Scope, ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, Projects: refs, Model: "fixture", NativeSessionID: "chosen-native"}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "chosen-run", SessionID: session.ID, Scope: p.Scope, ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, Projects: refs, NativeSessionID: session.NativeSessionID, Input: "Prepare a cited checklist.", Preparation: &PreparationSource{SuggestionID: choice.ID, SuggestionRevision: choice.Revision, MatterID: choice.MatterID, MatterRevision: choice.MatterRevision, EvidenceIDs: v.Matter.Draft.EvidenceIDs}}
	bad := run
	bad.SessionID = "missing-session"
	if err := s.CreatePreparationRun(testContext, bad, ids); err == nil {
		t.Fatal("invalid preparation run accepted")
	}
	unchanged, _, _ := s.Suggestion(testContext, p.ID, ids, choice.ID)
	if unchanged.Status != "ready" {
		t.Fatal("failed transaction consumed selection")
	}
	if err := s.CreatePreparationRun(testContext, run, ids); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(testContext); err != nil {
		t.Fatal(err)
	}
	run.ID = "replayed-run"
	if err := s.CreatePreparationRun(testContext, run, ids); err == nil {
		t.Fatal("replayed selected action after crash/recovery")
	}
	if _, err := s.Run(testContext, run.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("replay created a task", err)
	}
	linked, _, _ := s.Suggestion(testContext, p.ID, ids, choice.ID)
	if linked.RunID != "chosen-run" {
		t.Fatal("lost linked run")
	}
}

func TestMatterExtractionRejectsEvidenceDriftAndForeignCitationsAtomically(t *testing.T) {
	s, _ := testStore(t)
	source, _, evidence, _ := assistanceFixture(t, s)
	matters, _ := s.SourceMatters(testContext, source.ID)
	d := matters[0].Draft
	d.ExpectedRevision = matters[0].Revision
	if err := s.SaveMatterExtraction(testContext, source.ID, "old-digest", "fixture", []MatterDraft{d}, json.RawMessage(`{}`), ""); err == nil {
		t.Fatal("stale evidence snapshot accepted")
	}
	d.EvidenceIDs = []string{"invented-citation"}
	if err := s.SaveMatterExtraction(testContext, source.ID, AssistanceDigest(evidence), "fixture", []MatterDraft{d}, json.RawMessage(`{}`), ""); err == nil {
		t.Fatal("invented citation accepted")
	}
	current, _ := s.SourceMatters(testContext, source.ID)
	if current[0].Revision != matters[0].Revision {
		t.Fatal("invalid extraction partially committed")
	}
	if err := s.SaveMatterExtraction(testContext, source.ID, AssistanceDigest(evidence), "fixture", nil, json.RawMessage(`{}`), "native failed"); err != nil {
		t.Fatal(err)
	}
	list, err := s.Suggestions(testContext, source.ProjectID, []string{source.ID})
	if err != nil || len(list) != 0 {
		t.Fatal("failed reconciliation left actionable stale suggestions", list, err)
	}
}

func TestMatterRankingPrefersSubjectOverIncidentalSummaryMention(t *testing.T) {
	s, _ := testStore(t)
	source, _, evidence, _ := assistanceFixture(t, s)
	matters, _ := s.SourceMatters(testContext, source.ID)
	for _, m := range matters {
		if m.Draft.Kind != "commitment" {
			continue
		}
		d := m.Draft
		d.ExpectedRevision = m.Revision
		d.Summary = "The billing checklist is separate from the deployment readiness question."
		if err := s.SaveMatterExtraction(testContext, source.ID, AssistanceDigest(evidence), "fixture", []MatterDraft{d}, json.RawMessage(`{}`), ""); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.SearchMatters(testContext, source.ProjectID, []string{source.ID}, "readiness", 1)
	if err != nil || len(hits) != 1 || hits[0].Matter.Draft.Kind != "question" {
		t.Fatal("incidental mention displaced actual subject", hits, err)
	}
}

func TestMatterReconciliationScrubsSupersededProseAndUnavailableSources(t *testing.T) {
	s, _ := testStore(t)
	source, c, evidence, _ := assistanceFixture(t, s)
	changed := evidence[0]
	changed.Text = "The original question was corrected: deployment needs a health check."
	changed.UpdatedAt = changed.CreatedAt.Add(time.Second)
	changed = PrepareDiscoveryEvidence(changed)
	evidence[0] = changed
	if _, err := s.SaveDiscoveryCollection(testContext, source, c, evidence); err != nil {
		t.Fatal(err)
	}
	inputs, err := s.ReconciliationMatters(testContext, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range inputs {
		if m.Draft.Kind == "question" && (m.Draft.Summary != "" || m.Draft.Title != "" || len(m.Draft.EvidenceIDs) != 1 || m.Draft.EvidenceIDs[0] != changed.ID) {
			t.Fatal("superseded prose reached reconciliation", m)
		}
	}
	withdrawn := changed
	withdrawn.Availability = "withdrawn"
	withdrawn.Text = ""
	withdrawn.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	withdrawn = PrepareDiscoveryEvidence(withdrawn)
	evidence[0] = withdrawn
	if _, err := s.SaveDiscoveryCollection(testContext, source, c, evidence); err != nil {
		t.Fatal(err)
	}
	inputs, err = s.ReconciliationMatters(testContext, source.ID)
	if err != nil || len(inputs) != 1 || inputs[0].Draft.Kind != "commitment" {
		t.Fatal("withdrawn matter reached model input", inputs, err)
	}
}

func TestContextReadsAreNotExecutionExperience(t *testing.T) {
	evidence := []ToolEvidence{{ID: "context", Name: "mcp__host_context__read_context", Input: json.RawMessage(`{}`), Result: json.RawMessage(`{"role":"durable_memory"}`)}, {ID: "source", Name: "mcp__host_context__read_context_evidence", Input: json.RawMessage(`{}`), Result: json.RawMessage(`{"role":"participant_report"}`)}, {ID: "observed", Name: "Bash", Input: json.RawMessage(`{"command":"go test"}`), Result: json.RawMessage(`"passed"`)}}
	eligible := ExecutionExperienceEvidence(evidence)
	if len(eligible) != 1 || eligible[0].ID != "observed" {
		t.Fatal("reference reads entered execution experience", eligible)
	}
}

func TestReadinessSuccessTokenQueryRetrievesSavedGuidance(t *testing.T) {
	s, _ := testStore(t)
	recallSeed(t, s, "readiness", "app", "The deployment readiness endpoint is /readyz. Release may proceed only when it returns READY-123.")
	recallSeed(t, s, "credential", "app", "The deployment authentication bearer token is a separate secret credential.")
	result, err := s.Retrieve(testContext, "app", "deployment readiness endpoint success token", 10)
	if err != nil || !hasContextSource(recallIDs(result.Matches), "readiness") {
		t.Fatal("readiness marker mistaken for credential", result, err)
	}
	credentials, err := s.Retrieve(testContext, "app", "authentication bearer token", 10)
	if err != nil || hasContextSource(recallIDs(credentials.Matches), "readiness") || !hasContextSource(recallIDs(credentials.Matches), "credential") {
		t.Fatal("genuine token filtering weakened", credentials, err)
	}
}

func TestAnnotationUpgradePreservesSourceMemoryAndUsesCurrentPartitions(t *testing.T) {
	s, home := testStore(t)
	recallSeed(t, s, "ready", "app", "The readiness success token is READY-123.")
	var original []byte
	if err := s.db.QueryRow("SELECT payload FROM memories WHERE id='ready'").Scan(&original); err != nil {
		t.Fatal(err)
	}
	a := annotation.Extract("app", "The readiness success token is READY-123.")
	a.Version = "entity-topic-v1"
	legacy, _ := json.Marshal(a)
	if _, err := s.db.Exec("UPDATE memory_annotations SET version=?,payload=? WHERE memory_id='ready'", a.Version, legacy); err != nil {
		t.Fatal(err)
	}
	read, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := read.Retrieve(testContext, "app", "readiness success token", 10); err == nil {
		t.Fatal("old annotation silently used")
	}
	read.Close()
	upgraded, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	result, err := upgraded.Retrieve(testContext, "app", "readiness success token", 10)
	if err != nil || !hasContextSource(recallIDs(result.Matches), "ready") {
		t.Fatal("annotation upgrade did not rebuild applicable metadata", result, err)
	}
	var retained []byte
	if err := upgraded.db.QueryRow("SELECT payload FROM memories WHERE id='ready'").Scan(&retained); err != nil || !bytes.Equal(original, retained) {
		t.Fatal("derived migration rewrote source knowledge", err)
	}
}
