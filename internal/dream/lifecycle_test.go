package dream

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

type fakeExtractor struct {
	calls int
	fail  bool
	now   time.Time
}

func TestPublishHOTRejectsTamperedSnapshotAndSymlinkAncestor(t *testing.T) {
	bundle := HOTBundle{Version: "dream-hot-v1", Partition: state.DreamPartition{PersonID: "p", AgentID: "a", AudienceID: "u", Scope: "s"}, Documents: map[string]string{"USER.md": "user", "MEMORY.md": "memory", "TASKS.md": "tasks"}}
	bundle.Digest = hotBundleDigest(bundle)
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(cache, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := PublishHOT(cache, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "USER.md"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishHOT(cache, bundle); err == nil {
		t.Fatal("tampered immutable HOT snapshot was reused")
	}
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(other, 0700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(other, "dream-hot")
	if err = os.Symlink(t.TempDir(), linked); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishHOT(other, bundle); err == nil {
		t.Fatal("symlinked HOT publication ancestor was accepted")
	}
}

func (f *fakeExtractor) Extract(context.Context, ExtractionInput) ([]Draft, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("fixture extraction failed")
	}
	refs := []string{"artifact:request.txt"}
	return []Draft{{Kind: "working_preference", Text: "Prefers concise status summaries for this project.", Inferred: true, ExpiresAt: f.now.Add(time.Hour), SourceRefs: refs}, {Kind: "knowledge", Text: "The project distributes source only.", ExpiresAt: f.now.Add(time.Hour), SourceRefs: refs}, {Kind: "procedure", Text: "Review the source diff before publishing.", ExpiresAt: f.now.Add(time.Hour), SourceRefs: refs}}, nil
}

func TestLifecycleFakeExtractionReviewHOTArchiveAndSkillDraft(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	if _, err = store.MigrateFileMemory(context.Background(), filepath.Join(root, "memory"), filepath.Join(root, "cache"), "dream-test"); err != nil {
		t.Fatal(err)
	}
	source := privateHandoff(t, now)
	dest := Destination{PersonID: "local-owner", Agent: "agent-1", Audience: state.OwnerAudienceID, MemoryScope: "project-1"}
	partitioned := store.WithPartition(dest.Agent, dest.Audience)
	fake := &fakeExtractor{now: now}
	l := Lifecycle{Store: partitioned, Extractor: fake, Now: func() time.Time { return now }}
	if _, err = l.Process(context.Background(), source, dest); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Process(context.Background(), source, dest); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatalf("idempotent retry dispatched extraction %d times", fake.calls)
	}
	p := state.DreamPartition{PersonID: dest.PersonID, AgentID: dest.Agent, AudienceID: dest.Audience, Scope: dest.MemoryScope}
	items, err := partitioned.DreamCandidates(context.Background(), p, "pending", 10)
	if err != nil || len(items) != 3 {
		t.Fatalf("candidates=%+v err=%v", items, err)
	}
	for _, item := range items {
		if item.Kind == "procedure" {
			continue
		}
		if _, err = ReviewCandidate(context.Background(), partitioned, item.ID, "operator", "reviewed evidence", "accept"); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := ExportHOT(context.Background(), partitioned, p, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundle.Documents["USER.md"], "concise") || !strings.Contains(bundle.Documents["MEMORY.md"], "source only") || bundle.Documents["TASKS.md"] != "" {
		t.Fatalf("unexpected HOT bundle: %+v", bundle)
	}
	if generic, e := partitioned.Search(context.Background(), p.Scope, "source only", 10); e != nil || len(generic) != 0 {
		t.Fatalf("generic recall leaked dream memory: %+v %v", generic, e)
	}
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(cache, 0700); err != nil {
		t.Fatal(err)
	}
	published, err := PublishHOT(cache, bundle)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(published, "manifest.json"))
	if err != nil || !strings.Contains(string(raw), bundle.Digest) {
		t.Fatalf("published manifest mismatch: %s %v", raw, err)
	}
	userBody, _ := os.ReadFile(filepath.Join(published, "USER.md"))
	if string(userBody) != bundle.Documents["USER.md"] {
		t.Fatal("published USER.md differs from manifest bundle")
	}
	first := bundle.Memories[0]
	if err = partitioned.RetireDreamMemory(context.Background(), p, first.ID, "operator", "temporary retirement", first.Revision); err != nil {
		t.Fatal(err)
	}
	if err = partitioned.RestoreDreamMemory(context.Background(), p, first.ID, "operator", "restore reviewed revision", first.Revision+1); err != nil {
		t.Fatal(err)
	}
	wrong := p
	wrong.PersonID = "person-2"
	if other, e := ExportHOT(context.Background(), partitioned, wrong, now, 10); e != nil || len(other.Memories) != 0 {
		t.Fatalf("person isolation failed: %+v %v", other, e)
	}
	var procedure state.DreamCandidate
	for _, item := range items {
		if item.Kind == "procedure" {
			procedure = item
		}
	}
	skillsParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(skillsParent, "skills")
	entry, err := PromoteSkill(context.Background(), partitioned, procedure.ID, skills, "operator")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := managedskills.Load(skills)
	if err != nil || len(catalog.Records) != 1 || catalog.Records[0].Enabled || catalog.Records[0].Revisions[0].Published || entry.Revision != 1 {
		t.Fatalf("Skill auto-enabled or published: %+v %v", catalog, err)
	}
	retry, err := PromoteSkill(context.Background(), partitioned, procedure.ID, skills, "operator")
	if err != nil || retry.Digest != entry.Digest || retry.Revision != entry.Revision {
		t.Fatalf("Skill retry changed revision: %+v %v", retry, err)
	}
	count, err := partitioned.ArchiveExpiredDream(context.Background(), p, now.Add(2*time.Hour), 10)
	if err != nil || count != 2 {
		t.Fatalf("archive count=%d err=%v", count, err)
	}
	after, err := ExportHOT(context.Background(), partitioned, p, now.Add(2*time.Hour), 10)
	if err != nil || len(after.Memories) != 0 {
		t.Fatalf("expired memory remained HOT: %+v %v", after, err)
	}
}

func TestLifecycleBatchCountsOnlyNewExtractionAttempts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := privateRoot(t)
	for _, name := range []string{"handoff-one", "handoff-two", "handoff-three"} {
		handoff(t, source, name, 0)
	}
	destination := Destination{PersonID: "person", Agent: "agent", Audience: "private", MemoryScope: "scope"}
	fake := &fakeExtractor{now: now}
	lifecycle := Lifecycle{Store: store.WithPartition("agent", "private"), Extractor: fake, Now: func() time.Time { return now }, Batch: 1}

	for cycle := 1; cycle <= 3; cycle++ {
		receipts, processErr := lifecycle.Process(context.Background(), source, destination)
		if processErr != nil {
			t.Fatal(processErr)
		}
		if len(receipts) != 3 {
			t.Fatalf("cycle %d omitted bounded-scan receipts: %+v", cycle, receipts)
		}
		if fake.calls != cycle {
			t.Fatalf("cycle %d extraction calls=%d", cycle, fake.calls)
		}
	}
	if receipts, processErr := lifecycle.Process(context.Background(), source, destination); processErr != nil || len(receipts) != 3 || fake.calls != 3 {
		t.Fatalf("completed retry repeated extraction: receipts=%+v calls=%d err=%v", receipts, fake.calls, processErr)
	}
}

func TestLifecycleExtractionFailureLeavesRetryableReceipt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dest := Destination{PersonID: "p", Agent: "a", Audience: "private", MemoryScope: "s"}
	fake := &fakeExtractor{fail: true, now: now}
	l := Lifecycle{Store: store.WithPartition("a", "private"), Extractor: fake, Now: func() time.Time { return now }}
	source := privateHandoff(t, now)
	if _, err = l.Process(context.Background(), source, dest); err == nil {
		t.Fatal("extraction failure hidden")
	}
	p := state.DreamPartition{PersonID: "p", AgentID: "a", AudienceID: "private", Scope: "s"}
	if items, e := store.WithPartition("a", "private").DreamCandidates(context.Background(), p, "all", 10); e != nil || len(items) != 0 {
		t.Fatalf("failure invented candidates: %+v %v", items, e)
	}
	fake.fail = false
	receipts, err := l.Process(context.Background(), source, dest)
	if err != nil || len(receipts) != 1 || receipts[0].Status != "extracted" || receipts[0].Error != "" {
		t.Fatalf("successful retry retained failure: %+v %v", receipts, err)
	}
	receipts, err = l.Process(context.Background(), source, dest)
	if err != nil || len(receipts) != 1 || receipts[0].Error != "" || fake.calls != 2 {
		t.Fatalf("retry was not persisted idempotently: %+v %v calls=%d", receipts, err, fake.calls)
	}
}

func TestReviewRejectsChangedSource(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	if _, err = store.MigrateFileMemory(context.Background(), filepath.Join(root, "memory"), filepath.Join(root, "cache"), "dream-review"); err != nil {
		t.Fatal(err)
	}
	source := privateHandoff(t, now)
	destination := Destination{PersonID: "local-owner", Agent: "a", Audience: state.OwnerAudienceID, MemoryScope: "s"}
	partitioned := store.WithPartition("a", state.OwnerAudienceID)
	if _, err = (Lifecycle{Store: partitioned, Extractor: &fakeExtractor{now: now}, Now: func() time.Time { return now }}).Process(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	items, err := partitioned.DreamCandidates(context.Background(), state.DreamPartition{PersonID: "local-owner", AgentID: "a", AudienceID: state.OwnerAudienceID, Scope: "s"}, "pending", 10)
	if err != nil || len(items) == 0 {
		t.Fatal(err)
	}
	path := filepath.Join(source, "handoff-one", "request.txt")
	if err = os.WriteFile(path, []byte("changed historical affair"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReviewCandidate(context.Background(), partitioned, items[0].ID, "operator", "review", "accept"); err == nil {
		t.Fatal("changed source was accepted")
	}
}

func privateHandoff(t *testing.T, now time.Time) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonical
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "handoff-one")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"launch.json": `{"exit_code":0,"finalized_at_utc":"` + now.Format(time.RFC3339) + `","stream_format":"text"}`, "request.txt": "Summarize historical work.", "stdout.txt": "Work completed."}
	for name, body := range files {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
