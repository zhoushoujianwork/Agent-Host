package state

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func enqueueAPIReactionMessage(t *testing.T, s *Store, id string) ChannelMessage {
	t.Helper()
	m := feishuChannelInput(id, "1")
	m.DisableReactions = false
	if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil || !inserted {
		t.Fatalf("enqueue API reaction message: inserted=%v err=%v", inserted, err)
	}
	return m
}

func TestChannelAPIReactionOutcomesPersistProviderEvidence(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s, home := testStore(t)
			m := enqueueAPIReactionMessage(t, s, "outcome")
			r := reactionClaim(t, s, m.Profile, "received")
			providerID, cause := "", "unconfirmed platform outcome"
			if outcome == "accepted" {
				providerID, cause = "opaque/provider-reaction:123", ""
			}
			if err := s.FinishChannelAPIReaction(testContext, r.ID, providerID, outcome, cause); err != nil {
				t.Fatal(err)
			}
			reader, err := Open(home, true)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			rows, err := reader.ChannelReactions(testContext, m.ID)
			if err != nil || len(rows) != 1 || rows[0].ID != r.ID || rows[0].Status != outcome || rows[0].ProviderReactionID != providerID || rows[0].Active != (outcome == "accepted") || rows[0].Error != cause {
				t.Fatalf("provider acceptance was not persisted atomically: %+v %v", rows, err)
			}
			active, err := reader.ActiveChannelReactions(testContext, m.ID)
			if err != nil || outcome == "accepted" && (len(active) != 1 || active[0].ProviderReactionID != providerID) || outcome != "accepted" && len(active) != 0 {
				t.Fatalf("unconfirmed marker became removable: %+v %v", active, err)
			}
			message, err := reader.ChannelMessage(testContext, m.ID)
			if err != nil || message.Status != "queued" || message.ReplyStatus != "pending" || !reflect.DeepEqual(message.Reactions, rows) {
				t.Fatal("reaction completion changed task truth or lost inspection evidence", err)
			}
			if err := s.FinishChannelAPIReaction(testContext, r.ID, "replacement-id", "accepted", ""); err == nil {
				t.Fatal("terminal reaction was completed again")
			}
			if _, err := s.ClaimNextChannelReaction(testContext, m.Profile); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("attempted API marker was replayed", err)
			}
		})
	}
}

func TestChannelAPIReactionAcceptanceRollsBackOnPersistenceFailure(t *testing.T) {
	s, _ := testStore(t)
	m := enqueueAPIReactionMessage(t, s, "atomic")
	r := reactionClaim(t, s, m.Profile, "received")
	if _, err := s.db.Exec(`CREATE TRIGGER reject_api_reaction BEFORE UPDATE ON channel_reactions
WHEN NEW.provider_reaction_id <> '' BEGIN SELECT RAISE(ABORT, 'provider ID write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelAPIReaction(testContext, r.ID, "provider-id", "accepted", ""); err == nil {
		t.Fatal("failed acceptance write was hidden")
	}
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], r) {
		t.Fatalf("failed write partially committed acceptance or provider ID: %+v %v", rows, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_api_reaction"); err != nil {
		t.Fatal(err)
	}
	// Persist the same already-observed result; this makes no second API attempt.
	if err := s.FinishChannelAPIReaction(testContext, r.ID, "provider-id", "accepted", ""); err != nil {
		t.Fatal(err)
	}
}

func TestChannelAPIReactionRejectsInvalidTransitionsAndTransport(t *testing.T) {
	s, _ := testStore(t)
	m := enqueueAPIReactionMessage(t, s, "invalid")
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if err := s.FinishChannelAPIReaction(testContext, rows[0].ID, "provider-id", "accepted", ""); err == nil {
		t.Fatal("unclaimed API reaction finished")
	}
	r := reactionClaim(t, s, m.Profile, "received")
	for _, invalid := range []struct{ name, providerID, status, cause string }{
		{"missing ID", "", "accepted", ""},
		{"space ID", " ", "accepted", ""},
		{"padded ID", " id", "accepted", ""},
		{"control ID", "id\n", "accepted", ""},
		{"invalid UTF-8", string([]byte{0xff}), "accepted", ""},
		{"oversized ID", strings.Repeat("x", 1025), "accepted", ""},
		{"acceptance error", "provider-id", "accepted", "failed"},
		{"failure with ID", "provider-id", "failed", "failed"},
		{"unknown with ID", "provider-id", "unknown", "uncertain"},
		{"failure without error", "", "failed", ""},
		{"unknown without error", "", "unknown", " "},
		{"nonterminal outcome", "", "ready", "invalid"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if err := s.FinishChannelAPIReaction(testContext, r.ID, invalid.providerID, invalid.status, invalid.cause); err == nil {
				t.Fatal("invalid API reaction finish was accepted")
			}
			rows, err := s.ChannelReactions(testContext, m.ID)
			if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], r) {
				t.Fatal("rejected finish mutated the reaction", err)
			}
		})
	}
	if err := s.FinishChannelReaction(testContext, r.ID, "accepted", ""); err == nil {
		t.Fatal("legacy finalizer accepted an API reaction without provider evidence")
	}
	dingTalk := channelInput("legacy")
	dingTalk.Profile = "legacy-profile"
	enqueueChannel(t, s, dingTalk)
	legacy := reactionClaim(t, s, dingTalk.Profile, "received")
	if err := s.FinishChannelAPIReaction(testContext, legacy.ID, "provider-id", "accepted", ""); err == nil {
		t.Fatal("API finalizer accepted a DingTalk reaction")
	}
	reactionFinish(t, s, legacy, "accepted")
	if err := s.FinishChannelAPIReaction(testContext, r.ID, strings.Repeat("x", 1024), "accepted", ""); err != nil {
		t.Fatal("valid opaque provider ID at the size bound was rejected", err)
	}
}

func TestChannelAPIReactionConcurrentClaimsPreserveStageOrder(t *testing.T) {
	s, _ := testStore(t)
	m := enqueueAPIReactionMessage(t, s, "concurrent")
	claimChannel(t, s, m.Profile)
	for _, stage := range []string{"received", "processing"} {
		const workers = 8
		start := make(chan struct{})
		type result struct {
			reaction ChannelReaction
			err      error
		}
		done := make(chan result, workers)
		for i := 0; i < workers; i++ {
			go func() {
				<-start
				r, err := s.ClaimNextChannelReaction(testContext, m.Profile)
				done <- result{r, err}
			}()
		}
		close(start)
		var claimed []ChannelReaction
		for i := 0; i < workers; i++ {
			got := <-done
			if got.err == nil {
				claimed = append(claimed, got.reaction)
			} else if !errors.Is(got.err, sql.ErrNoRows) {
				t.Fatal(got.err)
			}
		}
		if len(claimed) != 1 || claimed[0].Stage != stage || claimed[0].Status != "sending" || claimed[0].ProviderReactionID != "" {
			t.Fatalf("concurrent claims duplicated or overtook %s: %+v", stage, claimed)
		}
		if err := s.FinishChannelAPIReaction(testContext, claimed[0].ID, "provider-"+stage, "accepted", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChannelAPIReactionCrashRecoveryNeverGuessesIDOrReplays(t *testing.T) {
	s, home := testStore(t)
	m := enqueueAPIReactionMessage(t, s, "crash")
	r := reactionClaim(t, s, m.Profile, "received")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for i := 0; i < 2; i++ {
		if err := reopened.RecoverChannels(testContext); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := reopened.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 1 || rows[0].ID != r.ID || rows[0].Status != "unknown" || rows[0].Error == "" || rows[0].ProviderReactionID != "" || rows[0].Active {
		t.Fatalf("crash recovery invented provider evidence: %+v %v", rows, err)
	}
	if _, err := reopened.ClaimNextChannelReaction(testContext, m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unknown add was replayed", err)
	}
	if err := reopened.ClaimChannelReactionRemoval(testContext, r.ID); err == nil {
		t.Fatal("unknown add gained a removal capability")
	}
	if err := reopened.FinishChannelAPIReaction(testContext, r.ID, "guessed-id", "accepted", ""); err == nil {
		t.Fatal("recovered unknown add became accepted")
	}
	message, err := reopened.ChannelMessage(testContext, m.ID)
	if err != nil || message.Status != "queued" || message.ReplyStatus != "pending" {
		t.Fatal("reaction recovery changed task execution or reply truth", err)
	}
}

func TestSchemaV13APIReactionMigrationPreservesHistoryWithoutRetroactiveMarkers(t *testing.T) {
	s, home := testStore(t)
	legacy := channelInput("old-dingtalk")
	legacy.Profile = "dingtalk-profile"
	enqueueChannel(t, s, legacy)
	r := reactionClaim(t, s, legacy.Profile, "received")
	reactionFinish(t, s, r, "accepted")
	before, err := s.ChannelReactions(testContext, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	im := feishuChannelInput("old-im", "1")
	im.Profile = "im-profile"
	if inserted, err := s.EnqueueChannelMessage(testContext, im, ""); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	payloads := map[string]string{}
	for _, id := range []string{legacy.ID, im.ID} {
		var payload string
		if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		payloads[id] = payload
	}
	// Start with a complete valid database, then remove only the additive v14
	// column. Existing rows use no provider IDs, exactly as in schema v13.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("ALTER TABLE channel_reactions DROP COLUMN provider_reaction_id"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("PRAGMA user_version=13"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if reader, err := Open(home, true); err == nil {
		reader.Close()
		t.Fatal("read-only access accepted an unmigrated reaction schema")
	}
	upgraded, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("schema migration incomplete: version=%d err=%v", version, err)
	}
	rows, err := upgraded.ChannelReactions(testContext, legacy.ID)
	if err != nil || !reflect.DeepEqual(rows, before) || rows[0].ProviderReactionID != "" {
		t.Fatal("migration changed DingTalk reaction evidence", rows, err)
	}
	for id, want := range payloads {
		var got string
		if err := upgraded.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", id).Scan(&got); err != nil || got != want {
			t.Fatal("migration changed original message payload", err)
		}
	}
	im.DisableReactions = false
	if inserted, err := upgraded.EnqueueChannelMessage(testContext, im, ""); err != nil || inserted {
		t.Fatal("redelivery under enabled feedback became a new receipt", inserted, err)
	}
	claimChannel(t, upgraded, im.Profile)
	reactionReply(t, upgraded, im, "completed", "accepted")
	if err := upgraded.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	saved, err := upgraded.ChannelMessage(testContext, im.ID)
	if err != nil || !saved.DisableReactions || len(saved.Reactions) != 0 {
		t.Fatal("legacy IM acquired retroactive feedback", saved, err)
	}
	claimChannel(t, upgraded, legacy.Profile)
	processing := reactionClaim(t, upgraded, legacy.Profile, "processing")
	reactionFinish(t, upgraded, processing, "accepted")
	fresh := enqueueAPIReactionMessage(t, upgraded, "new-im")
	reactionClaim(t, upgraded, fresh.Profile, "received")
}
