package host

import (
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type takeoverRunner struct{ harness.Runner }

func (takeoverRunner) SupportsFork() bool { return true }

func TestTakeoverForksExactSessionAndNeverReplaysOrChangesAudience(t *testing.T) {
	h, runner := testHost(t)
	h.Runner = takeoverRunner{runner}
	seed, err := h.RoomSession(t.Context(), state.RoomInput{Key: "dingtalk-group", RequestID: "im-1", SenderID: "alice", Text: "remember context"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.TakeoverSession(t.Context(), seed, "web-busy", "continue"); err == nil {
		t.Fatal("unexecuted source accepted")
	}
	runOK(t, h, seed, "remember context")
	child, err := h.TakeoverSession(t.Context(), seed, "web-1", "continue")
	if err != nil {
		t.Fatal(err)
	}
	if child.Room.ParentSessionID != seed.ID || child.Room.ForkFromNativeSessionID != seed.NativeSessionID || child.Room.SenderID != "local-owner" {
		t.Fatalf("wrong takeover provenance: %+v", child)
	}
	runOK(t, h, child, "continue")
	request := runner.requests[len(runner.requests)-1]
	if request.Resume || request.ForkFromNativeSessionID != seed.NativeSessionID || request.NativeSessionID == seed.NativeSessionID {
		t.Fatal("original context was replaced or mutated", request)
	}
	duplicate, err := h.TakeoverSession(t.Context(), seed, "web-1", "continue")
	if err != nil || duplicate.ID != child.ID {
		t.Fatal("duplicate did not recover exact receipt", err)
	}
	if _, err := h.Run(t.Context(), duplicate, "continue"); err == nil {
		t.Fatal("duplicate replayed execution")
	}
	if _, err := h.TakeoverSession(t.Context(), seed, "web-1", "changed"); err == nil {
		t.Fatal("request ID accepted different content")
	}
	other := *h
	other.Config.AudienceID = "other-audience"
	if _, err := other.TakeoverSession(t.Context(), seed, "web-2", "continue"); err == nil {
		t.Fatal("audience isolation lost")
	}
	other = *h
	other.Config.Model = "different-model"
	if _, err := other.TakeoverSession(t.Context(), seed, "web-2", "continue"); err == nil {
		t.Fatal("model change silently discarded context")
	}
	grandchild, err := h.TakeoverSession(t.Context(), child, "web-2", "next")
	if err != nil || grandchild.Room.ParentSessionID != child.ID {
		t.Fatal("second turn lost Web continuation", err)
	}
	original, err := h.Store.Session(t.Context(), seed.ID)
	if err != nil || original.Room.SenderID != "alice" || len(original.Room.History) != 0 {
		t.Fatal("original channel evidence changed", err)
	}
}
