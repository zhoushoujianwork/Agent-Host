package cli

import (
	"testing"

	"agent-host/internal/host"
	"agent-host/internal/state"
)

func TestWebRuntimeRenewalIsOwnedAndBeforeDispatch(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := &host.Host{Store: store, Config: host.Config{AgentID: "default", AudienceID: "owner", Scope: t.TempDir(), Model: "native", RuntimeFingerprint: "old"}}
	previous, err := h.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	o := options{agentID: "default", audienceID: "owner", model: "native", runtimeFingerprint: "new"}
	h.Config.RuntimeFingerprint = "new"
	if _, err := h.Session(t.Context(), previous.ID); err == nil {
		t.Fatal("ordinary explicit resume unexpectedly changed behavior")
	}
	next, err := renewChatRuntime(t.Context(), h, previous, &o)
	if err != nil || next.ID == previous.ID || next.NativeSessionID == previous.NativeSessionID || o.renewedFrom != previous.ID {
		t.Fatalf("renewal=%+v err=%v", next, err)
	}
	if _, err := store.LatestSessionRun(t.Context(), next.ID); err == nil {
		t.Fatal("renewal dispatched a task")
	}
	o.renewedFrom = ""
	unchanged, err := renewChatRuntime(t.Context(), h, next, &o)
	if err != nil || unchanged.ID != next.ID || o.renewedFrom != "" {
		t.Fatal("compatible session did not continue")
	}
	o.agentID = "other"
	if _, err := renewChatRuntime(t.Context(), h, previous, &o); err == nil {
		t.Fatal("renewal crossed Agent ownership")
	}
}
