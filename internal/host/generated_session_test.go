package host

import (
	"agent-host/internal/harness"
	"agent-host/internal/state"
	"context"
	"testing"
)

type generatedSessionRunner struct{ closeoutRunner }

func (generatedSessionRunner) GeneratesSessionID() bool { return true }
func (generatedSessionRunner) Identity() harness.Identity {
	return harness.Identity{Agent: "offload", Engine: "offload", Transport: "go-package"}
}
func (r generatedSessionRunner) Run(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	if req.Resume && req.NativeSessionID != "native-generated-session" {
		return harness.Result{}, context.Canceled
	}
	if err := emit(harness.Observation{Type: "init", NativeSessionID: "native-generated-session"}); err != nil {
		return harness.Result{}, err
	}
	result, err := r.closeoutRunner.Run(ctx, req, emit)
	result.NativeSessionID = "native-generated-session"
	return result, err
}

func TestFullHostPersistsGeneratedSessionBeforeResume(t *testing.T) {
	h, _ := testHost(t)
	h.Runner = generatedSessionRunner{}
	session := newSession(t, h)
	if !state.IsOffloadPending(session.NativeSessionID) {
		t.Fatal("missing pending native identity")
	}
	run, err := h.Run(context.Background(), session, "What is the answer?")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := h.Store.Session(context.Background(), session.ID)
	if err != nil || saved.NativeSessionID != "native-generated-session" {
		t.Fatalf("%+v %v", saved, err)
	}
	stored, err := h.Store.Run(context.Background(), run.ID)
	if err != nil || stored.NativeSessionID != saved.NativeSessionID {
		t.Fatalf("%+v %v", stored, err)
	}
	if _, err := h.Run(context.Background(), saved, "Repeat the answer."); err != nil {
		t.Fatal(err)
	}
}
