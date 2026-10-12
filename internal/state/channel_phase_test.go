package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"agent-host/internal/event"
)

func phaseRun(t *testing.T, s *Store, id string) (ChannelMessage, Run) {
	t.Helper()
	m := channelInput(id)
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	r := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.Run(testContext, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, r
}

func phaseEvent(t *testing.T, s *Store, run Run, phase string) event.Event {
	t.Helper()
	e := event.Event{RunID: run.ID, Type: "phase.started", Phase: phase}
	if err := s.AppendEvent(testContext, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func phaseRows(t *testing.T, s *Store, id string) map[string]ChannelReaction {
	t.Helper()
	rows, err := s.ChannelReactions(testContext, id)
	if err != nil {
		t.Fatal(err)
	}
	stages := map[string]ChannelReaction{}
	for _, row := range rows {
		stages[row.Stage] = row
	}
	return stages
}

func TestChannelPhaseProjectionUsesDurableEventSequenceAndExactLabels(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "phases")
	phases := []struct{ phase, label string }{
		{"recall", "检索记忆"}, {"execute", "AI执行"}, {"remember", "Jev判断"}, {"persist", "保存记忆"},
		{"evidence", "整理证据"}, {"extract", "提取经验"}, {"assess", "Jev评估"}, {"candidates", "保存候选"},
	}
	for i, phase := range phases {
		if phase.phase == "remember" {
			// Native completion precedes post-execution memory phases.
			if err := s.CompleteRun(testContext, run.ID, "completed", "answer", "actual-model", ""); err != nil {
				t.Fatal(err)
			}
		}
		e := phaseEvent(t, s, run, phase.phase)
		rows := phaseRows(t, s, m.ID)
		got := rows[phase.phase]
		if got.Label != phase.label || got.Status != "ready" || got.SourceRunID != run.ID || got.SourceEventSeq != e.Seq || e.Seq != int64(i+1) {
			t.Fatalf("phase %s: reaction=%+v event=%+v", phase.phase, got, e)
		}
		if rows["received"].Status != "ready" || rows["processing"].Status != "skipped" {
			t.Fatalf("phase did not supersede only obsolete ready stages: %+v", rows)
		}
		if i > 0 && rows[phases[i-1].phase].Status != "skipped" {
			t.Fatalf("older ready phase was not superseded: %+v", rows)
		}
	}
	before := phaseRows(t, s, m.ID)
	phaseEvent(t, s, run, "remember")
	after := phaseRows(t, s, m.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate phase changed first cause or superseded a later phase")
	}
	reactionClaim(t, s, m.Profile, "received")
}

func TestChannelPhaseOutboxAndEventCommitAtomically(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "atomic-phase")
	if _, err := s.db.Exec("CREATE TRIGGER reject_phase BEFORE INSERT ON channel_reactions WHEN NEW.stage='execute' BEGIN SELECT RAISE(ABORT, 'reaction persistence failed'); END;"); err != nil {
		t.Fatal(err)
	}
	e := event.Event{RunID: run.ID, Type: "phase.started", Phase: "execute"}
	if err := s.AppendEvent(testContext, &e); err == nil || e.Seq != 0 {
		t.Fatalf("failed append appeared committed: %+v %v", e, err)
	}
	stored, err := s.Run(testContext, run.ID)
	if err != nil || len(stored.Events) != 0 || phaseRows(t, s, m.ID)["processing"].Status != "ready" {
		t.Fatalf("event or supersession survived rollback: %+v %v", stored, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_phase"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(testContext, &e); err != nil || e.Seq != 1 {
		t.Fatalf("retry failed: %+v %v", e, err)
	}
	if row := phaseRows(t, s, m.ID)["execute"]; row.SourceEventSeq != 1 || row.SourceRunID != run.ID {
		t.Fatal("projection lacks committed cause", row)
	}
}

func TestChannelPhaseProjectionSkipsUnrelatedEventsAndTerminalMessages(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "unrelated")
	before := phaseRows(t, s, m.ID)
	for _, e := range []event.Event{
		{RunID: run.ID, Type: "phase.finished", Phase: "execute"},
		{RunID: run.ID, Type: "phase.progress", Phase: "remember"},
		{RunID: run.ID, Type: "phase.started", Phase: "review"},
		{RunID: run.ID, Type: "phase.started", Phase: "publish"},
		{RunID: run.ID, Type: "phase.started", Phase: "received"},
	} {
		if err := s.AppendEvent(testContext, &e); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, phaseRows(t, s, m.ID)) {
		t.Fatal("unrelated event generated feedback")
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	phaseEvent(t, s, run, "remember")
	if !reflect.DeepEqual(before, phaseRows(t, s, m.ID)) {
		t.Fatal("terminal channel memory retry generated feedback")
	}
	standalone := addRun(t, s, "standalone", "/workspace", "CLI input", false)
	phaseEvent(t, s, standalone, "execute")
	if !reflect.DeepEqual(before, phaseRows(t, s, m.ID)) {
		t.Fatal("standalone CLI run affected channel")
	}
}

func TestChannelPhaseProjectionRequiresOriginalProvenanceAndEnrollment(t *testing.T) {
	for _, change := range []string{"source message", "source route", "source sender", "source account", "source tenant", "source channel", "session", "scope", "enrollment"} {
		t.Run(change, func(t *testing.T) {
			s, _ := testStore(t)
			m, run := phaseRun(t, s, "provenance")
			switch change {
			case "source message":
				run.Source.MessageID = "unrelated"
			case "source route":
				run.Source.ConversationID = "another-conversation"
			case "source sender":
				run.Source.SenderID = "another-user"
			case "source account":
				run.Source.AccountID = "another-app"
			case "source tenant":
				run.Source.TenantID = "another-enterprise"
			case "source channel":
				run.Source.Channel = "another-channel"
			case "session":
				run.SessionID = "another-session"
			case "scope":
				run.Scope = "/another-scope"
			case "enrollment":
				if _, err := s.db.Exec("DELETE FROM channel_reactions WHERE channel_message_id=?", m.ID); err != nil {
					t.Fatal(err)
				}
			}
			if change != "enrollment" {
				if err := s.transaction(testContext, func(tx *sql.Tx) error { return writeRun(testContext, tx, run) }); err != nil {
					t.Fatal(err)
				}
			}
			before := phaseRows(t, s, m.ID)
			phaseEvent(t, s, run, "execute")
			if !reflect.DeepEqual(before, phaseRows(t, s, m.ID)) {
				t.Fatal("unmatched source produced feedback")
			}
		})
	}
}

func TestChannelPhaseSupersessionPreservesAttemptsAndQueuesCurrentStage(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "supersede")
	received := reactionClaim(t, s, m.Profile, "received")
	reactionFinish(t, s, received, "accepted")
	processing := reactionClaim(t, s, m.Profile, "processing")
	reactionFinish(t, s, processing, "failed")
	phaseEvent(t, s, run, "recall")
	recall := reactionClaim(t, s, m.Profile, "recall")
	phaseEvent(t, s, run, "execute")
	phaseEvent(t, s, run, "remember")
	rows := phaseRows(t, s, m.ID)
	if rows["received"].Status != "accepted" || rows["processing"].Status != "failed" || rows["recall"].Status != "sending" || rows["execute"].Status != "skipped" || rows["remember"].Status != "ready" {
		t.Fatal("supersession changed attempted rows or retained obsolete row", rows)
	}
	if _, err := s.ClaimNextChannelReaction(testContext, m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("later stage overtook in-flight predecessor", err)
	}
	reactionFinish(t, s, recall, "unknown")
	remember := reactionClaim(t, s, m.Profile, "remember")
	reactionFinish(t, s, remember, "accepted")
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "answer"); err != nil {
		t.Fatal(err)
	}
	if rows := phaseRows(t, s, m.ID); rows["replying"].Label != "发送回复" || rows["replying"].Status != "ready" || rows["replying"].SourceEventSeq != 0 {
		t.Fatal(rows)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	rows = phaseRows(t, s, m.ID)
	if rows["replying"].Status != "skipped" || rows["completed"].Status != "ready" {
		t.Fatal("terminal did not supersede unattempted delivery phase", rows)
	}
	reactionClaim(t, s, m.Profile, "completed")
}

func TestChannelPhaseRecoverySkipsObsoleteStagesWithoutReplay(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "recover-phases")
	received := reactionClaim(t, s, m.Profile, "received")
	reactionFinish(t, s, received, "accepted")
	if err := s.ClaimChannelReactionRemoval(testContext, received.ID); err != nil {
		t.Fatal(err)
	}
	phaseEvent(t, s, run, "execute")
	execute := reactionClaim(t, s, m.Profile, "execute")
	phaseEvent(t, s, run, "remember")
	if err := s.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	rows := phaseRows(t, s, m.ID)
	if rows["execute"].ID != execute.ID || rows["execute"].Status != "unknown" || rows["remember"].Status != "skipped" || rows["received"].RemovalStatus != "unknown" || !rows["received"].Active || rows["failed"].Status != "ready" {
		t.Fatal("recovery lost uncertain outcomes or retained obsolete stage", rows)
	}
	reactionClaim(t, s, m.Profile, "failed")
}

func TestReactionPhaseMigrationPreservesIDsOutcomesAndRemovalAudit(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + schemaV2 + schemaV3 + schemaV4 + schemaV5); err != nil {
		t.Fatal(err)
	}
	m := channelInput("old-phases")
	m.Status, m.ReplyStatus = "queued", "pending"
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channel_messages(id,channel,account_id,message_id,profile,status,received_at,reply_url,payload) VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.Channel, m.AccountID, m.MessageID, m.Profile, m.Status, m.ReceivedAt.Format(reactionTimeFormat), testReplyURL, payload); err != nil {
		t.Fatal(err)
	}
	stamp := m.ReceivedAt.Format(reactionTimeFormat)
	if _, err := db.Exec(`INSERT INTO channel_reactions(id,channel_message_id,profile,stage,label,status,error,active,removal_status,removal_error,created_at,updated_at) VALUES
(9,?,?,'received','已收到','accepted','',1,'unknown','cleanup response lost',?,?),
(19,?,?,'processing','处理中','unknown','add response lost',0,'pending','',?,?),
(100,?,?,'completed','已完成','ready','',0,'pending','',?,?)`, m.ID, m.Profile, stamp, stamp, m.ID, m.Profile, stamp, stamp, m.ID, m.Profile, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM channel_reactions WHERE id=100"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := phaseRows(t, s, m.ID)
	if len(rows) != 2 || rows["received"].ID != 9 || rows["received"].Status != "accepted" || !rows["received"].Active || rows["received"].RemovalStatus != "unknown" || rows["received"].RemovalError != "cleanup response lost" || rows["processing"].ID != 19 || rows["processing"].Status != "unknown" || rows["processing"].Error != "add response lost" {
		t.Fatal("migration changed durable outcomes", rows)
	}
	for _, row := range rows {
		if row.SourceRunID != "" || row.SourceEventSeq != 0 || row.CreatedAt.Format(reactionTimeFormat) != stamp || row.UpdatedAt.Format(reactionTimeFormat) != stamp {
			t.Fatal("migration invented attribution or changed timestamp", row)
		}
	}
	claimChannel(t, s, m.Profile)
	r := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	phaseEvent(t, s, r, "execute")
	if row := phaseRows(t, s, m.ID)["execute"]; row.ID <= 100 || row.SourceRunID != r.ID {
		t.Fatal("migration reset AUTOINCREMENT or disallowed new stage", row)
	}
	for _, index := range []string{"channel_reactions_queue", "channel_reactions_terminal"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&count); err != nil || count != 1 {
			t.Fatal("migration lost index", index, err)
		}
	}
}
