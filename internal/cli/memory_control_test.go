package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

type workbenchJudge struct{ compared int }

func (*workbenchJudge) Assess(context.Context, state.ExperienceDraft, []state.ToolEvidence) (experience.Assessment, error) {
	return experience.Assessment{}, errors.New("workbench must not extract or assess new experience")
}
func (j *workbenchJudge) Compare(context.Context, state.Candidate, []state.Memory) (experience.Relations, error) {
	j.compared++
	return experience.Relations{Model: "fixture-judge", Audit: json.RawMessage(`{"reason":"independent"}`)}, nil
}

func TestWorkbenchMemoryActionsUseHostAndPreserveBoundaries(t *testing.T) {
	f := seedReviewFixture(t)
	s, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AppendEvent(t.Context(), &event.Event{Type: "run.finished", RunID: f.runID}); err != nil {
		t.Fatal(err)
	}
	judge := &workbenchJudge{}
	control := &memoryControl{store: s, publish: func(ctx context.Context, c state.Candidate, note string) (state.Memory, error) {
		h := &host.Host{Store: s, Judge: judge, Objects: fixtureObjects{}, Config: host.Config{AgentID: c.AgentID, AudienceID: "owner", Scope: c.Scope}}
		return h.AcceptExperience(ctx, c.ID, note, nil)
	}}
	call := func(a memoryAction) *httptest.ResponseRecorder {
		b, _ := json.Marshal(a)
		w := httptest.NewRecorder()
		control.ServeHTTP(w, httptest.NewRequest("POST", "/memory-action", bytes.NewReader(b)))
		return w
	}
	base := memoryAction{Action: "accept", ID: f.candidateID, Agent: "default", Audience: "owner"}
	wrong := base
	wrong.Audience = "group-a"
	if w := call(wrong); w.Code != 400 {
		t.Fatal("group mutation accepted", w.Code, w.Body.String())
	}
	wrong = base
	wrong.Agent = "other-agent"
	if w := call(wrong); w.Code != 409 || judge.compared != 0 {
		t.Fatal("cross-partition mutation reached publisher", w.Code)
	}
	if w := call(base); w.Code != 200 || judge.compared != 1 {
		t.Fatal("publication did not reuse Host judgment", w.Code, w.Body.String())
	}
	c, err := s.GetCandidate(t.Context(), f.candidateID)
	if err != nil || c.Status != "accepted" {
		t.Fatal("candidate not persisted", c, err)
	}
	if w := call(base); w.Code != 409 || judge.compared != 1 {
		t.Fatal("duplicate publication was replayed", w.Code)
	}
	m, err := s.Memory(t.Context(), c.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	disable := memoryAction{Action: "invalidate", ID: m.ID, Agent: "default", Audience: "owner", Note: "fixture disabled", Revision: m.Revision + 1}
	if w := call(disable); w.Code != 409 {
		t.Fatal("stale revision accepted", w.Code)
	}
	m, _ = s.Memory(t.Context(), m.ID)
	if m.Status != "active" {
		t.Fatal("stale request changed memory")
	}
	disable.Revision = m.Revision
	if w := call(disable); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	m, _ = s.Memory(t.Context(), m.ID)
	if m.Status != "invalidated" || m.ReviewNote != disable.Note {
		t.Fatal("disable lost status or reason", m)
	}
}

func TestWorkbenchRejectReasonAndFinalizingSource(t *testing.T) {
	f := seedReviewFixture(t)
	s, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	control := &memoryControl{store: s, publish: func(context.Context, state.Candidate, string) (state.Memory, error) {
		t.Fatal("reject invoked publication")
		return state.Memory{}, nil
	}}
	call := func(note string) *httptest.ResponseRecorder {
		a := memoryAction{Action: "reject", ID: f.candidateID, Agent: "default", Audience: "owner", Note: note}
		b, _ := json.Marshal(a)
		w := httptest.NewRecorder()
		control.ServeHTTP(w, httptest.NewRequest("POST", "/memory-action", bytes.NewReader(b)))
		return w
	}
	if w := call(""); w.Code != 400 {
		t.Fatal("reasonless reject accepted")
	}
	if w := call("not reusable"); w.Code != 409 {
		t.Fatal("unfinished source was mutated")
	}
	if err := s.AppendEvent(t.Context(), &event.Event{Type: "run.finished", RunID: f.runID}); err != nil {
		t.Fatal(err)
	}
	if w := call("not reusable"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	c, _ := s.GetCandidate(t.Context(), f.candidateID)
	if c.Status != "rejected" || c.ReviewNote != "not reusable" {
		t.Fatal("rejection was not persisted", c)
	}
}

func TestMemoryControlSocketIsPrivateAndOwnedByService(t *testing.T) {
	home, err := os.MkdirTemp("", "ah-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	path := filepath.Join(home, "memory-control.sock")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := startMemoryControl(t.Context(), home, &memoryControl{}); err == nil {
		t.Fatal("regular file was replaced")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stop, err := startMemoryControl(t.Context(), home, &memoryControl{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket is not owner-private", err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	r, err := client.Get("http://localhost/memory-action")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 405 {
		t.Fatal("unsupported action was accepted")
	}
	stop()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("socket outlived managed service", err)
	}
}
