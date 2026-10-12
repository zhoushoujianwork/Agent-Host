package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func reactionClaim(t *testing.T, s *Store, profile, stage string) ChannelReaction {
	t.Helper()
	r, err := s.ClaimNextChannelReaction(testContext, profile)
	if err != nil || r.Stage != stage || r.Status != "sending" || r.ID <= 0 || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		t.Fatalf("claim stage %s: %+v err=%v", stage, r, err)
	}
	return r
}

func reactionFinish(t *testing.T, s *Store, r ChannelReaction, status string) {
	t.Helper()
	cause := ""
	if status != "accepted" {
		cause = "platform outcome " + status
	}
	if err := s.FinishChannelReaction(testContext, r.ID, status, cause); err != nil {
		t.Fatal(err)
	}
}

func reactionReply(t *testing.T, s *Store, m ChannelMessage, execution, delivery string) {
	t.Helper()
	cause := ""
	if execution == "failed" {
		cause = "host step failed"
	}
	if err := s.FinishChannelMessage(testContext, m.ID, execution, cause); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "exact final answer"); err != nil {
		t.Fatal(err)
	}
	cause = ""
	if delivery != "accepted" {
		cause = "reply outcome " + delivery
	}
	if err := s.FinishChannelReply(testContext, m.ID, delivery, cause); err != nil {
		t.Fatal(err)
	}
}

func TestChannelReactionsFollowDurableTransitionsAndOriginalProfile(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("ordered")
	enqueueChannel(t, s, m)
	duplicate := m
	duplicate.Profile = "new-profile"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, testReplyURL); err != nil || inserted {
		t.Fatalf("duplicate enqueue inserted=%v err=%v", inserted, err)
	}
	claimChannel(t, s, m.Profile)
	if _, err := s.ClaimNextChannelReaction(testContext, duplicate.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reaction changed profile: %v", err)
	}
	received := reactionClaim(t, s, m.Profile, "received")
	if received.Label != "已收到" {
		t.Fatal(received)
	}
	// A second worker cannot overtake an in-flight stage on the same message.
	if _, err := s.ClaimNextChannelReaction(testContext, m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("processing overtook receipt: %v", err)
	}
	reactionFinish(t, s, received, "accepted")
	processing := reactionClaim(t, s, m.Profile, "processing")
	if processing.Label != "处理中" || processing.ID <= received.ID {
		t.Fatal(processing)
	}
	reactionFinish(t, s, processing, "accepted")
	reactionReply(t, s, m, "completed", "accepted")
	completed := reactionClaim(t, s, m.Profile, "completed")
	if completed.Label != "已完成" || completed.ID <= processing.ID {
		t.Fatal(completed)
	}
	reactionFinish(t, s, completed, "accepted")
	if _, err := s.ClaimNextChannelReaction(testContext, m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("finished reaction was replayed: %v", err)
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || len(got.Reactions) != 4 || got.Reactions[2].Stage != "replying" || got.Reactions[2].Status != "skipped" || got.Status != "completed" || got.ReplyStatus != "accepted" {
		t.Fatalf("inspection: %+v %v", got, err)
	}
	listed, err := s.ChannelMessages(testContext, 10)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Reactions, got.Reactions) {
		t.Fatalf("list inspection: %+v %v", listed, err)
	}
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", m.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "reactions") || strings.Contains(string(payload), testReplyURL) {
		t.Fatal("message payload duplicated reaction audit or private capability")
	}
}

func TestChannelReactionCompletionRequiresHostAndDeliverySuccess(t *testing.T) {
	for _, scenario := range []struct{ host, delivery, stage, label string }{
		{"completed", "accepted", "completed", "已完成"},
		{"failed", "accepted", "failed", "打叉"},
		{"completed", "failed", "failed", "打叉"},
		{"completed", "unknown", "failed", "打叉"},
		{"failed", "unknown", "failed", "打叉"},
	} {
		t.Run(scenario.host+"/"+scenario.delivery, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("terminal")
			enqueueChannel(t, s, m)
			claimChannel(t, s, m.Profile)
			reactionReply(t, s, m, scenario.host, scenario.delivery)
			reactions, err := s.ChannelReactions(testContext, m.ID)
			if err != nil || len(reactions) != 4 || reactions[3].Stage != scenario.stage || reactions[3].Label != scenario.label || reactions[1].Status != "skipped" || reactions[2].Status != "skipped" {
				t.Fatalf("reactions: %+v %v", reactions, err)
			}
			got, err := s.ChannelMessage(testContext, m.ID)
			if err != nil || got.Status != scenario.host || got.ReplyStatus != scenario.delivery {
				t.Fatalf("reaction changed task truth: %+v %v", got, err)
			}
		})
	}
}

func TestChannelCommandsAndUnsupportedMessagesHaveNoReactionOutbox(t *testing.T) {
	for _, scenario := range []struct{ kind, text string }{
		{"text", " /new\n"}, {"image", ""}, {"richText", ""}, {"richText", " \n"},
		{"richText", "/new"}, {"richText", "/status"}, {"text", "/help"},
		{"richText", "/project alpha"}, {"richText", "/projects"}, {"text", "/unknown-command"},
	} {
		t.Run(scenario.kind, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("excluded")
			m.MessageType, m.Text = scenario.kind, scenario.text
			m.Reactions = []ChannelReaction{{ID: 99, Stage: "received", Status: "accepted", Active: true}}
			enqueueChannel(t, s, m)
			claimChannel(t, s, m.Profile)
			reactionReply(t, s, m, "completed", "accepted")
			if err := s.RecoverChannels(testContext); err != nil {
				t.Fatal(err)
			}
			got, err := s.ChannelMessage(testContext, m.ID)
			if err != nil || len(got.Reactions) != 0 {
				t.Fatalf("excluded input acquired reactions: %+v %v", got, err)
			}
		})
	}
}

func TestForwardedMarkdownReactionEligibilityRequiresParsedContent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		input *ChannelInput
		want  bool
	}{
		{name: "forwarded command", text: "Forwarded Markdown message", input: &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown", Body: "/new"}}, want: true},
		{name: "command alongside material", text: "/new", input: &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown", Body: "Forwarded text"}}, want: true},
		{name: "missing material", text: "Forwarded Markdown message"},
		{name: "missing quote", text: "Forwarded Markdown message", input: &ChannelInput{}},
		{name: "wrong quote type", text: "Forwarded Markdown message", input: &ChannelInput{Quote: &ChannelQuote{MessageType: "text", Body: "Quoted text"}}},
		{name: "empty body", text: "Forwarded Markdown message", input: &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown"}}},
		{name: "empty normalized text", input: &ChannelInput{Quote: &ChannelQuote{MessageType: "markdown", Body: "Forwarded text"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("markdown-reaction")
			m.MessageType, m.Text, m.Input = "markdown", tc.text, tc.input
			enqueueChannel(t, s, m)
			claimChannel(t, s, m.Profile)
			reactionReply(t, s, m, "completed", "accepted")
			reactions, err := s.ChannelReactions(testContext, m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want && (len(reactions) != 4 || reactions[0].Stage != "received" || reactions[3].Stage != "completed") || !tc.want && len(reactions) != 0 {
				t.Fatalf("unexpected markdown feedback: %+v", reactions)
			}
		})
	}
}

func TestChannelReactionOutcomesAndCleanupStayIndependent(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("one")
			enqueueChannel(t, s, m)
			r := reactionClaim(t, s, m.Profile, "received")
			reactionFinish(t, s, r, outcome)
			active, err := s.ActiveChannelReactions(testContext, m.ID)
			wantActive := 0
			if outcome == "accepted" {
				wantActive = 1
			}
			if err != nil || len(active) != wantActive {
				t.Fatalf("active markers: %+v %v", active, err)
			}
			if err := s.ClaimChannelReactionRemoval(testContext, r.ID); (err == nil) != (outcome == "accepted") {
				t.Fatalf("claim removal %s: %v", outcome, err)
			}
			if err := s.ClearChannelReaction(testContext, r.ID); (err == nil) != (outcome == "accepted") {
				t.Fatalf("clear %s: %v", outcome, err)
			}
			if err := s.ClearChannelReaction(testContext, r.ID); err == nil {
				t.Fatal("duplicate or unconfirmed cleanup succeeded")
			}
			if err := s.FinishChannelReaction(testContext, r.ID, "accepted", ""); err == nil {
				t.Fatal("reaction was finished twice")
			}
			got, err := s.ChannelMessage(testContext, m.ID)
			if err != nil || got.Status != "queued" || got.ReplyStatus != "pending" || got.Reactions[0].Status != outcome || got.Reactions[0].Active {
				t.Fatalf("reaction changed execution/delivery or lost audit: %+v %v", got, err)
			}
		})
	}
}

func TestChannelReactionRejectsInvalidStateTransitions(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("invalid")
	enqueueChannel(t, s, m)
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if err := s.FinishChannelReaction(testContext, rows[0].ID, "accepted", ""); err == nil {
		t.Fatal("unclaimed reaction finished")
	}
	r := reactionClaim(t, s, m.Profile, "received")
	for _, invalid := range []struct{ status, cause string }{{"ready", ""}, {"accepted", "unexpected error"}, {"failed", ""}, {"unknown", " "}} {
		if err := s.FinishChannelReaction(testContext, r.ID, invalid.status, invalid.cause); err == nil {
			t.Fatalf("accepted invalid finish %+v", invalid)
		}
	}
	if _, err := s.ClaimNextChannelReaction(testContext, ""); err == nil {
		t.Fatal("blank profile accepted")
	}
	reactionFinish(t, s, r, "accepted")
}

func TestChannelReactionRemovalHasOneDurableAttempt(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("cleanup")
			enqueueChannel(t, s, m)
			r := reactionClaim(t, s, m.Profile, "received")
			reactionFinish(t, s, r, "accepted")
			if err := s.ClearChannelReaction(testContext, r.ID); err == nil {
				t.Fatal("unclaimed removal was accepted")
			}
			if err := s.ClaimChannelReactionRemoval(testContext, r.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.ClaimChannelReactionRemoval(testContext, r.ID); err == nil {
				t.Fatal("in-flight removal claimed twice")
			}
			for _, invalid := range []struct{ status, cause string }{{"pending", ""}, {"accepted", "unexpected error"}, {"failed", ""}, {"unknown", " "}} {
				if err := s.FinishChannelReactionRemoval(testContext, r.ID, invalid.status, invalid.cause); err == nil {
					t.Fatalf("accepted invalid removal %+v", invalid)
				}
			}
			cause := ""
			if outcome != "accepted" {
				cause = "cleanup outcome " + outcome
			}
			if err := s.FinishChannelReactionRemoval(testContext, r.ID, outcome, cause); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.RecoverChannels(testContext); err != nil {
					t.Fatal(err)
				}
				if err := s.ClaimChannelReactionRemoval(testContext, r.ID); err == nil {
					t.Fatal("finished cleanup was retried")
				}
			}
			rows, err := s.ChannelReactions(testContext, m.ID)
			if err != nil || len(rows) != 1 || rows[0].RemovalStatus != outcome || rows[0].RemovalError != cause || rows[0].Status != "accepted" || rows[0].Active != (outcome != "accepted") {
				t.Fatalf("removal audit lost outcome: %+v %v", rows, err)
			}
		})
	}
}

func TestChannelReactionRemovalCrashDoesNotReplayOrClaimSuccess(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("cleanup-crash")
	enqueueChannel(t, s, m)
	received := reactionClaim(t, s, m.Profile, "received")
	reactionFinish(t, s, received, "accepted")
	claimChannel(t, s, m.Profile)
	processing := reactionClaim(t, s, m.Profile, "processing")
	if err := s.ClaimChannelReactionRemoval(testContext, received.ID); err != nil {
		t.Fatal(err)
	}
	// The platform may have removed the receipt, but the host crashed before
	// recording its response. Recovery cannot claim removal or repeat the call.
	if err := s.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 3 || rows[0].RemovalStatus != "unknown" || rows[0].RemovalError == "" || !rows[0].Active || rows[0].Status != "accepted" || rows[1].ID != processing.ID || rows[1].Status != "unknown" || rows[2].Stage != "failed" {
		t.Fatalf("crash state: %+v %v", rows, err)
	}
	if err := s.ClaimChannelReactionRemoval(testContext, received.ID); err == nil {
		t.Fatal("uncertain crash cleanup retried")
	}
	if err := s.ClearChannelReaction(testContext, received.ID); err == nil {
		t.Fatal("recovered unknown cleanup became accepted")
	}
	reactionClaim(t, s, m.Profile, "failed")
}

func TestChannelReactionStageInsertionRollsBackMessageTransition(t *testing.T) {
	for _, stage := range []string{"received", "processing", "completed"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("atomic")
			if stage != "received" {
				enqueueChannel(t, s, m)
			}
			if stage == "completed" {
				claimChannel(t, s, m.Profile)
				if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
				if err := s.PrepareChannelReply(testContext, m.ID, "answer"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec("CREATE TRIGGER reject_reaction BEFORE INSERT ON channel_reactions WHEN NEW.stage='" + stage + "' BEGIN SELECT RAISE(ABORT, 'reaction persistence failed'); END;"); err != nil {
				t.Fatal(err)
			}
			var err error
			switch stage {
			case "received":
				_, err = s.EnqueueChannelMessage(testContext, m, testReplyURL)
			case "processing":
				_, err = s.ClaimNextChannelMessage(testContext, m.Profile)
			case "completed":
				err = s.FinishChannelReply(testContext, m.ID, "accepted", "")
			}
			if err == nil {
				t.Fatal("reaction write failure was hidden")
			}
			got, readErr := s.ChannelMessage(testContext, m.ID)
			if stage == "received" {
				if !errors.Is(readErr, sql.ErrNoRows) {
					t.Fatalf("receipt survived failed atomic insert: %+v %v", got, readErr)
				}
			} else if readErr != nil || stage == "processing" && got.Status != "queued" || stage == "completed" && got.ReplyStatus != "sending" {
				t.Fatalf("message transition survived failed marker: %+v %v", got, readErr)
			}
		})
	}
}

func TestChannelReactionRecoveryNeverReplaysAttemptedStages(t *testing.T) {
	s, _ := testStore(t)
	queued, interrupted, delivered := channelInput("queued"), channelInput("interrupted"), channelInput("delivered")
	queued.Profile, interrupted.Profile, delivered.Profile = "queue", "interrupt", "done"
	for _, m := range []ChannelMessage{queued, interrupted, delivered} {
		enqueueChannel(t, s, m)
	}
	receipt := reactionClaim(t, s, interrupted.Profile, "received")
	claimChannel(t, s, interrupted.Profile)
	claimChannel(t, s, delivered.Profile)
	for _, stage := range []string{"received", "processing"} {
		r := reactionClaim(t, s, delivered.Profile, stage)
		reactionFinish(t, s, r, "accepted")
	}
	reactionReply(t, s, delivered, "completed", "accepted")
	terminal := reactionClaim(t, s, delivered.Profile, "completed")
	for i := 0; i < 2; i++ {
		if err := s.RecoverChannels(testContext); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ChannelReactions(testContext, interrupted.ID)
	if err != nil || len(rows) != 3 || rows[0].ID != receipt.ID || rows[0].Status != "unknown" || rows[1].Status != "skipped" || rows[2].Stage != "failed" || rows[2].Status != "ready" {
		t.Fatalf("interrupted recovery: %+v %v", rows, err)
	}
	rows, err = s.ChannelReactions(testContext, delivered.ID)
	if err != nil || len(rows) != 4 || rows[3].ID != terminal.ID || rows[3].Status != "unknown" || rows[2].Status != "skipped" || !rows[0].Active || !rows[1].Active {
		t.Fatalf("terminal replay or lost active history: %+v %v", rows, err)
	}
	if _, err := s.ClaimNextChannelReaction(testContext, delivered.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("begun terminal replayed: %v", err)
	}
	reactionClaim(t, s, queued.Profile, "received")
	reactionClaim(t, s, interrupted.Profile, "failed")
}

func TestChannelReactionRecoveryUsesDeliveryTruthAtCrash(t *testing.T) {
	for _, reply := range []string{"pending", "sending", "accepted", "failed", "unknown"} {
		t.Run(reply, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("crash")
			enqueueChannel(t, s, m)
			claimChannel(t, s, m.Profile)
			if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if reply != "pending" {
				if err := s.PrepareChannelReply(testContext, m.ID, "answer"); err != nil {
					t.Fatal(err)
				}
			}
			if reply != "pending" && reply != "sending" {
				cause := ""
				if reply != "accepted" {
					cause = "delivery " + reply
				}
				if err := s.FinishChannelReply(testContext, m.ID, reply, cause); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecoverChannels(testContext); err != nil {
				t.Fatal(err)
			}
			got, err := s.ChannelMessage(testContext, m.ID)
			stage := "failed"
			if reply == "accepted" {
				stage = "completed"
			}
			wantCount := 4
			if reply == "pending" {
				wantCount = 3
			}
			if err != nil || got.Status != "completed" || len(got.Reactions) != wantCount || got.Reactions[0].Status != "skipped" || got.Reactions[1].Status != "skipped" || got.Reactions[len(got.Reactions)-1].Stage != stage {
				t.Fatalf("crash recovery %+v err=%v", got, err)
			}
		})
	}
}

func TestSchemaV4ReactionMigrationPreservesHistoryWithoutRetroactiveMarkers(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + schemaV2 + schemaV3 + schemaV4); err != nil {
		t.Fatal(err)
	}
	m := channelInput("old-message")
	m.Status, m.ReplyStatus = "queued", "pending"
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channel_messages(id,channel,account_id,message_id,profile,status,received_at,reply_url,payload) VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.Channel, m.AccountID, m.MessageID, m.Profile, m.Status, m.ReceivedAt.Format(reactionTimeFormat), testReplyURL, payload); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if readonly, err := Open(home, true); err == nil {
		readonly.Close()
		t.Fatal("readonly accepted old schema without migration")
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var migrated []byte
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("schema %d %v", version, err)
	}
	if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", m.ID).Scan(&migrated); err != nil || string(migrated) != string(payload) {
		t.Fatal("migration changed historical payload", err)
	}
	if inserted, err := s.EnqueueChannelMessage(testContext, m, testReplyURL); err != nil || inserted {
		t.Fatal("historical duplicate", inserted, err)
	}
	claimChannel(t, s, m.Profile)
	reactionReply(t, s, m, "completed", "accepted")
	if err := s.RecoverChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || len(got.Reactions) != 0 {
		t.Fatalf("historical message got retroactive reactions: %+v %v", got, err)
	}
	newMessage := channelInput("new-message")
	newMessage.ReceivedAt = time.Now().UTC()
	enqueueChannel(t, s, newMessage)
	reactionClaim(t, s, newMessage.Profile, "received")
}
