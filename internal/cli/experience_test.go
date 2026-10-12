package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type reviewFixture struct{ home, workspace, runID, candidateID, memoryID string }

func seedReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	f := reviewFixture{home: t.TempDir(), workspace: t.TempDir(), runID: "review-run", candidateID: "pending-candidate", memoryID: "reviewed-memory"}
	var err error
	f.workspace, err = filepath.EvalSymlinks(f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	if err = s.CreateSession(ctx, state.Session{ID: "review-session", Scope: f.workspace, Model: "test", NativeSessionID: "native-session", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(ctx, state.Run{ID: f.runID, SessionID: "review-session", NativeSessionID: "native-session", Scope: f.workspace, Input: "Fix the fixture", RequestedModel: "test", ExecutionStatus: "running", MemoryStatus: "blocked", AcceptanceStatus: "unverified", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []harness.ToolObservation{
		{ID: "test-call", Name: "Bash", Stage: "call", Input: json.RawMessage(`{"command":"go test ./..."}`)},
		{ID: "test-call", Name: "Bash", Stage: "result", Result: json.RawMessage(`"ok fixture"`)},
	} {
		e := event.Event{Version: event.Version, RunID: f.runID, SessionID: "review-session", At: time.Now().UTC(), Type: "tool.activity", Phase: "execute", Component: "harness.claude", Data: map[string]any{"tool": o}}
		if err = s.AppendEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CompleteRun(ctx, f.runID, "completed", "Tests reported passing", "resolved-test", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeMemory(ctx, f.runID, nil, json.RawMessage(`{"kind":"none"}`)); err != nil {
		t.Fatal(err)
	}
	draft := state.ExperienceDraft{Title: "Fixture test check", Summary: "The fixture test command reported passing.", Conditions: "For the same fixture package", Steps: []string{"Run go test ./... and inspect the report."}, EvidenceIDs: []string{"test-call"}}
	c := state.Candidate{ID: f.candidateID, RunID: f.runID, Scope: f.workspace, Draft: draft, Model: "resolved-extractor", Assessment: json.RawMessage(`{"eligible":true}`)}
	accepted := c
	accepted.ID = "accepted-candidate"
	accepted.Draft.Title = "Reviewed fixture test procedure"
	if err = s.SaveCandidates(ctx, f.runID, []state.Candidate{c, accepted}, json.RawMessage(`{"reason":"extracted"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptCandidate(ctx, accepted.ID, state.CandidateAcceptance{MemoryID: f.memoryID, Note: "Reviewed test evidence", ExpectedRevisions: map[string]int{}, Decision: json.RawMessage(`{"reason":"independent"}`)}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f reviewFixture) args(action string, extra ...string) []string {
	args := []string{"memory", action, "--home", f.home, "--workspace", f.workspace, "--claude", "/nonexistent-native-must-not-run"}
	return append(args, extra...)
}

func TestCandidateDetailsIncludeSourcesWithoutCredentials(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	f := seedReviewFixture(t)
	var out bytes.Buffer
	if err := Run(context.Background(), f.args("candidate", f.candidateID), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var details struct {
		Candidate state.Candidate      `json:"candidate"`
		Evidence  []state.ToolEvidence `json:"evidence"`
	}
	if err := json.Unmarshal(out.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if details.Candidate.Status != "pending" || details.Candidate.RunID != f.runID || len(details.Evidence) != 1 || details.Evidence[0].ID != "test-call" || details.Evidence[0].CallSeq >= details.Evidence[0].ResultSeq || !strings.Contains(string(details.Evidence[0].Input), "go test ./...") {
		t.Fatalf("missing review evidence: %+v", details)
	}
	out.Reset()
	if err := Run(context.Background(), f.args("candidates", "--run", f.runID), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var candidates []state.Candidate
	if err := json.Unmarshal(out.Bytes(), &candidates); err != nil || len(candidates) != 1 || candidates[0].ID != f.candidateID {
		t.Fatalf("default pending filter=%s err=%v", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), f.args("candidates", "--run", f.runID, "--status", "all"), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &candidates); err != nil || len(candidates) != 2 {
		t.Fatalf("all filter=%s err=%v", out.String(), err)
	}
}

func TestLocalReviewRejectsAndInvalidatesWithoutModels(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	f := seedReviewFixture(t)
	var out bytes.Buffer
	if err := Run(context.Background(), f.args("reject", "--reason", "Unsupported applicability", f.candidateID), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var candidate state.Candidate
	if err := json.Unmarshal(out.Bytes(), &candidate); err != nil || candidate.Status != "rejected" || candidate.ReviewNote != "Unsupported applicability" || candidate.ReviewedAt == nil {
		t.Fatalf("rejection=%s err=%v", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), f.args("invalidate", "--reason", "Fixture changed", f.memoryID), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var m state.Memory
	if err := json.Unmarshal(out.Bytes(), &m); err != nil || m.Status != "invalidated" || m.Revision != 2 || m.ReviewNote != "Fixture changed" || m.SourceType != "reviewed_experience" {
		t.Fatalf("invalidation=%s err=%v", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), f.args("search", "fixture"), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var memories []state.Memory
	if err := json.Unmarshal(out.Bytes(), &memories); err != nil || len(memories) != 0 {
		t.Fatalf("invalidated knowledge remained searchable: %s %v", out.String(), err)
	}
	for _, args := range [][]string{f.args("reject", "--reason", "Again", f.candidateID), f.args("invalidate", "--reason", "Again", f.memoryID)} {
		if err := Run(context.Background(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("repeated terminal review silently succeeded")
		}
	}
}

func TestCandidateAndMemoryCommandsEnforceWorkspaceScope(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	f := seedReviewFixture(t)
	other := f
	other.workspace = t.TempDir()
	for _, args := range [][]string{
		other.args("candidate", f.candidateID),
		other.args("candidates", "--run", f.runID),
		other.args("show", f.memoryID),
		other.args("reject", "--reason", "Wrong scope", f.candidateID),
		other.args("invalidate", "--reason", "Wrong scope", f.memoryID),
	} {
		var out bytes.Buffer
		err := Run(context.Background(), args, nil, &out, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "another workspace") || out.Len() != 0 {
			t.Fatalf("scope leaked output or mutated review: args=%v out=%s err=%v", args, out.String(), err)
		}
	}
	s, err := state.Open(f.home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.GetCandidate(context.Background(), f.candidateID)
	if err != nil || c.Status != "pending" {
		t.Fatalf("out-of-scope rejection mutated candidate: %+v %v", c, err)
	}
	m, err := s.Memory(context.Background(), f.memoryID)
	if err != nil || m.Status != "active" || m.Revision != 1 {
		t.Fatalf("out-of-scope invalidation mutated memory: %+v %v", m, err)
	}
}
