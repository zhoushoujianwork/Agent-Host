package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/annotation"
)

func recallSeed(t *testing.T, s *Store, id, scope, text string) {
	t.Helper()
	run := addRun(t, s, "run-"+id, scope, text, true)
	update := updateFor(run, id)
	update.Memory.Kind = "project_fact"
	if err := s.FinalizeMemory(testContext, run.ID, []MemoryUpdate{update}, decision); err != nil {
		t.Fatal(err)
	}
}

func recallIDs(memories []Memory) []string {
	ids := []string{}
	for _, m := range memories {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestRetrieveObjectApplicabilityBeforeLimit(t *testing.T) {
	s, _ := testStore(t)
	for i := 0; i < 20; i++ {
		recallSeed(t, s, fmt.Sprintf("billing-%02d", i), "project", fmt.Sprintf("账单服务的数据库使用 MySQL %d 数据库 数据库。", i))
	}
	recallSeed(t, s, "orders", "project", "订单服务的数据库使用 PostgreSQL。")
	recallSeed(t, s, "port", "project", "订单服务的端口为 8080。")
	recallSeed(t, s, "foreign", "elsewhere", "订单服务的数据库使用 SQLite。")
	got, err := s.Retrieve(testContext, "project", "订单服务的数据库", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recallIDs(got.Matches), []string{"orders"}) {
		t.Fatalf("matches: %+v", got)
	}
	if len(got.LexicalIDs) < 20 || len(got.Decisions) < 20 {
		t.Fatalf("candidate diagnostics truncated before applicability: %+v", got)
	}
	for _, d := range got.Decisions {
		if d.MemoryID == "foreign" {
			t.Fatal("cross-scope candidate considered")
		}
		if d.MemoryID != "orders" && d.Accepted {
			t.Fatalf("unrelated memory accepted: %+v", d)
		}
	}
}

func TestStoredAliasReferenceReindexesWithoutSourceRewrite(t *testing.T) {
	s, _ := testStore(t)
	// The alias-only fact precedes its explicit identity declaration.
	recallSeed(t, s, "database", "project", "交易的数据库使用 PostgreSQL。")
	before, err := s.Memory(testContext, "database")
	if err != nil {
		t.Fatal(err)
	}
	recallSeed(t, s, "identity", "project", "订单服务（简称交易）的端口为 8080。")
	recallSeed(t, s, "billing", "project", "账单服务的数据库使用 MySQL。")
	got, err := s.Retrieve(testContext, "project", "订单服务的数据库", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recallIDs(got.Matches), []string{"database"}) {
		t.Fatalf("alias-only fact not bound: %+v", got)
	}
	after, err := s.Memory(testContext, "database")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("derived indexing rewrote memory: %+v %+v %v", before, after, err)
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM memory_revisions WHERE memory_id='database'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("unexpected revisions %d %v", count, err)
	}
	if err = s.InvalidateMemory(testContext, "identity", "alias declaration withdrawn"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Retrieve(testContext, "project", "订单服务的数据库", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 0 {
		t.Fatalf("withdrawn alias still authorizes binding: %+v", got)
	}
}

func TestRegistryExcludesInactiveAndExpiredAliases(t *testing.T) {
	s, _ := testStore(t)
	recallSeed(t, s, "alias", "project", "订单服务（简称交易服务）的数据库使用 PostgreSQL。")
	recallSeed(t, s, "billing", "project", "账单服务的数据库使用 MySQL。")
	before, err := s.Retrieve(testContext, "project", "交易服务的数据库", 10)
	if err != nil || !reflect.DeepEqual(recallIDs(before.Matches), []string{"alias"}) {
		t.Fatalf("active alias: %+v %v", before, err)
	}
	// Simulate time passing after indexing: no annotation/term reindex runs.
	// The read-time expiry gate must independently withdraw the alias registry.
	if _, err = s.db.Exec("UPDATE memories SET expires_at=? WHERE id='alias'", timestamp(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	after, err := s.Retrieve(testContext, "project", "交易服务的数据库", 10)
	if err != nil || len(after.Matches) != 0 {
		t.Fatalf("expired alias affected lookup: %+v %v", after, err)
	}
	for _, d := range after.Decisions {
		if d.MemoryID == "alias" {
			t.Fatal("expired candidate considered")
		}
	}
	if len(after.Query.Unknown) == 0 {
		t.Fatalf("expired alias still resolved: %+v", after.Query)
	}
}

func TestLifecycleRemovesDerivedTerms(t *testing.T) {
	s, _ := testStore(t)
	recallSeed(t, s, "orders", "project", "订单服务的数据库使用 PostgreSQL。")
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memory_terms WHERE memory_id='orders'").Scan(&count); err != nil || count == 0 {
		t.Fatalf("no derived terms: %d %v", count, err)
	}
	r := addRun(t, s, "conflicting", "project", "订单服务的数据库使用 MySQL。", true)
	u := updateFor(r, "other")
	u.ConflictIDs = []string{"orders"}
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{u}, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memory_terms").Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflicted terms retained: %d %v", count, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memory_annotations").Scan(&count); err != nil || count != 2 {
		t.Fatalf("source annotations removed: %d %v", count, err)
	}
}

func TestSchemaV3BackfillPreservesExactV2Data(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schemaV1 + schemaV2); err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db}
	r := addRun(t, old, "old", "project", "订单服务的数据库使用 PostgreSQL。", true)
	m := Memory{ID: "old", Scope: r.Scope, Text: r.Input, Kind: "project_fact", Status: "active", Revision: 7, SourceRunID: r.ID, SourceType: "user_statement", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	payload, _ := json.MarshalIndent(m, "", "  ")
	if _, err = db.Exec("INSERT INTO memories(id,scope,text,status,payload) VALUES(?,?,?,?,?)", m.ID, m.Scope, m.Text, m.Status, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", m.ID, m.Revision, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", m.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO memory_fts(memory_id,tokens) VALUES(?,?)", m.ID, strings.Join(searchTokens(m.Text), " ")); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if ro, err := Open(home, true); err == nil {
		ro.Close()
		t.Fatal("v2 read-only opened before migration")
	} else if !strings.Contains(err.Error(), "migration") {
		t.Fatal(err)
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, query := range []string{"SELECT payload FROM memories WHERE id='old'", "SELECT payload FROM memory_revisions WHERE memory_id='old' AND revision=7"} {
		var got []byte
		if err = s.db.QueryRow(query).Scan(&got); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("migration changed original bytes %s %v", got, err)
		}
	}
	var count, version int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM memory_evidence WHERE memory_id='old' AND run_id='old'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("source evidence changed %d %v", count, err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("schema %d %v", version, err)
	}
	var annotationPayload []byte
	if err = s.db.QueryRow("SELECT payload FROM memory_annotations WHERE memory_id='old'").Scan(&annotationPayload); err != nil {
		t.Fatal(err)
	}
	var a annotation.Annotation
	if err = json.Unmarshal(annotationPayload, &a); err != nil || a.Version != annotation.Version || len(a.Objects) != 1 {
		t.Fatalf("backfilled annotation %+v %v", a, err)
	}
	got, err := s.Retrieve(testContext, "project", "订单服务的数据库", 10)
	if err != nil || !reflect.DeepEqual(recallIDs(got.Matches), []string{"old"}) {
		t.Fatalf("backfilled retrieval %+v %v", got, err)
	}
}

func TestSchemaV3BackfillRollsBackOnInvalidMemory(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schemaV1 + schemaV2); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO memories(id,scope,text,status,payload) VALUES('bad','p','source','active','{broken')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(home, false); err == nil {
		s.Close()
		t.Fatal("corrupt source migration succeeded")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("partial version %d %v", version, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('memory_annotations','memory_terms')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial derived tables %d %v", count, err)
	}
}

func TestRetrieveRejectsMissingAnnotationsAndFTSErrors(t *testing.T) {
	for _, broken := range []string{"annotation", "fts"} {
		t.Run(broken, func(t *testing.T) {
			s, _ := testStore(t)
			recallSeed(t, s, "orders", "project", "订单服务的数据库使用 PostgreSQL。")
			query := "DELETE FROM memory_annotations"
			if broken == "fts" {
				query = "DROP TABLE memory_fts"
			}
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Retrieve(testContext, "project", "订单服务的数据库", 10); err == nil {
				t.Fatal("broken retrieval silently degraded")
			}
		})
	}
}

func TestExpiredCollisionResolvesSingleRuneAliasWithoutReindex(t *testing.T) {
	s, _ := testStore(t)
	recallSeed(t, s, "orders", "project", "订单服务（简称甲）的端口为 8080。")
	recallSeed(t, s, "billing", "project", "账单服务（简称甲）的端口为 9090。")
	recallSeed(t, s, "fact", "project", "甲的数据库使用 PostgreSQL。")
	before, err := s.Retrieve(testContext, "project", "订单服务", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range before.Matches {
		if m.ID == "fact" {
			t.Fatal("ambiguous alias fact bound")
		}
	}
	if _, err = s.db.Exec("UPDATE memories SET expires_at=? WHERE id='billing'", timestamp(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	after, err := s.Retrieve(testContext, "project", "订单服务", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range after.Matches {
		if m.ID == "fact" {
			found = true
		}
	}
	if !found {
		t.Fatalf("newly unambiguous alias-only fact omitted: %+v", after)
	}
}

func TestPersistentRulesShareApplicabilityWithoutSearchLimit(t *testing.T) {
	s, _ := testStore(t)
	for _, seed := range []struct{ id, text, kind string }{
		{"global", "请始终用中文回复。", "preference"},
		{"orders", "订单服务的数据库必须使用 PostgreSQL。", "constraint"},
		{"billing", "账单服务的数据库必须使用 MySQL。", "constraint"},
		{"rotation", "密钥每九十天更换一次。", "constraint"},
	} {
		r := addRun(t, s, "persistent-"+seed.id, "project", seed.text, true)
		u := updateFor(r, seed.id)
		u.Memory.Kind = seed.kind
		if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{u}, decision); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Retrieve(testContext, "project", "订单服务的数据库", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recallIDs(got.Matches), []string{"orders"}) {
		t.Fatalf("search matches: %+v", got)
	}
	if !reflect.DeepEqual(recallIDs(got.Persistent), []string{"global", "orders"}) {
		t.Fatalf("persistent limit or applicability leak: %+v", got)
	}
	got, err = s.Retrieve(testContext, "project", "Investigate a parser failure.", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recallIDs(got.Persistent), []string{"global"}) {
		t.Fatalf("object/topic rules became global: %+v", got)
	}
	got, err = s.Retrieve(testContext, "project", "凭据的轮换周期", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recallIDs(got.Persistent), []string{"global", "rotation"}) {
		t.Fatalf("relevant topic rule omitted: %+v", got)
	}
}

func TestAmbiguousAliasConstraintCannotBypassThroughPersistence(t *testing.T) {
	s, _ := testStore(t)
	recallSeed(t, s, "orders", "project", "订单服务（简称核心）的端口为 8080。")
	recallSeed(t, s, "billing", "project", "账单服务（简称核心）的端口为 9090。")
	r := addRun(t, s, "alias-rule", "project", "核心的数据库必须使用 PostgreSQL。", true)
	u := updateFor(r, "rule")
	u.Memory.Kind = "constraint"
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{u}, decision); err != nil {
		t.Fatal(err)
	}
	got, err := s.Retrieve(testContext, "project", "数据库", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Persistent) != 0 || len(got.Matches) != 0 {
		t.Fatalf("ambiguous rule escaped applicability: %+v", got)
	}
}

func TestReadOnlyRetrievalUsesCurrentSchema(t *testing.T) {
	s, home := testStore(t)
	recallSeed(t, s, "orders", "project", "订单服务的数据库使用 PostgreSQL。")
	reader, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := reader.Retrieve(testContext, "project", "订单服务的数据库", 1)
	if err != nil || !reflect.DeepEqual(recallIDs(got.Matches), []string{"orders"}) {
		t.Fatalf("read-only retrieval %+v %v", got, err)
	}
}
