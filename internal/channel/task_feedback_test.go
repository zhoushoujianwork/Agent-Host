package channel

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveredFeedbackRetriesOnlyConfirmedFailure(t *testing.T) {
	f := newServiceFixture(t)
	f.replier.reply = func(context.Context, replyCall) (string, error) {
		return "failed", errors.New("confirmed platform rejection")
	}
	m := f.process(t, f.message(t, "feedback", "Check service health", "text", "owner-dm"))
	run := f.run(t, m)
	if m.ReplyStatus != "failed" {
		t.Fatal(m.ReplyStatus)
	}
	before := len(f.runner.snapshot())
	f.replier.reply = nil
	if err := f.service.DeliverRecoveredFeedback(t.Context(), m, run); err != nil {
		t.Fatal(err)
	}
	saved := f.receipt(t, m.MessageID)
	if saved.ReplyStatus != "accepted" || saved.ReplyText != m.ReplyText || len(f.runner.snapshot()) != before {
		t.Fatal("notification recovery replayed business or changed body", saved.ReplyStatus)
	}
	if err := f.service.DeliverRecoveredFeedback(t.Context(), saved, run); err != nil {
		t.Fatal(err)
	}
	if len(f.replier.snapshot()) != 2 {
		t.Fatal("accepted reply sent again")
	}
}

func TestRecoveredFeedbackDoesNotReplayUnknownSend(t *testing.T) {
	f := newServiceFixture(t)
	f.replier.reply = func(context.Context, replyCall) (string, error) { return "unknown", errors.New("acknowledgement lost") }
	m := f.process(t, f.message(t, "unknown-feedback", "Check service health", "text", "owner-dm"))
	run := f.run(t, m)
	if err := f.service.DeliverRecoveredFeedback(t.Context(), m, run); err != nil {
		t.Fatal(err)
	}
	if len(f.replier.snapshot()) != 1 || len(f.runner.snapshot()) != 1 {
		t.Fatal("unknown send or task replayed")
	}
}
