package state

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"agent-host/internal/event"
)

func TestRuntimePhaseReactionsRepeatByEventAndRetainFailures(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "runtime-phases")
	var causes []event.Event
	for _, e := range []event.Event{
		{Type: "phase.started", Phase: "execute", Status: "running"},
		{Type: "phase.started", Phase: "knowledge_search", Status: "running"},
		{Type: "phase.finished", Phase: "knowledge_search", Status: "failed"},
		{Type: "phase.resumed", Phase: "execute", Status: "running"},
		{Type: "phase.started", Phase: "knowledge_search", Status: "running"},
		{Type: "phase.started", Phase: "knowledge_read", Status: "running"},
		{Type: "phase.finished", Phase: "knowledge_read", Status: "unknown"},
	} {
		e.RunID = run.ID
		if err := s.AppendEvent(testContext, &e); err != nil {
			t.Fatal(err)
		}
		causes = append(causes, e)
	}
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"已收到", "处理中", "AI执行", "检索知识", "检索失败", "AI执行", "检索知识", "阅读资料", "读取中断"}
	if len(rows) != len(want) {
		t.Fatalf("repeated phases lost: %+v", rows)
	}
	for i, row := range rows {
		if row.Label != want[i] {
			t.Fatalf("phase %d label %s want %s", i, row.Label, want[i])
		}
		if i >= 2 && (row.SourceRunID != run.ID || row.SourceEventSeq != causes[i-2].Seq) {
			t.Fatal("phase lost original event")
		}
	}
	last := causes[len(causes)-1]
	if err := s.transaction(testContext, func(tx *sql.Tx) error { return projectChannelPhase(testContext, tx, run, last) }); err != nil {
		t.Fatal(err)
	}
	again, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(again) != len(rows) {
		t.Fatal("same source event was projected twice", err)
	}
}

func TestRuntimePhaseMigrationPreservesV7History(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + schemaV2 + schemaV3 + schemaV4 + schemaV5 + schemaReactionsV6 + schemaProjectsV7); err != nil {
		t.Fatal(err)
	}
	m := channelInput("before-runtime-phase")
	data, _ := json.Marshal(m)
	if _, err := db.Exec("INSERT INTO channel_messages(id,channel,account_id,message_id,profile,status,received_at,reply_url,payload) VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.Channel, m.AccountID, m.MessageID, m.Profile, "claimed", m.ReceivedAt.Format(reactionTimeFormat), testReplyURL, data); err != nil {
		t.Fatal(err)
	}
	stamp := m.ReceivedAt.Format(reactionTimeFormat)
	if _, err := db.Exec("INSERT INTO channel_reactions(id,channel_message_id,profile,stage,label,status,source_run_id,source_event_seq,created_at,updated_at) VALUES(41,?,?,'execute','AI执行','unknown','old-run',8,?,?)", m.ID, m.Profile, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if old, err := Open(home, true); err == nil {
		old.Close()
		t.Fatal("read-only open silently migrated")
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 1 || rows[0].ID != 41 || rows[0].Status != "unknown" || rows[0].SourceRunID != "old-run" || rows[0].SourceEventSeq != 8 {
		t.Fatalf("migration rewrote history: %+v %v", rows, err)
	}
	if err := s.transaction(testContext, func(tx *sql.Tx) error {
		if err := queueChannelReaction(testContext, tx, m, "received"); err != nil {
			return err
		}
		if err := queueChannelReactionSource(testContext, tx, m, "execute", "old-run", 8); err != nil {
			return err
		}
		return queueChannelReactionSource(testContext, tx, m, "knowledge_search", "old-run", 9)
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 3 || rows[2].ID <= 41 {
		t.Fatalf("migration lost sequence: %+v %v", rows, err)
	}
}
