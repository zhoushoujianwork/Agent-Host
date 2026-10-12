package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testReplyURL = "https://example.invalid/reply?token=private-capability"

func channelInput(id string) ChannelMessage {
	return ChannelMessage{
		ID: id, Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "native-" + id,
		ConversationID: "dm-owner", SenderID: "owner", Profile: "profile", Binding: "binding", Scope: "/workspace",
		MessageType: "text", Text: "Remember the project uses Go.", ReceivedAt: time.Now().UTC(), ReplyExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
}

func enqueueChannel(t *testing.T, s *Store, m ChannelMessage) {
	t.Helper()
	if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL); err != nil || !inserted {
		t.Fatalf("enqueue: inserted=%v err=%v", inserted, err)
	}
}

func claimChannel(t *testing.T, s *Store, profile string) ChannelMessage {
	t.Helper()
	m, err := s.ClaimNextChannelMessage(testContext, profile)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func channelRun(t *testing.T, s *Store, m ChannelMessage) Run {
	t.Helper()
	session := Session{ID: "session-" + m.ID, Scope: m.Scope, Model: "test", NativeSessionID: "native-session-" + m.ID}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, m.Binding, session.ID); err != nil {
		t.Fatal(err)
	}
	return Run{ID: "run-" + m.ID, SessionID: session.ID, Scope: m.Scope, Input: m.Text, NativeSessionID: session.NativeSessionID, RequestedModel: "test"}
}

func TestChannelLifecyclePreservesExecutionAndSource(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("m1")
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	r := channelRun(t, s, m)
	// A supplied source cannot replace the durable receipt's identity.
	r.Source = &RunSource{SenderID: "forged"}
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Run(testContext, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := &RunSource{AgentID: DefaultAgentID, AudienceID: OwnerAudienceID, Channel: m.Channel, ChannelMessageID: m.ID, AccountID: m.AccountID, TenantID: m.TenantID, MessageID: m.MessageID, ConversationID: m.ConversationID, SenderID: m.SenderID}
	if !reflect.DeepEqual(saved.Source, want) || saved.AcceptanceStatus != "unverified" {
		t.Fatalf("source/acceptance: %+v", saved)
	}
	if err := s.CompleteRun(testContext, r.ID, "completed", "answer", "model", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MemoryFailure(testContext, r.ID, "decision unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "failed", "memory finalization failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "answer; memory failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || got.Status != "failed" || got.ReplyStatus != "accepted" || got.RunID != r.ID || got.SessionID != r.SessionID || got.ReplyText != "answer; memory failed" {
		t.Fatalf("receipt: %+v %v", got, err)
	}
	saved, err = s.Run(testContext, r.ID)
	if err != nil || saved.ExecutionStatus != "completed" || saved.MemoryStatus != "failed" || saved.AcceptanceStatus != "unverified" || !reflect.DeepEqual(saved.Source, want) {
		t.Fatalf("independent run outcome: %+v %v", saved, err)
	}
	if err := s.CreateRun(testContext, r); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("ordinary run accepted channel provenance: %v", err)
	}
}

func TestChannelDuplicatePreservesOriginalAndRejectsCollisions(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("m1")
	enqueueChannel(t, s, m)
	for _, stage := range []string{"queued", "claimed", "completed"} {
		if stage == "claimed" {
			claimChannel(t, s, m.Profile)
		}
		if stage == "completed" {
			if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
		}
		duplicate := m
		duplicate.Profile, duplicate.Binding, duplicate.Scope = "other-policy", "other-binding", "/other-workspace"
		duplicate.ReplyExpiresAt = m.ReplyExpiresAt.Add(time.Hour)
		if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, "https://example.invalid/replacement"); err != nil || inserted {
			t.Fatalf("%s duplicate: inserted=%v err=%v", stage, inserted, err)
		}
		got, err := s.ChannelMessage(testContext, m.ID)
		if err != nil || got.Status != stage || got.Profile != m.Profile || got.Binding != m.Binding || got.Scope != m.Scope || !got.ReplyExpiresAt.Equal(m.ReplyExpiresAt) {
			t.Fatalf("%s was overwritten: %+v %v", stage, got, err)
		}
		url, err := s.ChannelReplyURL(testContext, m.ID)
		if err != nil || url != testReplyURL {
			t.Fatalf("route overwritten: %q %v", url, err)
		}
	}
	for name, change := range map[string]func(*ChannelMessage){
		"id":           func(m *ChannelMessage) { m.ID = "other" },
		"channel":      func(m *ChannelMessage) { m.Channel = "other" },
		"account":      func(m *ChannelMessage) { m.AccountID = "other" },
		"tenant":       func(m *ChannelMessage) { m.TenantID = "other" },
		"message":      func(m *ChannelMessage) { m.MessageID = "other" },
		"conversation": func(m *ChannelMessage) { m.ConversationID = "other" },
		"sender":       func(m *ChannelMessage) { m.SenderID = "other" },
		"text":         func(m *ChannelMessage) { m.Text = "other" },
		"type":         func(m *ChannelMessage) { m.MessageType = "image" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := m
			change(&bad)
			if inserted, err := s.EnqueueChannelMessage(testContext, bad, testReplyURL); err == nil || inserted {
				t.Fatalf("collision accepted: inserted=%v err=%v", inserted, err)
			}
		})
	}
}

func TestLegacyRejectedRichTextRedeliveryPreservesOriginalRecord(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("legacy-rich")
	m.MessageType, m.Text = "richText", ""
	enqueueChannel(t, s, m)
	claimChannel(t, s, m.Profile)
	if err := s.FinishChannelMessage(testContext, m.ID, "failed", "unsupported message type"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "Only text messages are supported."); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", m.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	duplicate := m
	duplicate.Text = "/project alpha"
	duplicate.Profile, duplicate.Binding, duplicate.Scope = "new-profile", "new-binding", "/new-workspace"
	duplicate.ReplyExpiresAt = m.ReplyExpiresAt.Add(time.Hour)
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, "https://example.invalid/replacement"); err != nil || inserted {
		t.Fatalf("legacy richText redelivery collided or queued: inserted=%v err=%v", inserted, err)
	}
	for name, change := range map[string]func(*ChannelMessage){
		"id":           func(m *ChannelMessage) { m.ID = "other" },
		"channel":      func(m *ChannelMessage) { m.Channel = "other" },
		"account":      func(m *ChannelMessage) { m.AccountID = "other" },
		"tenant":       func(m *ChannelMessage) { m.TenantID = "other" },
		"message":      func(m *ChannelMessage) { m.MessageID = "other" },
		"conversation": func(m *ChannelMessage) { m.ConversationID = "other" },
		"sender":       func(m *ChannelMessage) { m.SenderID = "other" },
		"type":         func(m *ChannelMessage) { m.MessageType = "text" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := duplicate
			change(&bad)
			if inserted, err := s.EnqueueChannelMessage(testContext, bad, testReplyURL); err == nil || inserted {
				t.Fatalf("legacy richText identity collision accepted: inserted=%v err=%v", inserted, err)
			}
		})
	}
	var after []byte
	if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", m.ID).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatalf("legacy record changed during redelivery: %v", err)
	}
	url, err := s.ChannelReplyURL(testContext, m.ID)
	if err != nil || url != testReplyURL {
		t.Fatalf("legacy reply capability changed: %v", err)
	}
	if _, err := s.ClaimNextChannelMessage(testContext, duplicate.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy rejected input replayed under new profile: %v", err)
	}
	reactions, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(reactions) != 0 {
		t.Fatalf("legacy rejected input acquired reactions: %+v %v", reactions, err)
	}
}

func TestRichTextContentCollisionExceptionRequiresLegacyRejection(t *testing.T) {
	for _, tc := range []struct {
		name, kind, text, status, cause string
	}{
		{name: "queued", kind: "richText"},
		{name: "completed", kind: "richText", status: "completed"},
		{name: "other failure", kind: "richText", status: "failed", cause: "invalid text length"},
		{name: "previous normalized text", kind: "richText", text: "/project beta", status: "failed", cause: "unsupported message type"},
		{name: "other type", kind: "picture", status: "failed", cause: "unsupported message type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("collision-rich")
			m.MessageType, m.Text = tc.kind, tc.text
			enqueueChannel(t, s, m)
			if tc.status != "" {
				m = claimChannel(t, s, m.Profile)
				if err := s.FinishChannelMessage(testContext, m.ID, tc.status, tc.cause); err != nil {
					t.Fatal(err)
				}
			}
			m.Text = "/project alpha"
			if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL); err == nil || inserted {
				t.Fatalf("nonlegacy content collision accepted: inserted=%v err=%v", inserted, err)
			}
		})
	}
}

func TestChannelUnsupportedEmptyContentCanBeRejectedWithoutExecution(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("picture")
	m.MessageType, m.Text = "picture", ""
	enqueueChannel(t, s, m)
	claimChannel(t, s, m.Profile)
	if err := s.FinishChannelMessage(testContext, m.ID, "failed", "only text messages are supported"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "Only text messages are supported."); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || got.RunID != "" || got.SessionID != "" || got.Status != "failed" {
		t.Fatalf("unsupported message receipt: %+v %v", got, err)
	}
	for name, change := range map[string]func(*ChannelMessage){
		"blank text":     func(m *ChannelMessage) { m.Text = " \n\t" },
		"oversized text": func(m *ChannelMessage) { m.Text = strings.Repeat("x", 16*1024+1) },
		"oversized unsupported": func(m *ChannelMessage) {
			m.MessageType = "picture"
			m.Text = strings.Repeat("x", 16*1024+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := channelInput("invalid")
			change(&bad)
			if inserted, err := s.EnqueueChannelMessage(testContext, bad, testReplyURL); err == nil || inserted {
				t.Fatalf("invalid content accepted: inserted=%v err=%v", inserted, err)
			}
		})
	}
}

func TestChannelRunLinkIsAtomicAndValidated(t *testing.T) {
	for name, change := range map[string]func(*Run){
		"scope":          func(r *Run) { r.Scope = "/other" },
		"session":        func(r *Run) { r.SessionID = "absent" },
		"native session": func(r *Run) { r.NativeSessionID = "other" },
		"text":           func(r *Run) { r.Input = "other" },
		"id":             func(r *Run) { r.ID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("m1")
			enqueueChannel(t, s, m)
			claimChannel(t, s, m.Profile)
			r := channelRun(t, s, m)
			change(&r)
			if err := s.CreateChannelRun(testContext, r, m.ID); err == nil {
				t.Fatal("invalid run linked")
			}
			got, err := s.ChannelMessage(testContext, m.ID)
			if err != nil || got.RunID != "" || got.SessionID != "" || got.Status != "claimed" {
				t.Fatalf("partial linkage: %+v %v", got, err)
			}
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM runs").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial run: count=%d err=%v", count, err)
			}
		})
	}
	s, _ := testStore(t)
	m := channelInput("m1")
	enqueueChannel(t, s, m)
	r := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, r, m.ID); err == nil {
		t.Fatal("unclaimed run accepted")
	}
	claimChannel(t, s, m.Profile)
	if _, err := s.db.Exec("CREATE TRIGGER fail_channel_link BEFORE UPDATE ON channel_messages BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateChannelRun(testContext, r, m.ID); err == nil {
		t.Fatal("injected write failure ignored")
	}
	if _, err := s.Run(testContext, r.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("run escaped failed transaction: %v", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_channel_link"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	r.ID = "second-run"
	if err := s.CreateChannelRun(testContext, r, m.ID); err == nil {
		t.Fatal("message received a second native run")
	}
}

func TestChannelRecoveryNeverReplaysClaimsOrReplies(t *testing.T) {
	s, home := testStore(t)
	for i, id := range []string{"claimed", "running", "sending", "terminal", "queued"} {
		m := channelInput(id)
		m.Binding += id
		m.ReceivedAt = time.Now().Add(time.Duration(i) * time.Second)
		enqueueChannel(t, s, m)
		if id == "queued" {
			continue
		}
		claimChannel(t, s, m.Profile)
		if id == "running" {
			r := channelRun(t, s, m)
			if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
				t.Fatal(err)
			}
		}
		if id == "sending" || id == "terminal" {
			if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
		}
		if id == "sending" {
			if err := s.PrepareChannelReply(testContext, m.ID, "reply"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A fresh store handle models a new owning process after the old one exits.
	restarted, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.Recover(testContext); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claimed", "running"} {
		m, err := restarted.ChannelMessage(testContext, id)
		if err != nil || m.Status != "unknown" || m.Error == "" {
			t.Fatalf("recovered claim: %+v %v", m, err)
		}
	}
	r, err := restarted.Run(testContext, "run-running")
	if err != nil || r.ExecutionStatus != "unknown" {
		t.Fatalf("recovered native execution: %+v %v", r, err)
	}
	sending, err := restarted.ChannelMessage(testContext, "sending")
	if err != nil || sending.Status != "completed" || sending.ReplyStatus != "unknown" || sending.ReplyText != "reply" || sending.ReplyError == "" {
		t.Fatalf("recovered delivery: %+v %v", sending, err)
	}
	terminal, err := restarted.ChannelMessage(testContext, "terminal")
	if err != nil || terminal.Status != "completed" || terminal.ReplyStatus != "pending" {
		t.Fatalf("terminal input changed: %+v %v", terminal, err)
	}
	claimed := claimChannel(t, restarted, "profile")
	if claimed.ID != "queued" {
		t.Fatalf("replayed nonqueued input: %+v", claimed)
	}
	if _, err := restarted.ClaimNextChannelMessage(testContext, "profile"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("queue should be empty: %v", err)
	}
	if err := restarted.PrepareChannelReply(testContext, "sending", "duplicate"); err == nil {
		t.Fatal("uncertain reply replayed")
	}
}

func TestChannelQueueProfileIsolationAndConcurrentClaim(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("m1")
	enqueueChannel(t, s, m)
	if _, err := s.ClaimNextChannelMessage(testContext, "different-profile"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-profile claim: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ClaimNextChannelMessage(testContext, m.Profile)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("claim successes=%d", successes)
	}
}

func TestChannelReplyTransitionsAndPrivateRoute(t *testing.T) {
	s, home := testStore(t)
	m := channelInput("m1")
	enqueueChannel(t, s, m)
	if err := s.PrepareChannelReply(testContext, m.ID, "too early"); err == nil {
		t.Fatal("queued reply accepted")
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err == nil {
		t.Fatal("unclaimed finish accepted")
	}
	claimChannel(t, s, m.Profile)
	for _, status := range []string{"queued", "claimed", "unknown", "accepted"} {
		if err := s.FinishChannelMessage(testContext, m.ID, status, "error"); err == nil {
			t.Fatalf("invalid status %s accepted", status)
		}
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "failed", ""); err == nil {
		t.Fatal("failure without explanation accepted")
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "failed", "overwrite"); err == nil {
		t.Fatal("terminal outcome overwritten")
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err == nil {
		t.Fatal("unprepared delivery accepted")
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "replacement"); err == nil {
		t.Fatal("prepared reply overwritten")
	}
	if err := s.FinishChannelReply(testContext, m.ID, "failed", "transport rejection"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err == nil {
		t.Fatal("terminal delivery overwritten")
	}
	ro, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	messages, err := ro.ChannelMessages(testContext, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("inspect messages: %+v %v", messages, err)
	}
	data, err := json.Marshal(messages)
	if err != nil || strings.Contains(string(data), "private-capability") || strings.Contains(string(data), "reply_url") || strings.Contains(string(data), "example.invalid") {
		t.Fatalf("private route exposed: %s %v", data, err)
	}
	var payload string
	if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", m.ID).Scan(&payload); err != nil || strings.Contains(payload, "private-capability") {
		t.Fatalf("private route stored in payload: %s %v", payload, err)
	}
}

func TestChannelBindingExplicitReplacement(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.ChannelSession(testContext, "binding"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing binding: %v", err)
	}
	if err := s.BindChannelSession(testContext, "binding", "absent"); err == nil {
		t.Fatal("nonexistent session bound")
	}
	for _, id := range []string{"old", "new"} {
		if err := s.CreateSession(testContext, Session{ID: id, Scope: "/workspace", NativeSessionID: "native-" + id}); err != nil {
			t.Fatal(err)
		}
		if err := s.BindChannelSession(testContext, "binding", id); err != nil {
			t.Fatal(err)
		}
		got, err := s.ChannelSession(testContext, "binding")
		if err != nil || got != id {
			t.Fatalf("binding: %q %v", got, err)
		}
	}
	if _, err := s.Session(testContext, "old"); err != nil {
		t.Fatalf("previous session discarded: %v", err)
	}
}

func TestSchemaV3MigratesChannelTablesWithoutChangingHistory(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + schemaV2 + schemaV3); err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db}
	r := addRun(t, old, "old", "/workspace", "keep exact source", true)
	before, err := old.Run(testContext, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if ro, err := Open(home, true); err == nil {
		ro.Close()
		t.Fatal("old schema opened without migration")
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Run(testContext, r.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed history: before=%+v after=%+v err=%v", before, after, err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	enqueueChannel(t, s, channelInput("new"))
	if _, err := s.ClaimNextChannelMessage(context.Background(), "profile"); err != nil {
		t.Fatal(err)
	}
}
