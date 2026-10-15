package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/experience"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type externalDecider struct {
	fail      bool
	duplicate string
	conflict  string
	calls     int
}

func (d *externalDecider) Decide(context.Context, string, []state.Memory) (memory.Decision, error) {
	d.calls++
	if d.fail {
		return memory.Decision{}, errors.New("decision provider failed")
	}
	out := memory.Decision{Store: true, Kind: "decision", Details: json.RawMessage(`{"reason":"fixture"}`)}
	if d.duplicate != "" {
		out.DuplicateIDs = []string{d.duplicate}
		out.Store = false
	}
	if d.conflict != "" {
		out.ConflictIDs = []string{d.conflict}
	}
	return out, nil
}

type externalJudge struct{ eligible bool }

type externalPartialDecider struct{}

func (externalPartialDecider) Decide(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
	if input == "This second statement cannot be assessed." {
		return memory.Decision{}, errors.New("decision provider failed on second item")
	}
	return memory.Decision{Store: true, Kind: "decision", Details: json.RawMessage(`{"reason":"fixture"}`)}, nil
}

func (j externalJudge) Assess(_ context.Context, _ state.ExperienceDraft, e []state.ToolEvidence) (experience.Assessment, error) {
	if len(e) != 1 || e[0].Provenance != "external_agent_supplied" {
		return experience.Assessment{}, errors.New("lost external attribution")
	}
	return experience.Assessment{Eligible: j.eligible, Audit: json.RawMessage(`{"reason":"fixture"}`)}, nil
}
func (externalJudge) Compare(context.Context, state.Candidate, []state.Memory) (experience.Relations, error) {
	return experience.Relations{Audit: json.RawMessage(`{"reason":"fixture"}`)}, nil
}

func externalHost(t *testing.T) (*Host, state.ExternalConnection) {
	t.Helper()
	root := t.TempDir()
	s, err := state.Open(filepath.Join(root, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.MigrateFileMemory(t.Context(), filepath.Join(root, "memory"), filepath.Join(root, "cache"), "external-test"); err != nil {
		t.Fatal(err)
	}
	c := state.ExternalConnection{ID: "codex", AgentID: "default", AudienceID: "owner", Scope: "project:example", Enabled: true}
	if err := s.SaveExternalConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return &Host{Store: s, Decider: &externalDecider{}, Judge: externalJudge{eligible: true}}, c
}

func TestExternalObjectContextPreservesStatementAndRetrievesSubject(t *testing.T) {
	h, c := externalHost(t)
	r := submitExternal(t, h, c, "plain", false)
	v := r.Submission
	v.Key = "with-object"
	now := time.Now().UTC()
	v.Evidence = append(v.Evidence, state.ExternalEvidence{ID: "object", Role: "user", Author: "owner", Text: "This statement concerns Bastion.", ObservedAt: now})
	v.Items[0].Objects = []state.ExternalObject{{Name: "Bastion", Kind: "system", EvidenceID: "object"}}
	r, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	r = processedExternal(t, h, r)
	if r.Results[0].Status != "published" {
		t.Fatal(r.Results)
	}
	m, err := h.Store.Memory(t.Context(), r.Results[0].MemoryID)
	if err != nil || m.Text != v.Items[0].Text || len(m.Objects) != 1 || m.Objects[0].Evidence.ID != "object" {
		t.Fatal(m, err)
	}
	matches, err := h.Store.Search(t.Context(), c.Scope, "Bastion", 10)
	if err != nil || len(matches) != 1 || matches[0].ID != m.ID {
		t.Fatal(matches, err)
	}
	h.Decider = &externalDecider{duplicate: m.ID}
	v.Key = "different-object-duplicate"
	v.Items[0].Objects[0].Name = "Billing"
	v.Evidence[1].Text = "This statement concerns Billing."
	duplicate, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	duplicate = processedExternal(t, h, duplicate)
	if duplicate.Results[0].Status != "failed" || !strings.Contains(duplicate.Results[0].Reason, "object context differs") {
		t.Fatal("lost object on deduplication", duplicate.Results)
	}
	v.Key = "bad-context"
	v.Items[0].Objects[0].EvidenceID = "missing"
	if _, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v); err == nil {
		t.Fatal("accepted unknown object evidence")
	}
}
func submitExternal(t *testing.T, h *Host, c state.ExternalConnection, key string, procedure bool) state.ExternalReceipt {
	t.Helper()
	now := time.Now().UTC()
	v := state.ExternalSubmission{SchemaVersion: 1, Key: key, Source: state.ExternalSource{Entry: "codex", ObservedAt: now}, Items: []state.ExternalItem{{Kind: "statement", Text: "Ship source distribution only.", EvidenceIDs: []string{"u1"}}}, Evidence: []state.ExternalEvidence{{ID: "u1", Role: "user", Author: "owner", Text: "Ship source distribution only.", ObservedAt: now}}}
	if procedure {
		no := false
		v.Evidence = []state.ExternalEvidence{{ID: "t1", Role: "tool", Author: "codex", Text: "Reported test result", ObservedAt: now, Tool: &state.ExternalTool{Name: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`), Result: json.RawMessage(`"ok"`), IsError: &no}}}
		draft := state.ExperienceDraft{Title: "Check source changes", Summary: "The external agent reported passing Go tests.", Conditions: "When checking this Go module's source changes.", Steps: []string{"Run go test ./... and inspect its reported result."}, EvidenceIDs: []string{"t1"}}
		v.Items = []state.ExternalItem{{Kind: "procedure", EvidenceIDs: []string{"t1"}, Procedure: &draft}}
	}
	r, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func processedExternal(t *testing.T, h *Host, r state.ExternalReceipt) state.ExternalReceipt {
	t.Helper()
	if err := h.ProcessExternal(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	got, err := h.Store.ExternalReceipt(t.Context(), r.Connection.ID, r.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestExternalHostPublicationDuplicateConflictAndBlacklist(t *testing.T) {
	h, c := externalHost(t)
	r := processedExternal(t, h, submitExternal(t, h, c, "one", false))
	if r.Results[0].Status != "published" {
		t.Fatal(r.Results)
	}
	id := r.Results[0].MemoryID
	h.Decider = &externalDecider{duplicate: id}
	duplicate := processedExternal(t, h, submitExternal(t, h, c, "two", false))
	if duplicate.Results[0].MemoryID != id || duplicate.Results[0].Reason != "duplicate_existing_memory" {
		t.Fatal(duplicate)
	}
	if err := h.Store.InvalidateMemory(t.Context(), id, "do not retain this decision"); err != nil {
		t.Fatal(err)
	}
	blocked := processedExternal(t, h, submitExternal(t, h, c, "three", false))
	if blocked.Results[0].Status != "excluded" || blocked.Results[0].MemoryID != "" {
		t.Fatal(blocked)
	}
	h2, c2 := externalHost(t)
	first := processedExternal(t, h2, submitExternal(t, h2, c2, "first", false))
	h2.Decider = &externalDecider{conflict: first.Results[0].MemoryID}
	second := submitExternal(t, h2, c2, "second", false)
	// Distinct exact statement avoids the uniqueness constraint while exercising
	// semantic conflict handling and quarantine of both records.
	v := second.Submission
	v.Key = "conflicting"
	v.Items[0].Text = "Ship binary distribution only."
	v.Evidence[0].Text = v.Items[0].Text
	conflict, err := h2.Store.ReceiveExternal(t.Context(), c2.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	got := processedExternal(t, h2, conflict)
	if got.Results[0].Status != "excluded" || got.Results[0].Reason != "conflicting_or_uncertain_memory" {
		t.Fatal(got)
	}
	m, err := h2.Store.Memory(t.Context(), first.Results[0].MemoryID)
	if err != nil || m.Status != "conflicted" {
		t.Fatal(m, err)
	}
}

func TestExternalHostProviderFailureAndProcedureEvidence(t *testing.T) {
	h, c := externalHost(t)
	h.Decider = &externalDecider{fail: true}
	r := processedExternal(t, h, submitExternal(t, h, c, "failed", false))
	if r.ProcessingStatus != "failed" || r.Results[0].Status != "failed" || r.Results[0].MemoryID != "" {
		t.Fatal(r)
	}
	procedure := processedExternal(t, h, submitExternal(t, h, c, "procedure", true))
	if procedure.Results[0].Status != "published" {
		t.Fatal(procedure)
	}
	m, err := h.Store.Memory(t.Context(), procedure.Results[0].MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.ContextMemoryEvidence(t.Context(), c.Scope, m.ID, 1); err != nil {
		t.Fatal(err)
	}
	cacheRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = assembly.BuildSkillPackage(filepath.Join(cacheRoot, "cache"), "external-task", []state.Memory{m}, assembly.SkillIdentity{SourceIdentity: "external-test", AgentID: "default", AudienceID: "owner", Scopes: []string{c.Scope}}, time.Now())
	if err != nil {
		t.Fatal("external procedure cannot be mounted", err)
	}
	h.Judge = externalJudge{eligible: false}
	withheld := processedExternal(t, h, submitExternal(t, h, c, "unsupported", true))
	if withheld.Results[0].Status != "excluded" {
		t.Fatal(withheld)
	}
}

func TestExternalHostResumesOnlyPendingItems(t *testing.T) {
	h, c := externalHost(t)
	r := submitExternal(t, h, c, "pending", false)
	jobs, err := h.Store.PendingExternal(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	got := processedExternal(t, h, jobs[0])
	if got.Results[0].Status != "published" {
		t.Fatal(got)
	}
	// A stale delivery after recovery observes the saved result and does not
	// publish again. Provider work is unnecessary for completed deliveries.
	if err := h.ProcessExternal(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	jobs, err = h.Store.PendingExternal(t.Context())
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
}

func TestExternalHostRetainsPartialPublication(t *testing.T) {
	h, c := externalHost(t)
	h.Decider = externalPartialDecider{}
	seed := submitExternal(t, h, c, "seed", false)
	v := seed.Submission
	v.Key = "partial"
	v.Items = append(v.Items, state.ExternalItem{Kind: "statement", Text: "This second statement cannot be assessed.", EvidenceIDs: []string{"u2"}})
	v.Evidence = append(v.Evidence, state.ExternalEvidence{ID: "u2", Role: "user", Author: "owner", Text: v.Items[1].Text, ObservedAt: time.Now().UTC()})
	r, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), v)
	if err != nil {
		t.Fatal(err)
	}
	got := processedExternal(t, h, r)
	if got.ProcessingStatus != "failed" || got.Results[0].Status != "published" || got.Results[1].Status != "failed" {
		t.Fatal(got.Results)
	}
	if _, err := h.Store.ContextMemory(t.Context(), c.Scope, got.Results[0].MemoryID, 1); err != nil {
		t.Fatal("partial success was lost", err)
	}
}
