package host

import (
	"context"
	"strings"
	"testing"

	"agent-host/internal/knowledge"
)

func TestReplyRequirementsReachEveryNativeTurnIncludingResume(t *testing.T) {
	h, runner := testHost(t)
	session := newSession(t, h)
	questions := []string{"Explain the login flow with one example.", "How should I check an upload?"}
	for _, question := range questions {
		runOK(t, h, session, question)
		var err error
		session, err = h.Session(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(runner.requests) != 2 || runner.requests[0].Resume || !runner.requests[1].Resume {
		t.Fatal("fixture did not exercise initial and resumed native requests")
	}
	if runner.requests[0].NativeSessionID != runner.requests[1].NativeSessionID {
		t.Fatal("reply guidance should not reset a native conversation on every turn")
	}
	for i, request := range runner.requests {
		if !strings.Contains(request.Prompt, "Host automatically screens user declarations") || !strings.Contains(request.Prompt, "Do not claim either that the current turn was saved or that it was not saved") {
			t.Fatalf("turn %d lost Host memory ownership and pending-publication guidance", i+1)
		}
		marker := strings.LastIndex(request.Prompt, "Host reply requirements for this turn")
		question := strings.LastIndex(request.Prompt, questions[i])
		if question < 0 || marker <= question || !strings.HasSuffix(request.Prompt, knowledge.SummaryInstructions+"\n"+knowledge.CitationInstructions) {
			t.Fatalf("turn %d lost its current-turn summary and citation requirements", i+1)
		}
		if request.Extraction || request.PermissionMode != h.Config.PermissionMode {
			t.Fatal("answer formatting changed the request purpose or permissions")
		}
	}
}
