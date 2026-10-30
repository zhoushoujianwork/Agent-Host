package state

import (
	"agent-host/internal/event"
	"strings"
	"testing"
	"time"
)

const testOffloadFingerprint = OffloadFingerprintPrefix + "fixture"

// offloadRun links a claimed channel message to a fresh Offload session whose
// native ID is still the placeholder.
func offloadRun(t *testing.T, s *Store, id, fingerprint string) (ChannelMessage, Run) {
	t.Helper()
	m := channelInput(id)
	m.Binding = "binding-" + id
	enqueueChannel(t, s, m)
	claimChannel(t, s, m.Profile)
	session := Session{ID: "session-" + id, Scope: m.Scope, Model: "m", NativeSessionID: OffloadPendingPrefix + id, RuntimeFingerprint: fingerprint}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, m.Binding, session.ID); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run-" + id, SessionID: session.ID, Scope: m.Scope, Input: m.Text, NativeSessionID: session.NativeSessionID, RuntimeFingerprint: fingerprint, RequestedModel: "m", MemoryPluginDisabled: true}
	if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	return m, run
}

func TestBindOffloadSessionUpdatesRunAndSessionAtomicallyAndIsIdempotent(t *testing.T) {
	s, _ := testStore(t)
	_, run := offloadRun(t, s, "a", testOffloadFingerprint)
	if err := s.BindOffloadSession(testContext, run.ID, run.NativeSessionID, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindOffloadSession(testContext, run.ID, "sess-1", "sess-1"); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
	gotRun, err := s.Run(testContext, run.ID)
	if err != nil || gotRun.NativeSessionID != "sess-1" || !gotRun.MemoryPluginDisabled {
		t.Fatalf("run: %+v %v", gotRun, err)
	}
	gotSession, err := s.Session(testContext, run.SessionID)
	if err != nil || gotSession.NativeSessionID != "sess-1" {
		t.Fatalf("session: %+v %v", gotSession, err)
	}
	if err := s.BindOffloadSession(testContext, run.ID, "sess-1", "sess-2"); err == nil {
		t.Fatal("actual native ID was rebound")
	}
	if err := s.BindOffloadSession(testContext, run.ID, run.NativeSessionID, "sess-2"); err == nil {
		t.Fatal("stale expected placeholder rebound a bound session")
	}
}

func TestBindOffloadSessionRejectsInvalidIDsAndForeignOwners(t *testing.T) {
	s, _ := testStore(t)
	_, run := offloadRun(t, s, "a", testOffloadFingerprint)
	for name, id := range map[string]string{
		"empty": "", "placeholder": OffloadPendingPrefix + "x", "control": "a\nb", "padded": " a",
		"oversized": strings.Repeat("a", maxOffloadIDBytes+1),
	} {
		if err := s.BindOffloadSession(testContext, run.ID, run.NativeSessionID, id); err == nil {
			t.Errorf("%s ID accepted", name)
		}
	}
	if err := s.BindOffloadSession(testContext, run.ID, "other-expected", "sess"); err == nil {
		t.Error("mismatched expected ID accepted")
	}
	if err := s.BindOffloadSession(testContext, "absent", run.NativeSessionID, "sess"); err == nil {
		t.Error("absent run accepted")
	}
	if err := s.WithPartition("other", "owner").BindOffloadSession(testContext, run.ID, run.NativeSessionID, "sess"); err == nil {
		t.Error("cross-partition bind accepted")
	}
	got, _ := s.Session(testContext, run.SessionID)
	if got.NativeSessionID != run.NativeSessionID {
		t.Fatalf("rejected binds changed session: %+v", got)
	}
}

func TestBindOffloadSessionRejectsOtherEngineTerminalAndSharedSessions(t *testing.T) {
	s, _ := testStore(t)
	_, native := offloadRun(t, s, "native", "plain-fingerprint")
	if err := s.BindOffloadSession(testContext, native.ID, native.NativeSessionID, "sess"); err == nil {
		t.Error("non-Offload runtime was rebound")
	}
	_, done := offloadRun(t, s, "done", testOffloadFingerprint)
	if err := s.CompleteRun(testContext, done.ID, "failed", "", "", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindOffloadSession(testContext, done.ID, done.NativeSessionID, "sess-done"); err == nil {
		t.Error("terminal run was rebound")
	}
	_, first := offloadRun(t, s, "first", testOffloadFingerprint)
	if err := s.BindOffloadSession(testContext, first.ID, first.NativeSessionID, "shared"); err != nil {
		t.Fatal(err)
	}
	_, second := offloadRun(t, s, "second", testOffloadFingerprint)
	if err := s.BindOffloadSession(testContext, second.ID, second.NativeSessionID, "shared"); err == nil {
		t.Error("one Offload ID was bound to two sessions")
	}
}

func TestMemoryPluginDisabledRunIsResumableAndLegacyPendingIsNot(t *testing.T) {
	s, _ := testStore(t)
	_, disabled := offloadRun(t, s, "off", testOffloadFingerprint)
	if disabled.AutomaticMemorySkipReason() != MemoryPluginDisabledReason {
		t.Fatalf("reason %q", disabled.AutomaticMemorySkipReason())
	}
	if err := s.CompleteRun(testContext, disabled.ID, "completed", "answer", "m", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(testContext, disabled.ID)
	if err != nil || got.MemoryStatus != "skipped" || got.ExperienceStatus != "skipped" || !strings.Contains(string(got.Decision), "memory-plugin-disabled") {
		t.Fatalf("disabled completion: %+v %v", got, err)
	}
	if err := s.SessionCanResume(testContext, disabled.SessionID); err != nil {
		t.Fatalf("disabled session not resumable: %v", err)
	}

	m := channelInput("legacy")
	m.Binding = "binding-legacy"
	enqueueChannel(t, s, m)
	claimChannel(t, s, m.Profile)
	legacy := channelRun(t, s, m)
	if legacy.MemoryPluginDisabled {
		t.Fatal("legacy run gained the disabled marker")
	}
	if err := s.CreateChannelRun(testContext, legacy, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, legacy.ID, "completed", "answer", "m", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SessionCanResume(testContext, legacy.SessionID); err == nil {
		t.Fatal("legacy pending-memory run became resumable")
	}
	pending, err := s.PendingMemory(testContext)
	if err != nil || len(pending) != 1 || pending[0].ID != legacy.ID {
		t.Fatalf("pending memory: %+v %v", pending, err)
	}
}

func TestOffloadRoomBindingRequiresLatestSealedSameRoom(t *testing.T) {
	for _, kind := range []string{"valid", "newer-receipt", "different-room"} {
		t.Run(kind, func(t *testing.T) {
			st, _ := testStore(t)
			seed := func(id string) Session {
				return Session{ID: id, Scope: "/scope", Model: "m", NativeSessionID: OffloadPendingPrefix + id, RuntimeFingerprint: testOffloadFingerprint}
			}
			input := func(id, key string) RoomInput {
				return RoomInput{Key: key, RequestID: id, SenderID: "local-owner", Text: id}
			}
			create := func(s Session, id string) Run {
				r := Run{ID: id, SessionID: s.ID, Agent: "offload", Scope: s.Scope, Input: s.Room.Text, NativeSessionID: s.NativeSessionID, RuntimeFingerprint: s.RuntimeFingerprint, RequestedModel: s.Model, MemoryPluginDisabled: true}
				if err := st.CreateRun(testContext, r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			parent, _, err := st.EnterOffloadRoom(testContext, seed("parent"), input("one", "room"))
			if err != nil {
				t.Fatal(err)
			}
			first := create(parent, "run-parent")
			if err = st.BindOffloadSession(testContext, first.ID, first.NativeSessionID, "native-room"); err != nil {
				t.Fatal(err)
			}
			if err = st.CompleteRun(testContext, first.ID, "completed", "answer", "m", ""); err != nil {
				t.Fatal(err)
			}
			e := event.Event{Type: "run.finished", RunID: first.ID, SessionID: parent.ID, At: time.Now().UTC()}
			if err = st.AppendEvent(testContext, &e); err != nil {
				t.Fatal(err)
			}
			key := "room"
			if kind == "different-room" {
				key = "other"
			}
			child, _, err := st.EnterOffloadRoom(testContext, seed("child"), input("two", key))
			if err != nil {
				t.Fatal(err)
			}
			second := create(child, "run-child")
			if kind == "newer-receipt" {
				if _, _, err = st.EnterOffloadRoom(testContext, seed("newer"), input("three", "room")); err != nil {
					t.Fatal(err)
				}
			}
			err = st.BindOffloadSession(testContext, second.ID, second.NativeSessionID, "native-room")
			if kind == "valid" && err != nil {
				t.Fatal(err)
			}
			if kind != "valid" && err == nil {
				t.Fatal("unsafe room native sharing accepted")
			}
		})
	}
}
