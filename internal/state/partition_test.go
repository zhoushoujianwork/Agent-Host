package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"agent-host/internal/event"
)

func partitionStatement(t *testing.T, s *Store, id, scope, text string) Memory {
	t.Helper()
	r := addRun(t, s, id, scope, text, true)
	update := updateFor(r, "memory-"+id)
	update.Memory.Kind = "project_fact"
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{update}, decision); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memory(testContext, "memory-"+id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLearnedKnowledgePartitionBeforeRetrievalAndMutation(t *testing.T) {
	root, _ := testStore(t)
	a, b, otherAgent := root.WithPartition("dev", "group-a"), root.WithPartition("dev", "group-b"), root.WithPartition("ops", "group-a")
	stores := []*Store{root, a, b, otherAgent}
	memories := make([]Memory, len(stores))
	for i, s := range stores {
		id := []string{"owner", "a", "b", "ops"}[i]
		memories[i] = partitionStatement(t, s, id, "project:shared", "订单服务的数据库使用 PostgreSQL。")
	}
	for i, s := range stores {
		got, err := s.Memories(testContext, "project:shared")
		if err != nil || len(got) != 1 || got[0].ID != memories[i].ID {
			t.Fatalf("partition memories %d: %+v %v", i, got, err)
		}
		retrieved, err := s.Retrieve(testContext, "project:shared", "订单服务的数据库", 5)
		if err != nil || len(retrieved.Matches) != 1 || retrieved.Matches[0].ID != memories[i].ID {
			t.Fatalf("partition retrieval %d: %+v %v", i, retrieved, err)
		}
		for j, m := range memories {
			if i == j {
				continue
			}
			if _, err := s.Memory(testContext, m.ID); err == nil {
				t.Fatal("cross-partition by-ID memory read")
			}
			if err := s.InvalidateMemory(testContext, m.ID, "wrong audience"); err == nil {
				t.Fatal("cross-partition invalidation")
			}
		}
	}
	// Identical project identities do not allow foreign conflict/duplicate IDs.
	for _, duplicate := range []bool{false, true} {
		id := "conflict"
		if duplicate {
			id = "duplicate"
		}
		r := addRun(t, a, id, "project:shared", "The orchid service uses Postgres.", true)
		update := updateFor(r, "memory-"+id)
		if duplicate {
			update.DuplicateIDs = []string{memories[2].ID}
		} else {
			update.ConflictIDs = []string{memories[2].ID}
		}
		if err := a.FinalizeMemory(testContext, r.ID, []MemoryUpdate{update}, decision); err == nil {
			t.Fatal("foreign memory relation accepted")
		}
		got, _ := b.Memory(testContext, memories[2].ID)
		if got.Status != "active" {
			t.Fatal("failed relation modified another audience")
		}
	}
	r := addRun(t, a, "context", "project:shared", "Investigate orchid", false)
	if err := a.SaveContext(testContext, r.ID, []Memory{memories[0]}); err == nil {
		t.Fatal("owner context was inserted into group")
	}
	if _, err := b.Run(testContext, r.ID); err == nil {
		t.Fatal("cross-partition run lookup")
	}
	if _, err := b.Session(testContext, r.SessionID); err == nil {
		t.Fatal("cross-partition session lookup")
	}
	if _, err := b.SessionHasRun(testContext, r.SessionID); err == nil {
		t.Fatal("cross-partition session existence")
	}
	if err := b.SessionCanResume(testContext, r.SessionID); err == nil {
		t.Fatal("cross-partition resume")
	}
	if err := b.CompleteRun(testContext, r.ID, "completed", "forged", "model", ""); err == nil {
		t.Fatal("cross-partition completion")
	}
	e := event.Event{RunID: r.ID, Type: "native.tool"}
	if err := b.AppendEvent(testContext, &e); err == nil {
		t.Fatal("cross-partition evidence append")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Memory(testContext, memories[0].ID); err != nil {
		t.Fatal("borrowed Close closed owning store", err)
	}
}

func TestCandidateGovernanceAndRetryKeepAudience(t *testing.T) {
	root, _ := testStore(t)
	a, b := root.WithPartition("dev", "a"), root.WithPartition("dev", "b")
	ca, cb := experienceCandidate(t, a, "a", "shared"), experienceCandidate(t, b, "b", "shared")
	saveCandidate(t, a, ca)
	saveCandidate(t, b, cb)
	pending, err := a.PendingMemory(testContext)
	if err != nil || len(pending) != 1 || pending[0].ID != ca.RunID {
		t.Fatal("pending retry partition", pending, err)
	}
	if _, err := a.GetCandidate(testContext, cb.ID); err == nil {
		t.Fatal("foreign candidate lookup")
	}
	if _, err := a.CandidateEvidence(testContext, cb.ID); err == nil {
		t.Fatal("foreign candidate evidence")
	}
	if _, err := a.ToolEvidence(testContext, cb.RunID); err == nil {
		t.Fatal("foreign run evidence")
	}
	if err := a.RejectCandidate(testContext, cb.ID, "wrong audience"); err == nil {
		t.Fatal("foreign candidate rejection")
	}
	if err := a.ExperienceFailure(testContext, cb.RunID, "wrong audience"); err == nil {
		t.Fatal("foreign extraction mutation")
	}
	if err := a.FinalizeMemory(testContext, cb.RunID, nil, decision); err == nil {
		t.Fatal("foreign memory retry")
	}
	if _, err := a.AcceptCandidate(testContext, cb.ID, CandidateAcceptance{MemoryID: "bad", Decision: decision}); err == nil {
		t.Fatal("foreign candidate acceptance")
	}
	am, bm := acceptCandidate(t, a, ca.ID, "a-memory", nil, nil), acceptCandidate(t, b, cb.ID, "b-memory", nil, nil)
	if am.Text != bm.Text || am.ID == bm.ID {
		t.Fatal("identical independent procedures should remain separate")
	}
	got, err := a.Candidates(testContext, "shared", "", "all")
	if err != nil || len(got) != 1 || got[0].ID != ca.ID {
		t.Fatalf("candidate partition %+v %v", got, err)
	}
}

func TestActorAttributionSurvivesSameTextWithoutDedup(t *testing.T) {
	root, _ := testStore(t)
	group := root.WithPartition("dev", "group")
	for _, sender := range []string{"alice", "bob"} {
		m := channelInput(sender)
		m.AgentID, m.AudienceID, m.ConversationType = "dev", "group", "2"
		m.SenderID = sender
		m.Text = "I prefer short answers."
		enqueueChannel(t, root, m)
		m = claimChannel(t, root, m.Profile)
		session := Session{ID: "session-" + sender, Scope: m.Scope, NativeSessionID: "native-" + sender}
		if err := group.CreateSession(testContext, session); err != nil {
			t.Fatal(err)
		}
		if err := root.BindChannelSession(testContext, m.Binding, session.ID); err != nil {
			t.Fatal(err)
		}
		r := Run{ID: "run-" + sender, SessionID: session.ID, Scope: m.Scope, NativeSessionID: session.NativeSessionID, Input: m.Text}
		if err := group.CreateChannelRun(testContext, r, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := group.CompleteRun(testContext, r.ID, "completed", "Acknowledged", "model", ""); err != nil {
			t.Fatal(err)
		}
		update := updateFor(r, "memory-"+sender)
		update.Memory.SenderID = "forged"
		if err := group.FinalizeMemory(testContext, r.ID, []MemoryUpdate{update}, decision); err != nil {
			t.Fatal(err)
		}
	}
	got, err := group.Memories(testContext, channelInput("sample").Scope)
	if err != nil || len(got) != 2 || got[0].SenderID != "alice" || got[1].SenderID != "bob" {
		t.Fatalf("actor provenance %+v %v", got, err)
	}
}

func TestPartitionMigrationPreservesHistoricalPayloadsAndOwnership(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaReactionsV6, schemaProjectsV7, schemaRuntimePhasesV8, schemaRoutingV9} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	legacy := &Store{db: db}
	r := addRun(t, legacy, "legacy", "project:shared", "The legacy database uses SQLite.", true)
	m := Memory{ID: "legacy-memory", Scope: r.Scope, Text: r.Input, Kind: "project_fact", Status: "active", Revision: 1, SourceRunID: r.ID}
	raw, _ := json.Marshal(m)
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO memories(id,scope,text,status,payload) VALUES(?,?,?,?,?)", []any{m.ID, m.Scope, m.Text, m.Status, raw}},
		{"INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", []any{m.ID, 1, raw}},
		{"INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", []any{m.ID, r.ID}},
	} {
		if _, err := db.Exec(query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.transaction(testContext, func(tx *sql.Tx) error { return backfillAnnotations(testContext, tx) }); err != nil {
		t.Fatal(err)
	}
	snapshots := map[string][]byte{}
	for _, table := range []string{"sessions", "runs", "memories", "memory_revisions", "memory_annotations"} {
		var p []byte
		if err := db.QueryRow("SELECT payload FROM " + table + " LIMIT 1").Scan(&p); err != nil {
			t.Fatal(err)
		}
		snapshots[table] = p
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	for table, want := range snapshots {
		var got []byte
		if err := migrated.db.QueryRow("SELECT payload FROM " + table + " LIMIT 1").Scan(&got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("raw %s changed during migration: %v", table, err)
		}
	}
	got, err := migrated.Memory(testContext, m.ID)
	if err != nil || got.AgentID != DefaultAgentID || got.AudienceID != OwnerAudienceID {
		t.Fatal("legacy ownership", got, err)
	}
	group := migrated.WithPartition("default", "group")
	if got, err := group.Memories(testContext, m.Scope); err != nil || len(got) != 0 {
		t.Fatal("owner memory entered group", got, err)
	}
	if _, err := group.Run(testContext, r.ID); err == nil {
		t.Fatal("owner run entered group")
	}
	var foreignKeys int
	if err := migrated.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("foreign keys not restored", foreignKeys, err)
	}
	partitionStatement(t, group, "new-group", m.Scope, m.Text)
	if got, err := migrated.Memories(testContext, m.Scope); err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatal("new group text changed owner memory", got, err)
	}
}

func TestAliasRegistryAndRebuildAreAudienceLocal(t *testing.T) {
	root, _ := testStore(t)
	a, b := root.WithPartition("dev", "a"), root.WithPartition("dev", "b")
	recallSeed(t, a, "a-fact", "shared", "交易的数据库使用 PostgreSQL。")
	recallSeed(t, a, "a-alias", "shared", "订单服务（简称交易）的端口为 8080。")
	recallSeed(t, b, "b-fact", "shared", "交易的数据库使用 MySQL。")
	recallSeed(t, b, "b-alias", "shared", "账单服务（简称交易）的端口为 8081。")
	for _, tc := range []struct {
		s              *Store
		question, want string
	}{{a, "订单服务的数据库", "a-fact"}, {b, "账单服务的数据库", "b-fact"}} {
		got, err := tc.s.Retrieve(testContext, "shared", tc.question, 10)
		if err != nil || len(got.Matches) != 1 || got.Matches[0].ID != tc.want || len(got.Query.Ambiguous) != 0 {
			t.Fatalf("aliases crossed audiences: %+v %v", got, err)
		}
	}
	if err := b.InvalidateMemory(testContext, "b-alias", "withdraw alias"); err != nil {
		t.Fatal(err)
	}
	got, err := a.Retrieve(testContext, "shared", "订单服务的数据库", 10)
	if err != nil || len(got.Matches) != 1 || got.Matches[0].ID != "a-fact" {
		t.Fatalf("foreign reindex changed aliases %+v %v", got, err)
	}
}

func TestNewPublicReceiptKeepsOnlyGenericReactions(t *testing.T) {
	for _, kind := range []string{"1", "2"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := testStore(t)
			s := root
			m := channelInput("public")
			m.ConversationType = kind
			if kind == "2" {
				m.AgentID, m.AudienceID = "default", "group"
				s = root.WithPartition(m.AgentID, m.AudienceID)
			}
			enqueueChannel(t, root, m)
			m = claimChannel(t, root, m.Profile)
			r := channelRun(t, s, m)
			if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"recall", "execute", "remember", "extract"} {
				phaseEvent(t, s, r, phase)
			}
			if err := root.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if err := root.PrepareChannelReply(testContext, m.ID, "answer"); err != nil {
				t.Fatal(err)
			}
			for _, reaction := range phaseRows(t, root, m.ID) {
				if reaction.Stage != "received" && reaction.Stage != "processing" && reaction.Stage != "completed" && reaction.Stage != "failed" {
					t.Fatalf("internal phase became public: %+v", reaction)
				}
			}
			run, err := s.Run(testContext, r.ID)
			if err != nil || len(run.Events) != 4 {
				t.Fatal("local diagnostics lost", run, err)
			}
		})
	}
}

func TestPartitionPreflightCanInspectUnmigratedSchema9(t *testing.T) {
	home, want := projectInspectionV7Fixture(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaRuntimePhasesV8 + schemaRoutingV9); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenForProjectInspection(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Project(testContext, want.ID)
	if err != nil || got.Scope != want.Scope {
		t.Fatal("preflight registry mismatch", got, err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 9 {
		t.Fatal("preflight mutated schema", version, err)
	}
}

func TestSkipLegacyReactionRequiresUnfinishedClaim(t *testing.T) {
	s, _ := testStore(t)
	m, run := phaseRun(t, s, "legacy-phase")
	received := reactionClaim(t, s, m.Profile, "received")
	reactionFinish(t, s, received, "accepted")
	phaseEvent(t, s, run, "recall")
	reaction := reactionClaim(t, s, m.Profile, "recall")
	if err := s.SkipChannelReaction(testContext, reaction.ID, "private phase omitted"); err != nil {
		t.Fatal(err)
	}
	got := phaseRows(t, s, m.ID)["recall"]
	if got.Status != "skipped" || got.Active || got.Error != "private phase omitted" {
		t.Fatalf("skip outcome %+v", got)
	}
	if err := s.SkipChannelReaction(testContext, reaction.ID, "repeat"); err == nil {
		t.Fatal("terminal reaction reprocessed")
	}
}
