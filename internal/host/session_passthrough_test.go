package host

import (
	"context"
	"testing"
)

func TestHostPassesConversationIdentityAcrossTurns(t *testing.T) {
	h, runner := testHost(t)
	s := newSession(t, h)
	runOK(t, h, s, "first")
	resumed, err := h.Session(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	runOK(t, h, resumed, "second")
	if len(runner.requests) != 2 || runner.requests[0].HostSessionID != s.ID || runner.requests[1].HostSessionID != s.ID || !runner.requests[1].Resume {
		t.Fatalf("Host conversation identity was not preserved: %+v", runner.requests)
	}
	if runner.requests[0].HostSessionID == runner.requests[0].CallerRunID || runner.requests[0].HostSessionID == runner.requests[0].NativeSessionID {
		t.Fatal("Host conversation identity was replaced by a run or native identity")
	}
}
