package state

import "testing"

func TestOffloadMemoryCloseoutDoesNotGateNativeContinuation(t *testing.T) {
	for _, mode := range []string{OffloadMemoryRecall, OffloadMemoryProcess} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			session := Session{ID: "session", Scope: "scope", NativeSessionID: "native", RuntimeFingerprint: testOffloadFingerprint}
			if err := s.CreateSession(testContext, session); err != nil {
				t.Fatal(err)
			}
			r := Run{ID: "run", SessionID: session.ID, Scope: session.Scope, Input: "statement", Agent: "offload", NativeSessionID: session.NativeSessionID, RuntimeFingerprint: session.RuntimeFingerprint, OffloadMemoryMode: mode}
			if err := s.CreateRun(testContext, r); err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteRun(testContext, r.ID, "completed", "answer", "model", ""); err != nil {
				t.Fatal(err)
			}
			got, err := s.Run(testContext, r.ID)
			if err != nil || got.ExperienceStatus != "skipped" {
				t.Fatalf("experience pending: %+v %v", got, err)
			}
			if err := s.SessionCanResume(testContext, session.ID); err != nil {
				t.Fatal(err)
			}
			if mode == OffloadMemoryProcess {
				if got.MemoryStatus != "pending" {
					t.Fatalf("processing not pending: %+v", got)
				}
				if err := s.MemoryFailure(testContext, r.ID, "classifier unavailable"); err != nil {
					t.Fatal(err)
				}
				if err := s.SessionCanResume(testContext, session.ID); err != nil {
					t.Fatalf("memory failure blocks continuation: %v", err)
				}
			} else if got.MemoryStatus != "skipped" || got.AutomaticMemorySkipReason() != "memory_processing_disabled" {
				t.Fatalf("recall-only outcome: %+v", got)
			}
		})
	}
}

func TestOffloadMemoryModeCannotBypassOtherEngineGuards(t *testing.T) {
	for _, engine := range []string{"claude", "offload"} {
		s, _ := testStore(t)
		fp := "legacy-fingerprint"
		if engine == "claude" {
			fp = testOffloadFingerprint
		}
		session := Session{ID: "session", Scope: "scope", NativeSessionID: "native", RuntimeFingerprint: fp}
		if err := s.CreateSession(testContext, session); err != nil {
			t.Fatal(err)
		}
		r := Run{ID: "run", SessionID: session.ID, Scope: session.Scope, Input: "statement", Agent: engine, NativeSessionID: session.NativeSessionID, RuntimeFingerprint: fp, OffloadMemoryMode: OffloadMemoryProcess}
		if err := s.CreateRun(testContext, r); err == nil {
			t.Fatal("foreign engine claimed Offload memory mode")
		}
	}
}
