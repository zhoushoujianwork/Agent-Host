package channelprogress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func fixture(t *testing.T) (*state.Store, Binding) {
	t.Helper()
	s, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	session := state.Session{ID: "s", Scope: "/fixture", NativeSessionID: "native"}
	if err = s.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	m := state.ChannelMessage{ID: "receipt", Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "provider", ConversationID: "conversation", SenderID: "owner", Profile: "p", Binding: "b", Scope: session.Scope, MessageType: "text", Text: "work", ReplyExpiresAt: time.Now().Add(time.Hour)}
	if _, err = s.EnqueueChannelMessage(t.Context(), m, "https://secret.invalid/?token=credential"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNextChannelMessage(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindChannelSession(t.Context(), "b", session.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateChannelRun(t.Context(), state.Run{ID: "r", SessionID: session.ID, Scope: session.Scope, NativeSessionID: session.NativeSessionID, Input: m.Text}, m.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableChannelProgress(t.Context(), "r"); err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(s.Home(), m.ID, "r")
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}
func TestMCPBoundedPendingAndSameAttempt(t *testing.T) {
	s, b := fixture(t)
	raw := json.RawMessage(`{"text":"  working  ","idempotency_key":"one"}`)
	p, err := send(t.Context(), s, b.Receipt, "send_progress", raw, time.Millisecond)
	if err != nil || p.Status != "pending" || p.Text != "working" {
		t.Fatal(p, err)
	}
	got, _, err := claimProgress(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishChannelReaction(t.Context(), got.ReactionID, "unknown", "test unknown"); err != nil {
		t.Fatal(err)
	}
	next, err := send(t.Context(), s, b.Receipt, "send_progress", raw, time.Millisecond)
	if err != nil || next.ID != p.ID || next.Status != "unknown" {
		t.Fatal(next, err)
	}
	encoded, _ := json.Marshal(next)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "https:") {
		t.Fatal("capability leaked", string(encoded))
	}
	for _, args := range []string{`{"text":"x","destination":"other"}`, `{"text":"x","run_id":"other"}`, `{"text":""}`} {
		if _, err = send(t.Context(), s, b.Receipt, "send_progress", json.RawMessage(args), time.Millisecond); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
	if publicError(errors.New("https://secret.invalid/?token=credential")).Error() != "channel progress reservation unavailable" {
		t.Fatal("raw error escaped")
	}
}
func TestMCPServerRequiresCurrentRunFingerprint(t *testing.T) {
	s, b := fixture(t)
	b.Receipt.Fingerprint = "stale"
	if err := Serve(context.Background(), b, strings.NewReader(""), &strings.Builder{}); err == nil {
		t.Fatal("stale launch accepted")
	}
	if err := s.RevokeChannelProgress(t.Context(), "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(s.Home(), "receipt", "r"); err == nil {
		t.Fatal("revoked launch accepted")
	}
}

func TestMCPWaitsForTerminalDeliveryBeforeReturning(t *testing.T) {
	s, b := fixture(t)
	worker := make(chan error, 1)
	go func() {
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			p, _, err := claimProgress(t.Context(), s)
			if err == nil {
				worker <- s.FinishChannelReaction(t.Context(), p.ReactionID, "accepted", "")
				return
			}
			if !errors.Is(err, sql.ErrNoRows) {
				worker <- err
				return
			}
			select {
			case <-deadline.C:
				worker <- errors.New("reservation not observed")
				return
			case <-ticker.C:
			}
		}
	}()
	p, err := send(t.Context(), s, b.Receipt, "send_progress", json.RawMessage(`{"text":"Milestone","idempotency_key":"milestone"}`), time.Second)
	workerErr := <-worker
	if err != nil || workerErr != nil || p.Status != "accepted" {
		t.Fatal("returned before normal delivery finished", p, err, workerErr)
	}
}

func claimProgress(ctx context.Context, s *state.Store) (state.ChannelProgress, state.ChannelMessage, error) {
	for {
		r, err := s.ClaimNextChannelReaction(ctx, "p")
		if err != nil {
			return state.ChannelProgress{}, state.ChannelMessage{}, err
		}
		if !strings.HasPrefix(r.Stage, "progress:") {
			if err = s.SkipChannelReaction(ctx, r.ID, "test lifecycle omission"); err != nil {
				return state.ChannelProgress{}, state.ChannelMessage{}, err
			}
			continue
		}
		if err = s.AuthorizeChannelReaction(ctx, r.ID); err != nil {
			return state.ChannelProgress{}, state.ChannelMessage{}, err
		}
		var id int64
		if _, err = fmt.Sscanf(r.Stage, "progress:%d", &id); err != nil {
			return state.ChannelProgress{}, state.ChannelMessage{}, err
		}
		p, err := s.ChannelProgress(ctx, id)
		return p, state.ChannelMessage{}, err
	}
}
