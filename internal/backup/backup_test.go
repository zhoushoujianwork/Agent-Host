package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-host/internal/state"
	"filippo.io/age"
)

func backupFixture(t *testing.T) (string, Policy) {
	t.Helper()
	base := t.TempDir()
	base, e := filepath.EvalSymlinks(base)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(base, "host")
	if e = os.MkdirAll(filepath.Join(root, "config"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "config", "host.json"), []byte(`{"secret":"private-token","source_root":"/external/docs"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = Init(root, filepath.Join(base, "archives"), filepath.Join(base, "keys", "recovery.key"), "Asia/Singapore"); e != nil {
		t.Fatal(e)
	}
	p, e := LoadPolicy(root)
	if e != nil {
		t.Fatal(e)
	}
	return root, p
}

func TestEncryptedTarRoundTripAndFailurePreservesRecovery(t *testing.T) {
	ctx := context.Background()
	root, p := backupFixture(t)
	home := filepath.Join(root, "channels", "test", "state")
	s, e := state.Open(home, false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.MigrateFileMemory(ctx, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory"), "test"); e != nil {
		t.Fatal(e)
	}
	session := state.Session{ID: "session", Scope: "private-project", NativeSessionID: "native"}
	if e = s.CreateSession(ctx, session); e != nil {
		t.Fatal(e)
	}
	run := state.Run{ID: "run", SessionID: session.ID, Scope: session.Scope, NativeSessionID: "native", Input: "Keep answers concise."}
	if e = s.CreateRun(ctx, run); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteRun(ctx, run.ID, "completed", "Done", "test", ""); e != nil {
		t.Fatal(e)
	}
	update := state.MemoryUpdate{Memory: state.Memory{ID: "memory", Scope: run.Scope, Kind: "preference", Text: run.Input, SourceRunID: run.ID}}
	if e = s.FinalizeMemory(ctx, run.ID, []state.MemoryUpdate{update}, json.RawMessage(`{"choice":"save"}`)); e != nil {
		t.Fatal(e)
	}
	if e = s.InvalidateMemory(ctx, "memory", "User disabled"); e != nil {
		t.Fatal(e)
	}
	point, e := Create(ctx, root, false)
	if e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(p.Destination, point.Archive)
	b, e := os.ReadFile(archive)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(point.Archive, ".tar.gz.age") || !strings.HasPrefix(string(b), "age-encryption.org/") || strings.Contains(string(b), "private-token") {
		t.Fatal("not an encrypted tar archive")
	}
	m, e := Verify(ctx, archive, p.IdentityFile)
	if e != nil || len(m.Sources) != 1 {
		t.Fatalf("verification: %+v %v", m, e)
	}
	if len(m.Dependencies) == 0 {
		t.Fatal("external dependencies not reported")
	}
	target := filepath.Join(filepath.Dir(root), "restored")
	if _, e = Restore(ctx, archive, p.IdentityFile, target); e != nil {
		t.Fatal(e)
	}
	ro, e := state.Open(filepath.Join(target, "channels", "test", "state"), true)
	if e != nil {
		t.Fatal(e)
	}
	got, e := ro.Memory(ctx, "memory")
	if e != nil || got.Revision != 2 || got.Status != "invalidated" {
		t.Fatalf("current memory differs: %+v %v", got, e)
	}
	report, e := ro.VerifyFileMemory(ctx)
	if e != nil || report.Revisions != 1 {
		t.Fatalf("history was not compacted: %+v %v", report, e)
	}
	ro.Close()
	restoredPolicy, e := LoadPolicy(target)
	if e != nil || restoredPolicy.Enabled {
		t.Fatalf("restored automatic jobs enabled: %+v %v", restoredPolicy, e)
	}
	if _, e = Restore(ctx, archive, p.IdentityFile, target); e == nil {
		t.Fatal("overwrote existing directory")
	}
	second, e := Create(ctx, root, false)
	if e != nil || second.Archive != point.Archive {
		t.Fatalf("unchanged data duplicated: %+v %v", second, e)
	}
	key, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	wrong := filepath.Join(filepath.Dir(root), "wrong.key")
	if e = os.WriteFile(wrong, []byte(key.String()), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, archive, wrong); e == nil {
		t.Fatal("accepted wrong key")
	}
	corrupt := filepath.Join(filepath.Dir(root), "corrupt.age")
	b[len(b)-1] ^= 1
	if e = os.WriteFile(corrupt, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, corrupt, p.IdentityFile); e == nil {
		t.Fatal("accepted corrupted archive")
	}
	before, e := ReadStatus(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("/external/not-copied", filepath.Join(root, "config", "unsafe")); e != nil {
		t.Fatal(e)
	}
	if _, e = Create(ctx, root, false); e == nil {
		t.Fatal("silently skipped symlink")
	}
	after, e := ReadStatus(root)
	if e != nil || after.LastError == "" || !after.LastSuccess.Equal(before.LastSuccess) {
		t.Fatalf("lost last successful recovery point: %+v %v", after, e)
	}
	if _, e = Verify(ctx, archive, p.IdentityFile); e != nil {
		t.Fatal("failure damaged prior archive", e)
	}
}

func TestRetentionAndDailyDeadline(t *testing.T) {
	loc, e := time.LoadLocation("Asia/Singapore")
	if e != nil {
		t.Fatal(e)
	}
	var points []Point
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)
	for i := 0; i < 80; i++ {
		points = append(points, Point{CheckedAt: start.AddDate(0, 0, i), Archive: "shared"})
	}
	kept := retain(points, loc)
	if len(kept) < 7 || len(kept) > 11 {
		t.Fatalf("retention size %d", len(kept))
	}
	for _, p := range points[len(points)-7:] {
		found := false
		for _, k := range kept {
			found = found || p.CheckedAt.Equal(k.CheckedAt)
		}
		if !found {
			t.Fatal("lost recent day")
		}
	}
	root, p := backupFixture(t)
	point, e := Create(context.Background(), root, false)
	if e != nil {
		t.Fatal(e)
	}
	// Corrupting configuration proves the not-due path does not perform a capture.
	if e = os.WriteFile(filepath.Join(root, "config", "host.json"), []byte("broken json"), 0600); e != nil {
		t.Fatal(e)
	}
	due, e := Create(context.Background(), root, true)
	if e != nil || due.Archive != "" {
		t.Fatalf("ran before deadline: %+v %v", due, e)
	}
	status, e := ReadStatus(root)
	if e != nil || status.Latest.Archive != point.Archive {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(p.Destination, point.Archive)); e != nil {
		t.Fatal(e)
	}
}

func TestArchiveRejectsUnsafeMembersAndPolicyPaths(t *testing.T) {
	root, p := backupFixture(t)
	key, e := loadIdentity(p.IdentityFile)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"../escape", "/absolute", "workspace/config/../escape"} {
		data := payload{manifest: Manifest{Version: 1}, files: map[string][]byte{name: []byte("unsafe")}}
		if e = data.inventory(); e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(p.Destination, "unsafe.age")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		e = encodeArchive(f, data, key)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = decodeArchive(path, key); e == nil {
			t.Fatalf("accepted path %s", name)
		}
	}
	p.Destination = filepath.Join(root, "cache")
	if e = writeJSON(filepath.Join(root, "config", "backup.json"), p); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadPolicy(root); e == nil {
		t.Fatal("accepted recursive backup destination")
	}
}

func TestInternalBackupsAreAllowedExcludedAndPreserveHistoricalFiles(t *testing.T) {
	root, p := backupFixture(t)
	p.Destination = filepath.Join(root, "backups")
	historical := filepath.Join(p.Destination, "migration", "state.db")
	if e := os.MkdirAll(filepath.Dir(historical), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(historical, []byte("historical rollback data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(filepath.Join(root, "config", "backup.json"), p); e != nil {
		t.Fatal(e)
	}
	point, e := Create(context.Background(), root, false)
	if e != nil {
		t.Fatal(e)
	}
	manifest, e := Verify(context.Background(), filepath.Join(p.Destination, point.Archive), p.IdentityFile)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range manifest.Files {
		if strings.HasPrefix(f.Path, "workspace/backups/") {
			t.Fatal("recursively included backup storage")
		}
	}
	if _, e = Create(context.Background(), root, false); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(historical); e != nil || string(b) != "historical rollback data" {
		t.Fatal("modified historical backup", e)
	}
	p.IdentityFile = filepath.Join(p.Destination, "key")
	if e = writeJSON(filepath.Join(root, "config", "backup.json"), p); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadPolicy(root); e == nil {
		t.Fatal("allowed key beside archives")
	}
}

func TestSupervisorCatchUpAndCrossProcessExclusion(t *testing.T) {
	root, _ := backupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	failure := make(chan error, 1)
	go func() { defer close(done); Run(ctx, root, func(e error) { failure <- e }) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case e := <-failure:
			t.Fatal(e)
		default:
		}
		s, e := ReadStatus(root)
		if e != nil {
			t.Fatal(e)
		}
		if !s.LastSuccess.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup did not produce overdue backup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	lock, e := os.OpenFile(filepath.Join(root, "backup-state", "job.lock"), os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		t.Fatal(e)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if _, e = Create(context.Background(), root, false); e == nil {
		t.Fatal("overlapping manual/automatic backup was allowed")
	}
}
