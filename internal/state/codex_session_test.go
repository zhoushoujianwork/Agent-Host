package state

import "testing"

func TestFullHostBindsGeneratedNativeSessionOnlyOnce(t *testing.T) {
	s, _ := testStore(t)
	session := Session{ID: "host", Scope: "owner", NativeSessionID: OffloadPendingPrefix + "host", RuntimeFingerprint: "full-host-fingerprint"}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run", SessionID: session.ID, Scope: session.Scope, Input: "task", Agent: "offload", NativeSessionID: session.NativeSessionID, RuntimeFingerprint: session.RuntimeFingerprint}
	if err := s.CreateRun(testContext, run); err != nil {
		t.Fatal(err)
	}
	if err := s.BindHostNativeSession(testContext, run.ID, session.NativeSessionID, "codex-native"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Session(testContext, session.ID)
	if err != nil || got.NativeSessionID != "codex-native" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.BindHostNativeSession(testContext, run.ID, session.NativeSessionID, "different"); err == nil {
		t.Fatal("rebind accepted")
	}
}
