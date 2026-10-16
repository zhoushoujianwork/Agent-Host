package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileMemoryPublishPreservesEquivalentHistoricalJSON(t *testing.T) {
	s, _, _, _ := fileStore(t)
	r := addRun(t, s, "original-format", "scope", "Remember the original format.", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "original-format")}, decision); err != nil {
		t.Fatal(err)
	}
	f := s.files
	manifest, err := f.readManifest()
	if err != nil {
		t.Fatal(err)
	}
	entry := &manifest.Entries[0]
	path := filepath.Join(f.revisionPath(*entry, 1), "record.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Model a historical encoder's ordering and whitespace without changing data.
	var value map[string]any
	if err = json.Unmarshal(original, &value); err != nil {
		t.Fatal(err)
	}
	retained, _ := json.Marshal(value)
	if err = os.WriteFile(path, retained, 0600); err != nil {
		t.Fatal(err)
	}
	entry.Revisions[0].RecordSHA256 = fileHash(retained)
	if err = atomicJSON(filepath.Join(f.sourceDir, "current.json"), manifest, false); err != nil {
		t.Fatal(err)
	}
	r2 := addRun(t, s, "new-after-historical-format", "scope", "Remember another fact.", true)
	if err = s.FinalizeMemory(testContext, r2.ID, []MemoryUpdate{updateFor(r2, "new-after-historical-format")}, decision); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, retained) {
		t.Fatalf("historical bytes changed: %v", err)
	}
}

func TestSameJSONValueRejectsChangedData(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"revision":1}`, `{"revision":2}`},
		{`{"id":9007199254740992}`, `{"id":9007199254740993}`},
		{`{"a":1,"extra":true}`, `{"a":1}`},
		{`invalid`, `null`},
	} {
		if sameJSONValue([]byte(pair[0]), []byte(pair[1])) {
			t.Fatalf("accepted changed JSON: %v", pair)
		}
	}
}

func fileStore(t *testing.T) (*Store, string, string, string) {
	t.Helper()
	s, home := testStore(t)
	root := t.TempDir()
	memoryRoot, cacheRoot := filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")
	if _, err := s.MigrateFileMemory(testContext, memoryRoot, cacheRoot, "test-channel"); err != nil {
		t.Fatal(err)
	}
	return s, home, memoryRoot, cacheRoot
}

func TestFileMemoryMigrationPreservesHistoryAndRebuildsCache(t *testing.T) {
	s, home := testStore(t)
	r := addRun(t, s, "historical", "project-a", "请记住项目输出使用中文和 Markdown。", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "historical-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateMemory(testContext, "historical-memory", "obsolete preference"); err != nil {
		t.Fatal(err)
	}
	r2 := addRun(t, s, "active", "project-a", "我的偏好是使用简短的中文。", true)
	if err := s.FinalizeMemory(testContext, r2.ID, []MemoryUpdate{updateFor(r2, "active-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	memoryRoot, cacheRoot := filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")
	report, err := s.MigrateFileMemory(testContext, memoryRoot, cacheRoot, "dingtalk")
	if err != nil {
		t.Fatal(err)
	}
	if report.Memories != 2 || report.Revisions != 3 || report.Evidence != 2 || report.Domains != 1 {
		t.Fatalf("migration report: %+v", report)
	}
	if !s.FileMemoryEnabled() {
		t.Fatal("migration did not enable file memory")
	}
	retry, err := s.MigrateFileMemory(testContext, memoryRoot, cacheRoot, "dingtalk")
	if err != nil || retry.Digest != report.Digest || retry.Generation != report.Generation {
		t.Fatalf("retry changed history: %+v %v", retry, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(cacheRoot); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if !ro.FileMemoryEnabled() {
		t.Fatal("read-only open bypassed file backend")
	}
	got, err := ro.Search(testContext, "project-a", "中文", 10)
	if err != nil || len(got) != 1 || got[0].ID != "active-memory" {
		t.Fatalf("cache rebuilt recall: %+v %v", got, err)
	}
	old, err := ro.Memory(testContext, "historical-memory")
	if err != nil || old.Status != "invalidated" || old.Revision != 2 {
		t.Fatalf("invalidated history: %+v %v", old, err)
	}
	verified, err := ro.VerifyFileMemory(testContext)
	if err != nil || verified.Digest != report.Digest {
		t.Fatalf("verification mismatch: %+v %v", verified, err)
	}
	if err = ro.InvalidateMemory(testContext, "active-memory", "forbidden"); err == nil {
		t.Fatal("read-only file mutation succeeded")
	}
	legacy, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var revisions int
	if err = legacy.QueryRow("SELECT COUNT(*) FROM memory_revisions").Scan(&revisions); err != nil || revisions != 3 {
		t.Fatalf("legacy history changed: %d %v", revisions, err)
	}
}

func TestFileMemoryMutationIsolationCorruptionAndRollback(t *testing.T) {
	s, home, memoryRoot, _ := fileStore(t)
	r := addRun(t, s, "new-run", "scope", "请记住回复要简短。", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "new-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memory(testContext, "new-memory")
	if err != nil || m.Revision != 1 {
		t.Fatalf("new publication: %+v %v", m, err)
	}
	if _, err = s.WithPartition("other", "owner").Memory(testContext, m.ID); err == nil {
		t.Fatal("partition ownership bypass")
	}
	before, err := s.MemoryFingerprint(testContext, []string{"scope"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InvalidateMemoryRevision(testContext, m.ID, "user disabled", 1); err != nil {
		t.Fatal(err)
	}
	after, err := s.MemoryFingerprint(testContext, []string{"scope"})
	if err != nil || before == after {
		t.Fatalf("withdrawal fingerprint unchanged: %v", err)
	}
	legacy, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = legacy.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy memory got dual-write: %d %v", n, err)
	}
	legacy.Close()
	report, err := s.RollbackFileMemory(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if report.Memories != 1 || report.Revisions != 2 {
		t.Fatalf("rollback report: %+v", report)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if old.FileMemoryEnabled() {
		t.Fatal("rollback marker remains enabled")
	}
	restored, err := old.Memory(testContext, m.ID)
	if err != nil || restored.Status != "invalidated" || restored.Revision != 2 {
		t.Fatalf("lossy rollback: %+v %v", restored, err)
	}
	files, err := filepath.Glob(filepath.Join(memoryRoot, "domains", "*", "entries", "new-memory", "revisions", "2", "content.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files not retained: %v %v", files, err)
	}
}

func TestFileMemoryChangedDocumentFailsWithoutLegacyFallback(t *testing.T) {
	s, _, memoryRoot, _ := fileStore(t)
	r := addRun(t, s, "fact", "scope", "请记住使用中文。", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "fact")}, decision); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(memoryRoot, "domains", "*", "entries", "fact", "revisions", "1", "content.md"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	inflight := addRun(t, s, "inflight", "scope", "Observe an actual health result.", false)
	if err := os.WriteFile(paths[0], []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"中文", "no match"} {
		if _, err := s.Search(testContext, "scope", query, 10); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("corrupt file was silently indexed: %v", err)
		}
	}
	if _, err := s.Memory(testContext, "fact"); err == nil {
		t.Fatal("corrupt memory readable")
	}
	if _, err := s.VerifyFileMemory(testContext); err == nil {
		t.Fatal("corrupt memory verified")
	}
	call, result := toolEvent("observed-check", "Bash", "call", 1), toolEvent("observed-check", "Bash", "result", 2)
	call.RunID, result.RunID = inflight.ID, inflight.ID
	if err := s.AppendEvent(testContext, &call); err != nil {
		t.Fatalf("memory corruption blocked raw execution evidence: %v", err)
	}
	if err := s.AppendEvent(testContext, &result); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, inflight.ID, "completed", "observed actual result", "test", ""); err != nil {
		t.Fatalf("memory corruption blocked execution completion: %v", err)
	}
	run, err := s.Run(testContext, inflight.ID)
	if err != nil || run.ExecutionStatus != "completed" {
		t.Fatalf("execution receipt lost: %+v %v", run, err)
	}
	if err = s.FinalizeMemory(testContext, inflight.ID, []MemoryUpdate{updateFor(inflight, "unpublished")}, decision); err == nil {
		t.Fatal("corrupt memory allowed further publication")
	}
}

func TestFileMemoryConcurrentPublishAndStaleSnapshot(t *testing.T) {
	s, home, _, _ := fileStore(t)
	r := addRun(t, s, "one", "scope", "我的偏好是简短中文。", true)
	r2 := addRun(t, s, "two", "scope", "我的偏好是详细英文。", true)
	other, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, work := range []struct {
		s  *Store
		r  Run
		id string
	}{{s, r, "one"}, {other, r2, "two"}} {
		wg.Add(1)
		go func(work struct {
			s  *Store
			r  Run
			id string
		}) {
			defer wg.Done()
			failures <- work.s.FinalizeMemory(context.Background(), work.r.ID, []MemoryUpdate{updateFor(work.r, work.id)}, decision)
		}(work)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	memories, err := s.Memories(testContext, "scope")
	if err != nil || len(memories) != 2 {
		t.Fatalf("lost concurrent publication: %+v %v", memories, err)
	}
	if err = s.InvalidateMemoryRevision(testContext, "one", "disabled", 1); err != nil {
		t.Fatal(err)
	}
	if err = other.InvalidateMemoryRevision(testContext, "one", "stale", 1); err == nil {
		t.Fatal("stale parallel invalidation succeeded")
	}
}

func TestFileMemoryAcceptedExperienceIsSkillAndRevisable(t *testing.T) {
	s, _, memoryRoot, _ := fileStore(t)
	r := addRun(t, s, "experience", "scope", "Diagnose service health.", false)
	call := toolEvent("health", "Bash", "call", 1)
	result := toolEvent("health", "Bash", "result", 2)
	call.RunID, result.RunID = r.ID, r.ID
	if err := s.AppendEvent(testContext, &call); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(testContext, &result); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, r.ID, "completed", "healthy", "test", ""); err != nil {
		t.Fatal(err)
	}
	evidence, err := s.ToolEvidence(testContext, r.ID)
	if err != nil || len(evidence) != 1 {
		t.Fatalf("evidence: %+v %v", evidence, err)
	}
	draft := ExperienceDraft{Title: "Check service health", Summary: "Read the health endpoint and verify its status.", Conditions: "When investigating API health.", Steps: []string{"Read the health endpoint.", "Verify the healthy result."}, EvidenceIDs: []string{evidence[0].ID}}
	c := Candidate{ID: "candidate", RunID: r.ID, Scope: r.Scope, Draft: draft, Model: "test", Assessment: decision}
	if err = s.SaveCandidates(testContext, r.ID, []Candidate{c}, decision); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := s.MemoryFingerprint(testContext, []string{r.Scope})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "skill", Decision: decision, ExpectedRevisions: map[string]int{}, ExpectedGeneration: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(memoryRoot, "domains", "*", "skills", "skill", "revisions", "1", "SKILL.md"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil || !strings.HasPrefix(string(body), "---\nname: learned-") {
		t.Fatalf("skill format: %s %v", body, err)
	}
	draft.Summary = "Use the observed health status to diagnose availability."
	m, err = s.RestructureExperience(testContext, m.ID, 1, draft, decision, "Improve wording")
	if err != nil || m.Revision != 2 {
		t.Fatalf("skill revision: %+v %v", m, err)
	}
	report, err := s.VerifyFileMemory(testContext)
	if err != nil || report.Skills != 1 || report.Revisions != 2 {
		t.Fatalf("skill history: %+v %v", report, err)
	}
	var indexedText string
	if err = s.db.QueryRow("SELECT text FROM memories WHERE id=?", m.ID).Scan(&indexedText); err != nil || indexedText != m.Text {
		t.Fatalf("restructured dedup text stayed stale: %q %v", indexedText, err)
	}
	r2 := addRun(t, s, "experience-duplicate", "scope", "Check health again.", true)
	call2, result2 := toolEvent("health-second", "Bash", "call", 1), toolEvent("health-second", "Bash", "result", 2)
	call2.RunID, result2.RunID = r2.ID, r2.ID
	if err = s.AppendEvent(testContext, &call2); err != nil {
		t.Fatal(err)
	}
	if err = s.AppendEvent(testContext, &result2); err != nil {
		t.Fatal(err)
	}
	draft.EvidenceIDs = []string{"health-second"}
	c2 := Candidate{ID: "candidate-duplicate", RunID: r2.ID, Scope: r2.Scope, Draft: draft, Model: "test", Assessment: decision}
	if err = s.SaveCandidates(testContext, r2.ID, []Candidate{c2}, decision); err != nil {
		t.Fatal(err)
	}
	fingerprint, err = s.MemoryFingerprint(testContext, []string{r2.Scope})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptCandidate(testContext, c2.ID, CandidateAcceptance{MemoryID: "duplicate-new", Decision: decision, ExpectedRevisions: map[string]int{m.ID: m.Revision}, ExpectedGeneration: fingerprint}); err == nil || !strings.Contains(err.Error(), "exact memory text") {
		t.Fatalf("restructured exact duplicate admitted: %v", err)
	}
}

func TestFileMemoryRecoversPublicationBeforeOperationalCommit(t *testing.T) {
	s, home, _, _ := fileStore(t)
	r := addRun(t, s, "recover", "scope", "请记住输出使用中文。", true)
	f := s.files
	release, err := f.lock(testContext)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("simulated interruption before operational commit")
	err = s.sqlTransaction(testContext, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM file_operation_log"); err != nil {
			return err
		}
		m := updateFor(r, "recovered-memory").Memory
		m.AgentID, m.AudienceID = DefaultAgentID, OwnerAudienceID
		m.Status, m.SourceType, m.Revision, m.CreatedAt = "active", "user_statement", 1, time.Now().UTC()
		if err := persistMemory(testContext, tx, m, true); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", m.ID, r.ID); err != nil {
			return err
		}
		run, err := s.readRun(testContext, tx, r.ID)
		if err != nil {
			return err
		}
		run.MemoryStatus = "saved"
		run.Decision = decision
		if err = writeRun(testContext, tx, run); err != nil {
			return err
		}
		snapshot, err := captureFileSnapshot(testContext, tx)
		if err != nil {
			return err
		}
		operations, err := readFileOperations(testContext, tx)
		if err != nil {
			return err
		}
		if _, err = f.publish(snapshot, operations); err != nil {
			return err
		}
		return interrupted
	})
	if err = errors.Join(err, release()); !errors.Is(err, interrupted) {
		t.Fatalf("did not simulate rollback: %v", err)
	}
	// Host error handling follows a failed publication immediately. Recover the
	// committed file receipt before it can replace saved status with failure.
	if err = s.MemoryFailure(testContext, r.ID, "post-publication cache commit failure"); err == nil {
		t.Fatal("follow-up failure overwrote committed publication")
	}
	receipt, err := s.Run(testContext, r.ID)
	if err != nil || receipt.MemoryStatus != "saved" || receipt.MemoryError != "" {
		t.Fatalf("follow-up corrupted recovery receipt: %+v %v", receipt, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	got, err := recovered.Memory(testContext, "recovered-memory")
	if err != nil || got.Revision != 1 {
		t.Fatalf("publication recovery lost memory: %+v %v", got, err)
	}
	run, err := recovered.Run(testContext, r.ID)
	if err != nil || run.MemoryStatus != "saved" {
		t.Fatalf("operational receipt not repaired: %+v %v", run, err)
	}
	if err = recovered.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "different-id")}, decision); err == nil {
		t.Fatal("recovered finalized operation was replayed")
	}
	report, err := recovered.VerifyFileMemory(testContext)
	if err != nil || report.Memories != 1 || report.Revisions != 1 || report.Evidence != 1 {
		t.Fatalf("retry duplicated publication: %+v %v", report, err)
	}
}

func TestFileMemoryCacheEditsCannotBecomeAuthoritative(t *testing.T) {
	s, _, _, _ := fileStore(t)
	r := addRun(t, s, "cache-fact", "scope", "请记住使用中文。", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "cache-fact")}, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM memories WHERE id='cache-fact'"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memory(testContext, "cache-fact")
	if err != nil || m.Text != r.Input {
		t.Fatalf("cache deletion changed published knowledge: %+v %v", m, err)
	}
	report, err := s.VerifyFileMemory(testContext)
	if err != nil || report.Generation != 2 {
		t.Fatalf("cache repair published a knowledge version: %+v %v", report, err)
	}
	if err = s.InvalidateMemory(testContext, m.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memories SET status='active',text='invented' WHERE id=?", m.ID); err != nil {
		t.Fatal(err)
	}
	result, err := s.Retrieve(testContext, "scope", "中文", 10)
	if err != nil || len(result.Matches) != 0 || len(result.Persistent) != 0 {
		t.Fatalf("cache columns reenabled withdrawn memory: %+v %v", result, err)
	}
}

func TestFileMemoryRecallPastLegacyRegistryLimit(t *testing.T) {
	s, _ := testStore(t)
	const count = 300
	err := s.sqlTransaction(testContext, func(tx *sql.Tx) error {
		for i := 0; i < count; i++ {
			m := Memory{ID: fmt.Sprintf("fact-%04d", i), AgentID: DefaultAgentID, AudienceID: OwnerAudienceID, Scope: "large", Kind: "project_fact", Text: fmt.Sprintf("API service api-%04d uses health checking.", i), Status: "active", Revision: 1, SourceRunID: "historical-import", SourceType: "user_statement", CreatedAt: time.Now().UTC()}
			b, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO memories(id,agent_id,audience_id,sender_id,scope,text,status,payload) VALUES(?,?,?,?,?,?,?,?)", m.ID, m.AgentID, m.AudienceID, "", m.Scope, m.Text, m.Status, b); err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", m.ID, 1, b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err = s.MigrateFileMemory(testContext, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory"), "large"); err != nil {
		t.Fatal(err)
	}
	all, err := s.Memories(testContext, "large")
	if err != nil || len(all) != count {
		t.Fatalf("legacy memory limit remained: %d %v", len(all), err)
	}
	result, err := s.Retrieve(testContext, "large", "api-0299", 10)
	if err != nil || len(result.Matches) == 0 {
		t.Fatalf("large scope retrieval: %+v %v", result, err)
	}
}

func TestFileMemoryFingerprintChangesWhenKnowledgeExpires(t *testing.T) {
	s, _, _, _ := fileStore(t)
	c := experienceCandidate(t, s, "expiry", "scope")
	saveCandidate(t, s, c)
	fingerprint, err := s.MemoryFingerprint(testContext, []string{c.Scope})
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if _, err = s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "expiring-skill", Decision: decision, ExpectedRevisions: map[string]int{}, ExpectedGeneration: fingerprint, ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	var before, after string
	err = s.transaction(testContext, func(tx *sql.Tx) error {
		var err error
		before, err = memoryFingerprintTxAt(testContext, tx, []string{c.Scope}, DefaultAgentID, OwnerAudienceID, expires.Add(-time.Second))
		if err != nil {
			return err
		}
		after, err = memoryFingerprintTxAt(testContext, tx, []string{c.Scope}, DefaultAgentID, OwnerAudienceID, expires.Add(time.Second))
		return err
	})
	if err != nil || before == after {
		t.Fatalf("expiration did not rotate knowledge snapshot: %s %s %v", before, after, err)
	}
}

func TestNativeSkillLoadCannotSupportLearnedExperience(t *testing.T) {
	evidence := []ToolEvidence{{ID: "loaded-skill", Name: "Skill", Input: json.RawMessage(`{"skill":"learned-diagnostics"}`), Result: json.RawMessage(`{"loaded":true}`)}, {ID: "real-check", Name: "Bash", Input: json.RawMessage(`{"command":"health-check"}`), Result: json.RawMessage(`{"healthy":true}`)}}
	eligible := ExecutionExperienceEvidence(evidence)
	if len(eligible) != 1 || eligible[0].ID != "real-check" {
		t.Fatalf("reference load became execution evidence: %+v", eligible)
	}
	draft := ExperienceDraft{Title: "A skill was loaded", Summary: "Loading an existing skill is not new verified execution.", Conditions: "For reference lookup.", Steps: []string{"Load an existing skill."}, EvidenceIDs: []string{"loaded-skill"}}
	if err := validateDraft(draft, evidence); err == nil {
		t.Fatal("self-reinforcing skill publication accepted")
	}
}

func TestFileMemoryUserStatementRejectsStaleComparison(t *testing.T) {
	s, home, _, _ := fileStore(t)
	base := addRun(t, s, "base-compare", "scope", "请记住输出使用中文。", true)
	if err := s.FinalizeMemory(testContext, base.ID, []MemoryUpdate{updateFor(base, "base-compare")}, decision); err != nil {
		t.Fatal(err)
	}
	pending := addRun(t, s, "pending-compare", "scope", "请记住输出简短。", true)
	generation, err := s.MemoryFingerprint(testContext, []string{pending.Scope})
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = other.InvalidateMemory(testContext, "base-compare", "changed during comparison"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeMemory(testContext, pending.ID, []MemoryUpdate{updateFor(pending, "pending-compare")}, decision, generation); err == nil || !strings.Contains(err.Error(), "snapshot changed") {
		t.Fatalf("stale Jev decision published: %v", err)
	}
	if _, err = s.Memory(testContext, "pending-compare"); err == nil {
		t.Fatal("stale comparison wrote memory")
	}
	run, err := s.Run(testContext, pending.ID)
	if err != nil || run.MemoryStatus != "pending" {
		t.Fatalf("stale comparison changed source run: %+v %v", run, err)
	}
	current, err := s.MemoryFingerprint(testContext, []string{pending.Scope})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeMemory(testContext, pending.ID, []MemoryUpdate{updateFor(pending, "pending-compare")}, decision, current); err != nil {
		t.Fatalf("fresh comparison could not publish: %v", err)
	}
}

func TestFileMemoryExperienceReceiptPrecedesHostFailureFollowup(t *testing.T) {
	s, _, _, _ := fileStore(t)
	c := experienceCandidate(t, s, "recovered-experience", "scope")
	saveCandidate(t, s, c)
	c, err := s.GetCandidate(testContext, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := s.files
	release, err := f.lock(testContext)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("simulated candidate publication commit interruption")
	err = s.sqlTransaction(testContext, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM file_operation_log"); err != nil {
			return err
		}
		now := time.Now().UTC()
		m := Memory{ID: "recovered-skill", AgentID: c.AgentID, AudienceID: c.AudienceID, Scope: c.Scope, Kind: "procedure", Text: ExperienceText(c.Draft), Status: "active", Revision: 1, SourceRunID: c.RunID, SourceType: "reviewed_experience", CandidateID: c.ID, CreatedAt: now, Conditions: c.Draft.Conditions, Steps: c.Draft.Steps, EvidenceIDs: c.Draft.EvidenceIDs, Entities: c.Entities, ReviewNote: "supported"}
		if err := persistMemory(testContext, tx, m, true); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", m.ID, c.RunID); err != nil {
			return err
		}
		candidate, err := s.readCandidate(testContext, tx, c.ID)
		if err != nil {
			return err
		}
		candidate.Status, candidate.MemoryID, candidate.ReviewedAt = "accepted", m.ID, &now
		if err = writeCandidate(testContext, tx, candidate, false); err != nil {
			return err
		}
		run, err := s.readRun(testContext, tx, c.RunID)
		if err != nil {
			return err
		}
		if err = finishReview(testContext, tx, run); err != nil {
			return err
		}
		snapshot, err := captureFileSnapshot(testContext, tx)
		if err != nil {
			return err
		}
		ops, err := readFileOperations(testContext, tx)
		if err != nil {
			return err
		}
		if _, err = f.publish(snapshot, ops); err != nil {
			return err
		}
		return interrupted
	})
	if err = errors.Join(err, release()); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	if err = s.ExperiencePublicationFailure(testContext, c.RunID, "cache commit failed after publish"); err == nil {
		t.Fatal("follow-up failure overwrote published candidate")
	}
	run, err := s.Run(testContext, c.RunID)
	if err != nil || run.ExperienceStatus != "reviewed" || run.ExperienceError != "" {
		t.Fatalf("candidate receipt recovery failed: %+v %v", run, err)
	}
	accepted, err := s.GetCandidate(testContext, c.ID)
	if err != nil || accepted.Status != "accepted" || accepted.MemoryID != "recovered-skill" {
		t.Fatalf("candidate review receipt lost: %+v %v", accepted, err)
	}
	if _, err = s.VerifyFileMemory(testContext); err != nil {
		t.Fatalf("host failure follow-up broke future recovery: %v", err)
	}
}

func TestFileMemoryRollbackExistingHandleRecoversPendingReceipt(t *testing.T) {
	s, home, _, _ := fileStore(t)
	r := addRun(t, s, "rollback-interrupted", "scope", "请记住使用简短中文。", true)
	f := s.files
	release, err := f.lock(testContext)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("simulated interruption before rollback")
	err = s.sqlTransaction(testContext, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM file_operation_log"); err != nil {
			return err
		}
		m := updateFor(r, "rollback-interrupted-memory").Memory
		m.AgentID, m.AudienceID = DefaultAgentID, OwnerAudienceID
		m.Status, m.SourceType, m.Revision, m.CreatedAt = "active", "user_statement", 1, time.Now().UTC()
		if err := persistMemory(testContext, tx, m, true); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", m.ID, r.ID); err != nil {
			return err
		}
		run, err := s.readRun(testContext, tx, r.ID)
		if err != nil {
			return err
		}
		run.MemoryStatus, run.Decision = "saved", decision
		if err = writeRun(testContext, tx, run); err != nil {
			return err
		}
		snapshot, err := captureFileSnapshot(testContext, tx)
		if err != nil {
			return err
		}
		ops, err := readFileOperations(testContext, tx)
		if err != nil {
			return err
		}
		if _, err = f.publish(snapshot, ops); err != nil {
			return err
		}
		return interrupted
	})
	if err = errors.Join(err, release()); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	before, err := s.Run(testContext, r.ID)
	if err != nil || before.MemoryStatus != "pending" {
		t.Fatalf("did not leave a pending receipt: %+v %v", before, err)
	}
	if _, err = s.RollbackFileMemory(testContext); err != nil {
		t.Fatalf("existing-handle rollback failed: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if legacy.FileMemoryEnabled() {
		t.Fatal("rollback did not retire file backend")
	}
	m, err := legacy.Memory(testContext, "rollback-interrupted-memory")
	if err != nil || m.Revision != 1 || m.Text != r.Input {
		t.Fatalf("rollback lost published data: %+v %v", m, err)
	}
	receipt, err := legacy.Run(testContext, r.ID)
	if err != nil || receipt.MemoryStatus != "saved" || receipt.MemoryError != "" || !equalJSON(receipt.Decision, decision) {
		t.Fatalf("rollback lost publication receipt: %+v %v", receipt, err)
	}
}
