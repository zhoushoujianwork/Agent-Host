package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/event"
)

var testContext = context.Background()

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, home
}

func addRun(t *testing.T, s *Store, id, scope, input string, completed bool) Run {
	t.Helper()
	session := Session{ID: "session-" + id, Scope: scope, Model: "test", NativeSessionID: "native-" + id}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: id, SessionID: session.ID, Scope: scope, Input: input, Agent: "claude", RequestedModel: "test", NativeSessionID: session.NativeSessionID}
	if err := s.CreateRun(testContext, run); err != nil {
		t.Fatal(err)
	}
	if completed {
		if err := s.CompleteRun(testContext, id, "completed", "answer", "resolved-model", ""); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func updateFor(run Run, id string) MemoryUpdate {
	return MemoryUpdate{Memory: Memory{ID: id, Scope: run.Scope, Kind: "preference", Text: run.Input, SourceRunID: run.ID}}
}

var decision = json.RawMessage(`{"model":"test","choice":"save"}`)

func TestMemoryPersistenceFTSAndScope(t *testing.T) {
	s, home := testStore(t)
	r := addRun(t, s, "r1", "project-a", "我的输出偏好是使用中文，文件采用 Markdown。", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "m1")}, decision); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"中文", "输出", "Markdown", `"中文" OR * : ( )`} {
		found, err := s.Search(testContext, "project-a", query, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 || found[0].ID != "m1" {
			t.Fatalf("query %q: %+v", query, found)
		}
	}
	found, err := s.Search(testContext, "project-b", "中文", 10)
	if err != nil || len(found) != 0 {
		t.Fatalf("cross-scope search: %+v %v", found, err)
	}
	readonly, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	m, err := readonly.Memory(testContext, "m1")
	if err != nil || m.Text != r.Input || m.Revision != 1 || m.SourceRunID != r.ID {
		t.Fatalf("persisted source: %+v %v", m, err)
	}
	run, err := readonly.Run(testContext, r.ID)
	if err != nil || run.MemoryStatus != "saved" || run.ExecutionStatus != "completed" || run.AcceptanceStatus != "unverified" || len(run.Decision) == 0 {
		t.Fatalf("receipt: %+v %v", run, err)
	}
	if err = readonly.CreateSession(testContext, Session{ID: "ro", Scope: "project-a", NativeSessionID: "ro"}); err == nil {
		t.Fatal("readonly write succeeded")
	}
}

func TestMemorySourceValidationIsAtomic(t *testing.T) {
	s, _ := testStore(t)
	r := addRun(t, s, "r1", "a", "请记住项目使用 Go。", true)
	for name, change := range map[string]func(*Memory){
		"assistant assertion": func(m *Memory) { m.Text = "The assistant claims deployment succeeded." },
		"wrong scope":         func(m *Memory) { m.Scope = "b" },
		"wrong source":        func(m *Memory) { m.SourceRunID = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := updateFor(r, "bad")
			change(&bad.Memory)
			if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "good"), bad}, decision); err == nil {
				t.Fatal("bad provenance accepted")
			}
			if _, err := s.Memory(testContext, "good"); err == nil {
				t.Fatal("transaction partly committed")
			}
		})
	}
	failed := addRun(t, s, "r2", "a", "请记住一个未完成的请求。", false)
	if err := s.CompleteRun(testContext, failed.ID, "failed", "", "test", "native error"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeMemory(testContext, failed.ID, []MemoryUpdate{updateFor(failed, "bad2")}, decision); err == nil {
		t.Fatal("failed execution promoted memory")
	}
}

func TestMemoryDedupConflictRevisionsAndRetry(t *testing.T) {
	s, _ := testStore(t)
	r1 := addRun(t, s, "r1", "a", "我的输出偏好是中文。", true)
	if err := s.FinalizeMemory(testContext, r1.ID, []MemoryUpdate{updateFor(r1, "m1")}, decision); err != nil {
		t.Fatal(err)
	}
	r2 := addRun(t, s, "r2", "a", r1.Input, true)
	if err := s.MemoryFailure(testContext, r2.ID, "Jev was unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeMemory(testContext, r2.ID, []MemoryUpdate{updateFor(r2, "ignored-duplicate-id")}, decision); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(testContext, r2.ID)
	if err != nil || run.MemoryStatus != "unchanged" || run.MemoryError != "" {
		t.Fatalf("dedup receipt: %+v %v", run, err)
	}
	if err = s.FinalizeMemory(testContext, r2.ID, nil, decision); err == nil {
		t.Fatal("successful finalization replay accepted")
	}
	if err = s.MemoryFailure(testContext, r2.ID, "renderer failed after durable commit"); err == nil {
		t.Fatal("committed memory outcome overwritten by reporting failure")
	}
	run, err = s.Run(testContext, r2.ID)
	if err != nil || run.MemoryStatus != "unchanged" {
		t.Fatalf("post-commit reporting changed memory outcome %+v %v", run, err)
	}
	var evidence int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM memory_evidence WHERE memory_id='m1'").Scan(&evidence); err != nil || evidence != 2 {
		t.Fatalf("dedup evidence %d %v", evidence, err)
	}
	r3 := addRun(t, s, "r3", "a", "我的输出偏好是英文。", true)
	u := updateFor(r3, "m2")
	u.ConflictIDs = []string{"m1"}
	if err = s.FinalizeMemory(testContext, r3.ID, []MemoryUpdate{u}, decision); err != nil {
		t.Fatal(err)
	}
	m1, err := s.Memory(testContext, "m1")
	if err != nil || m1.Status != "conflicted" || m1.Revision != 2 {
		t.Fatalf("old conflict revision %+v %v", m1, err)
	}
	m2, err := s.Memory(testContext, "m2")
	if err != nil || m2.Status != "conflicted" || m2.Revision != 1 {
		t.Fatalf("new conflict revision %+v %v", m2, err)
	}
	found, err := s.Search(testContext, "a", "输出", 10)
	if err != nil || len(found) != 0 {
		t.Fatalf("conflicted recalled %+v %v", found, err)
	}
	var old []byte
	if err = s.db.QueryRow("SELECT payload FROM memory_revisions WHERE memory_id='m1' AND revision=1").Scan(&old); err != nil {
		t.Fatal(err)
	}
	var previous Memory
	if err = json.Unmarshal(old, &previous); err != nil || previous.Status != "active" || previous.Text != r1.Input {
		t.Fatalf("immutable previous revision %+v %v", previous, err)
	}
	r4 := addRun(t, s, "r4", "a", r1.Input, true)
	repeated := updateFor(r4, "new-id-for-same-statement")
	repeated.ConflictIDs = []string{"m1"}
	if err = s.FinalizeMemory(testContext, r4.ID, []MemoryUpdate{repeated}, decision); err != nil {
		t.Fatalf("repeat of quarantined statement failed: %v", err)
	}
	m1, err = s.Memory(testContext, "m1")
	if err != nil || m1.Status != "conflicted" || m1.Revision != 2 {
		t.Fatalf("repeated conflict changed canonical record %+v %v", m1, err)
	}
}

func TestCrossScopeConflictRejected(t *testing.T) {
	s, _ := testStore(t)
	r1 := addRun(t, s, "r1", "a", "偏好中文", true)
	r2 := addRun(t, s, "r2", "b", "偏好英文", true)
	if err := s.FinalizeMemory(testContext, r1.ID, []MemoryUpdate{updateFor(r1, "m1")}, decision); err != nil {
		t.Fatal(err)
	}
	u := updateFor(r2, "m2")
	u.ConflictIDs = []string{"m1"}
	if err := s.FinalizeMemory(testContext, r2.ID, []MemoryUpdate{u}, decision); err == nil {
		t.Fatal("cross-scope conflict accepted")
	}
	m, err := s.Memory(testContext, "m1")
	if err != nil || m.Status != "active" {
		t.Fatalf("old memory changed %+v %v", m, err)
	}
	if _, err = s.Memory(testContext, "m2"); err == nil {
		t.Fatal("new memory persisted after rejected conflict")
	}
}

func TestEventsContextAndRecovery(t *testing.T) {
	s, _ := testStore(t)
	memoryRun := addRun(t, s, "memory", "a", "保存中文偏好", true)
	if err := s.FinalizeMemory(testContext, memoryRun.ID, []MemoryUpdate{updateFor(memoryRun, "m")}, decision); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Memory(testContext, "m")
	r := addRun(t, s, "r1", "a", "继续", false)
	if err := s.SaveContext(testContext, r.ID, []Memory{m}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := event.Event{RunID: r.ID, Type: "text.delta", Delta: fmt.Sprint(i)}
			if err := s.AppendEvent(testContext, &e); err != nil {
				t.Error(err)
			}
			if e.Seq < 1 || e.Version != event.Version || e.At.IsZero() {
				t.Errorf("event not assigned: %+v", e)
			}
		}(i)
	}
	wg.Wait()
	run, err := s.Run(testContext, r.ID)
	if err != nil || len(run.Events) != 20 {
		t.Fatalf("events %+v %v", run.Events, err)
	}
	for i, e := range run.Events {
		if e.Seq != int64(i+1) || e.SessionID != r.SessionID {
			t.Fatalf("sequence %+v", e)
		}
	}
	previous, err := s.SessionMemories(testContext, r.SessionID)
	if err != nil || len(previous) != 1 || previous[0].Revision != 1 {
		t.Fatalf("session snapshots %+v %v", previous, err)
	}
	pending := addRun(t, s, "pending", "a", "等待记忆", true)
	if err = s.Recover(testContext); err != nil {
		t.Fatal(err)
	}
	run, err = s.Run(testContext, r.ID)
	if err != nil || run.ExecutionStatus != "unknown" || run.MemoryStatus != "skipped" || !strings.Contains(run.Error, "not replayed") {
		t.Fatalf("recovery %+v %v", run, err)
	}
	if err = s.SessionCanResume(testContext, r.SessionID); err == nil {
		t.Fatal("unknown session resumed")
	}
	if err = s.SessionCanResume(testContext, pending.SessionID); err == nil {
		t.Fatal("pending memory session resumed")
	}
	jobs, err := s.PendingMemory(testContext)
	if err != nil || len(jobs) != 1 || jobs[0].ID != pending.ID {
		t.Fatalf("pending %+v %v", jobs, err)
	}
	if err = s.CompleteRun(testContext, r.ID, "completed", "invented", "test", ""); err == nil {
		t.Fatal("unknown execution rewritten")
	}
}

func TestOwnershipLockAndReadOnlyOpen(t *testing.T) {
	home := t.TempDir()
	release, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Acquire(home); err == nil {
		t.Fatal("second owner acquired lock")
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	release, err = Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = Open(home, true); err == nil {
		t.Fatal("readonly opened missing database")
	}
}

func TestOwnershipLockReleasedOnProcessExit(t *testing.T) {
	const helper = "AGENT_HOST_STATE_LOCK_HELPER"
	if home := os.Getenv(helper); home != "" {
		if _, err := Acquire(home); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Deliberately bypass release, exactly as a process exit does.
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnershipLockReleasedOnProcessExit$")
	cmd.Env = append(os.Environ(), helper+"="+home)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock owner subprocess: %s: %v", output, err)
	}
	release, err := Acquire(home)
	if err != nil {
		t.Fatalf("lock survived process exit: %v", err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
}

func TestStateFilePermissions(t *testing.T) {
	_, home := testStore(t)
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("%s grants group/other access: %o", name, info.Mode().Perm())
		}
	}
}

func TestSemanticDuplicateEvidence(t *testing.T) {
	s, _ := testStore(t)
	r1 := addRun(t, s, "r1", "a", "我偏好中文回答", true)
	if err := s.FinalizeMemory(testContext, r1.ID, []MemoryUpdate{updateFor(r1, "m1")}, decision); err != nil {
		t.Fatal(err)
	}
	r2 := addRun(t, s, "r2", "a", "我的回复语言偏好是中文", true)
	u := updateFor(r2, "unused")
	u.DuplicateIDs = []string{"m1", "m1"}
	if err := s.FinalizeMemory(testContext, r2.ID, []MemoryUpdate{u}, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Memory(testContext, "unused"); err == nil {
		t.Fatal("semantic duplicate created new canonical record")
	}
	run, err := s.Run(testContext, r2.ID)
	if err != nil || run.MemoryStatus != "unchanged" {
		t.Fatalf("duplicate receipt %+v %v", run, err)
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM memory_evidence WHERE memory_id='m1'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("evidence count %d %v", count, err)
	}
	m, err := s.Memory(testContext, "m1")
	if err != nil || m.Text != r1.Input || m.Revision != 1 || m.SourceRunID != r1.ID {
		t.Fatalf("canonical source modified %+v %v", m, err)
	}
}

func TestSemanticDuplicateValidationRollback(t *testing.T) {
	s, _ := testStore(t)
	ra := addRun(t, s, "a", "a", "项目偏好中文", true)
	rb := addRun(t, s, "b", "b", "另一个项目偏好中文", true)
	for _, r := range []Run{ra, rb} {
		if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "m"+r.ID)}, decision); err != nil {
			t.Fatal(err)
		}
	}
	r := addRun(t, s, "candidate", "a", "回复应优先中文", true)
	for name, ids := range map[string][]string{"wrong scope": {"ma", "mb"}, "unknown ID": {"ma", "missing"}} {
		t.Run(name, func(t *testing.T) {
			u := updateFor(r, "unused")
			u.DuplicateIDs = ids
			if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{u}, decision); err == nil {
				t.Fatal("invalid duplicate accepted")
			}
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM memory_evidence WHERE run_id=?", r.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial evidence persisted %d %v", count, err)
			}
		})
	}
	u := updateFor(r, "unused")
	u.DuplicateIDs = []string{"ma"}
	u.ConflictIDs = []string{"ma"}
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{u}, decision); err == nil {
		t.Fatal("duplicate bypassed conflict")
	}
}

func TestTokenization(t *testing.T) {
	want := "输出 出偏 偏好 中文 markdown go 123"
	if got := strings.Join(searchTokens("输出偏好：中文 / Markdown GO 123\""), " "); got != want {
		t.Fatalf("tokens %q", got)
	}
	if got := searchTokens("中 ! : (*)"); len(got) != 0 {
		t.Fatalf("single CJK rune tokens %+v", got)
	}
}

func TestSessionResumeUsesLatestRun(t *testing.T) {
	s, _ := testStore(t)
	r := addRun(t, s, "r1", "a", "one", true)
	r.ID = "r2"
	r.Input = "two"
	r.CreatedAt = time.Now().Add(time.Second)
	if err := s.CreateRun(testContext, r); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, r.ID, "canceled", "", "test", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := s.SessionCanResume(testContext, r.SessionID); err == nil {
		t.Fatal("canceled latest run resumed")
	}
}
