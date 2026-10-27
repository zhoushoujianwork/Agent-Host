package channel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecoveryNoticeDeliveryEvidence(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			f := newServiceFixture(t)
			input := f.message(t, "notice", "Check work", "text", "dm")
			f.receive(t, input)
			m := f.next(t)
			if err := f.service.Store.RecoverChannels(t.Context()); err != nil {
				t.Fatal(err)
			}
			if outcome == "interrupted" {
				if err := f.service.Store.PrepareChannelReply(t.Context(), m.ID, "recovery notice"); err != nil {
					t.Fatal(err)
				}
			}
			f.replier.reply = func(_ context.Context, call replyCall) (string, error) {
				original, err := f.service.Store.ChannelReplyURL(t.Context(), m.ID)
				if err != nil || call.url != original {
					t.Fatalf("destination changed: %v", err)
				}
				if outcome == "accepted" {
					return outcome, nil
				}
				return outcome, errors.New("fixture delivery uncertainty")
			}
			for i := 0; i < 2; i++ {
				if err := f.service.Store.RecoverChannels(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := f.service.deliverRecoveryNotices(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			got := f.receipt(t, input.ID)
			want, sends := outcome, 1
			if outcome == "interrupted" {
				want, sends = "unknown", 0
			}
			if got.Status != "unknown" || got.RunID != "" || got.ReplyKind != "recovery_notice" || got.ReplyStatus != want || len(f.runner.snapshot()) != 0 || len(f.replier.snapshot()) != sends {
				t.Fatalf("lost uncertainty or replayed: %+v", got)
			}
		})
	}
}

func TestRecoveryNoticeKeepsOriginalAccountAndExpires(t *testing.T) {
	f := newServiceFixture(t)
	input := f.message(t, "expired-notice", "Check work", "text", "dm")
	input.ReplyExpiresAt = time.Now().Add(-time.Minute)
	f.receive(t, input)
	f.next(t)
	if err := f.service.Store.RecoverChannels(t.Context()); err != nil {
		t.Fatal(err)
	}
	account := f.service.ClientID
	f.service.ClientID = "different-account"
	if err := f.service.deliverRecoveryNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.service.ClientID = account
	if got := f.receipt(t, input.ID); got.ReplyStatus != "pending" {
		t.Fatal("changed account claimed notice")
	}
	if err := f.service.deliverRecoveryNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := f.receipt(t, input.ID)
	if got.Status != "unknown" || got.ReplyStatus != "failed" || got.ReplyError == "" || len(f.replier.snapshot()) != 0 {
		t.Fatalf("expired destination used: %+v", got)
	}
}
