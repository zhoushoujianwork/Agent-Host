package state

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCompactRecoveryPreservesCurrentStatusEvidenceAndScope(t *testing.T) {
	s, home, _, _ := fileStore(t)
	defer s.Close()
	r := addRun(t, s, "remembered", "scope", "Keep replies concise.", true)
	if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "m")}, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateMemory(testContext, "m", "Do not learn this again"); err != nil {
		t.Fatal(err)
	}
	addRun(t, s, "unrelated", "scope", "An unrelated pending task", false)
	captured, err := CaptureMemoryRecovery(testContext, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Memories) != 1 || len(captured.Runs) != 1 || captured.Memories[0].Revision != 2 {
		t.Fatalf("not compact: %+v", captured)
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "state")
	if err = RestoreMemoryRecovery(testContext, captured, restored, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(restored, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	m, err := ro.Memory(testContext, "m")
	if err != nil || m.Status != "invalidated" || m.Revision != 2 || m.Scope != "scope" {
		t.Fatalf("lost disabled memory: %+v %v", m, err)
	}
	if hits, e := ro.Search(testContext, "scope", "concise", 10); e != nil || len(hits) != 0 {
		t.Fatalf("disabled memory recalled %+v %v", hits, e)
	}
	report, err := ro.VerifyFileMemory(testContext)
	if err != nil || report.Revisions != 1 {
		t.Fatalf("retained old revision: %+v %v", report, err)
	}
	if _, err = ro.Run(testContext, "remembered"); err != nil {
		t.Fatal(err)
	}
	if _, err = ro.Run(testContext, "unrelated"); err == nil {
		t.Fatal("restored unrelated work")
	}
	if got, err := ro.PendingMemory(testContext); err != nil || len(got) != 0 {
		t.Fatalf("pending work revived: %+v %v", got, err)
	}
	if _, err = ro.WithPartition("other", "owner").Memory(testContext, "m"); err == nil {
		t.Fatal("scope isolation lost")
	}
	if err = RestoreMemoryRecovery(testContext, captured, restored, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")); err == nil {
		t.Fatal("overwrote existing restore")
	}
	if _, err = os.Stat(filepath.Join(restored, "recovery-original.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCapturesSchema14WithoutMigration(t *testing.T) {
	s, home, _, _ := fileStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("DROP TABLE external_memory_receipts; DROP TABLE external_memory_connections; PRAGMA user_version=14"); err != nil {
		t.Fatal(err)
	}
	if _, err = CaptureMemoryRecovery(testContext, home); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 14 {
		t.Fatalf("backup migrated original schema %d %v", version, err)
	}
}

func TestRecoveryRetainsChannelInputFileWithoutRevivingDelivery(t *testing.T) {
	s, home, _, _ := fileStore(t)
	m := attachmentInput("picture")
	if _, err := s.EnqueueChannelMessage(testContext, m, testReplyURL, []string{"private-download-capability"}); err != nil {
		t.Fatal(err)
	}
	m = claimChannel(t, s, m.Profile)
	file := filepath.Join(t.TempDir(), "diagram.png")
	data := []byte("preserved image input")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	attachments := downloadedAttachments(m)
	attachments[0].Path = file
	attachments[0].Bytes = int64(len(data))
	attachments[0].SHA256 = fileHash(data)
	if err = s.SaveChannelAttachments(testContext, m.ID, attachments); err != nil {
		t.Fatal(err)
	}
	run := channelRun(t, s, m)
	if err = s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRun(testContext, run.ID, "completed", "Observed diagram", "test", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeMemory(testContext, run.ID, []MemoryUpdate{updateFor(run, "picture-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	captured, err := CaptureMemoryRecovery(testContext, home)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "state")
	if err = RestoreMemoryRecovery(testContext, captured, restored, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(restored, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.Run(testContext, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Source.Input.Attachments[0].Path
	if path == file {
		t.Fatal("input still depends on original file")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(data) {
		t.Fatalf("lost input attachment %v", err)
	}
	message, err := ro.ChannelMessage(testContext, m.ID)
	if err != nil || message.Status != "completed" || message.ReplyStatus != "skipped" {
		t.Fatalf("revived delivery %+v %v", message, err)
	}
	if replyURL, e := ro.ChannelReplyURL(testContext, m.ID); e != nil || replyURL != "" {
		t.Fatal("restored a private reply capability")
	}
}

func TestRecoveryRestoresSkillToolEvidenceAndExternalReceipt(t *testing.T) {
	s, home, _, _ := fileStore(t)
	c := experienceCandidate(t, s, "procedure", "scope")
	saveCandidate(t, s, c)
	fingerprint, err := s.MemoryFingerprint(testContext, []string{c.Scope})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.AcceptCandidate(testContext, c.ID, CandidateAcceptance{MemoryID: "learned-skill", ExpectedRevisions: map[string]int{}, ExpectedGeneration: fingerprint, Decision: decision})
	if err != nil {
		t.Fatal(err)
	}
	connection := admitExternal(t, s, "codex")
	receipt, err := s.ReceiveExternal(testContext, connection.ID, "external", externalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishExternalItem(testContext, receipt.ID, 0, externalPublication(t, s, receipt, "external-memory")); err != nil {
		t.Fatal(err)
	}
	captured, err := CaptureMemoryRecovery(testContext, home)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "state")
	if err = RestoreMemoryRecovery(testContext, captured, restored, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory")); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(restored, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	report, err := ro.VerifyFileMemory(testContext)
	if err != nil || report.Skills != 1 {
		t.Fatalf("lost skill %+v %v", report, err)
	}
	got, err := ro.Memory(testContext, accepted.ID)
	if err != nil || got.Text != accepted.Text {
		t.Fatalf("skill changed %+v %v", got, err)
	}
	evidence, err := ro.CandidateEvidence(testContext, c.ID)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("tool evidence missing %+v %v", evidence, err)
	}
	external, err := ro.ExternalReceipt(testContext, connection.ID, receipt.ID, "")
	if err != nil || len(external.Submission.Evidence) != 1 {
		t.Fatalf("external source lost %+v %v", external, err)
	}
	if pending, err := ro.PendingExternal(testContext); err != nil || len(pending) != 0 {
		t.Fatalf("external intake resumed %+v %v", pending, err)
	}
}
