package owneranswering

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOutcomeReadyDeliversBeforeHandleReturnsAndIgnoresLaterCloseoutError(t *testing.T) {
	f := setup(t)
	var sentWhenReturning int
	var secondErr error
	f.c.Handle = func(ctx context.Context, q Question) (Outcome, error) {
		f.handled++
		ready := OutcomeReady(ctx)
		if ready == nil {
			t.Fatal("coordinator did not offer the request-scoped callback")
		}
		if err := ready(Outcome{Outcome: "answer", Text: "Early verified result", RunID: "early-run"}, nil); err != nil {
			t.Fatal(err)
		}
		sentWhenReturning = f.sent
		secondErr = ready(Outcome{Outcome: "answer", Text: "Different answer"}, nil)
		// Host closeout fails after the early outcome was handled.
		return Outcome{}, errors.New("host closeout failed")
	}
	f.consume(t)
	_, q := f.question(t)
	if sentWhenReturning != 1 || f.sent != 1 || f.handled != 1 || f.notified != 0 {
		t.Fatalf("peer delivery must happen once before Handle returns: sent-at-return=%d sent=%d handled=%d alerts=%d", sentWhenReturning, f.sent, f.handled, f.notified)
	}
	if secondErr == nil {
		t.Fatal("a second outcome was accepted")
	}
	if q.Status != "replied" || q.RunID != "early-run" || q.HandlerOutcome.Text != "Early verified result" || !strings.Contains(q.Answer, "Early verified result") || strings.Contains(q.Warning, "closeout") {
		t.Fatalf("later closeout error changed peer delivery or state: %+v", q)
	}
}

func TestOutcomeReadyKeepsBlockedAndInvalidOutcomeSafeguards(t *testing.T) {
	long := strings.Repeat("x", 15001)
	for name, tc := range map[string]struct {
		outcome Outcome
		err     error
	}{
		"blocked":    {Outcome{Outcome: "blocked", Reason: "needs production access", Clarification: "Approve?"}, nil},
		"empty":      {Outcome{Outcome: "answer", Text: "  "}, nil},
		"oversized":  {Outcome{Outcome: "answer", Text: long}, nil},
		"host error": {Outcome{Outcome: "answer", Text: "Looks fine"}, errors.New("native failed")},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.c.Handle = func(ctx context.Context, q Question) (Outcome, error) {
				f.handled++
				if err := OutcomeReady(ctx)(tc.outcome, tc.err); err != nil {
					t.Fatal(err)
				}
				return Outcome{Outcome: "answer", Text: "Post-closeout answer must be ignored"}, nil
			}
			f.consume(t)
			_, q := f.question(t)
			if f.sent != 0 || f.handled != 1 || f.notified != 1 || q.Status == "replied" || q.Peer.Status != "" || strings.Contains(q.Answer, "Post-closeout") {
				t.Fatalf("unsafe outcome reached the peer: sent=%d alerts=%d question=%+v", f.sent, f.notified, q)
			}
		})
	}
}

func TestOutcomeReadyIsUnavailableOutsideHandle(t *testing.T) {
	if OutcomeReady(context.Background()) != nil {
		t.Fatal("callback leaked into an unrelated context")
	}
}
