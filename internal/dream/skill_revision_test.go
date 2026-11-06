package dream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

func TestDraftSkillTargetRequiresCompletedActualEvidence(t *testing.T) {
	now := time.Now().UTC()
	p := state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	receipt := state.DreamReceipt{ID: "receipt", Partition: p, CreatedAt: now}
	draft := Draft{Kind: "procedure", Text: "Apply the correction.", ExpiresAt: now.Add(time.Hour), SourceRefs: []string{"artifact:result.json"}, TargetSkillID: "managed", TargetSkillRevision: 3, TargetSkillDigest: "digest"}
	allowed := map[string]bool{"artifact:result.json": true}
	offered := []state.DreamSkillEvidence{{Kind: "offered", SkillID: "managed", Revision: 3, Digest: "digest", Status: "completed"}}
	if _, err := validateDreamDrafts(receipt, []Draft{draft}, allowed, nil, offered, now); err == nil || !strings.Contains(err.Error(), "exact completed load") {
		t.Fatal("offered-only Skill became a revision target", err)
	}
	loaded := []state.DreamSkillEvidence{{Kind: "loaded", SkillID: "managed", Revision: 3, Digest: "digest", Status: "completed"}}
	candidates, err := validateDreamDrafts(receipt, []Draft{draft}, allowed, nil, loaded, now)
	if err != nil || len(candidates) != 1 || candidates[0].TargetSkillID != "managed" || candidates[0].TargetSkillRevision != 3 || candidates[0].TargetSkillDigest != "digest" {
		t.Fatalf("completed load target was not retained: %+v %v", candidates, err)
	}
	if err = validatePromotionTarget(SkillTarget{ID: "native", Revision: 1, Digest: "requested"}, []state.DreamSkillEvidence{{Kind: "loaded", SkillID: "native", Status: "completed"}}); err == nil || !strings.Contains(err.Error(), "lacks exact managed revision metadata") {
		t.Fatal("native Skill without revision metadata was not left pending with an explicit reason", err)
	}
}

func TestPromoteSkillAppendsExactManagedDraftRevision(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "local-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	root := filepath.Join(canonicalDreamTemp(t), "managed")
	base := dreamAuthoredSkill(t, "managed")
	catalog, err := managedskills.Update(root, "import", base.ID, "operator", "", "", 0, &base)
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.Records[0].Revisions[0].Entry
	for _, action := range []string{"review", "publish", "enable"} {
		catalog, err = managedskills.Update(root, action, first.ID, "operator", first.Digest, "reviewed base", first.Revision, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	target := SkillTarget{ID: first.ID, Revision: first.Revision, Digest: first.Digest}
	candidate := seedAutomaticSkillCandidate(t, store, p, "manual-revision", target)
	promoted, err := PromoteSkillWithTarget(ctx, partitioned, candidate.ID, root, "operator", target)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.ID != first.ID || promoted.Revision != 2 || promoted.Digest == first.Digest {
		t.Fatalf("unexpected promoted revision: %+v", promoted)
	}
	record := mustDreamSkillRecord(t, root, first.ID)
	if len(record.Revisions) != 2 || !record.Enabled || record.Selected != 1 || record.Revisions[1].Published || record.Revisions[1].Reviewer != "" {
		t.Fatalf("revision changed lifecycle state: %+v", record)
	}
	stored, err := partitioned.DreamCandidate(ctx, candidate.ID)
	if err != nil || stored.Status != "promoted" || stored.SkillRevision != promoted.Revision || stored.SkillDigest != promoted.Digest {
		t.Fatalf("candidate did not record exact revision: %+v %v", stored, err)
	}
	third, err := managedskills.AppendDraftRevision(root, first.ID, "operator", promoted.Revision, promoted.Digest, managedskills.DraftRevisionChange{Provenance: "manual:later", ReferencePath: "references/manual-later.md", ReferenceBody: "# Later draft\n", SkillAppendix: "## Later draft\n\nRead references/manual-later.md."})
	if err != nil || third.Revision != 3 {
		t.Fatalf("append later revision: %+v %v", third, err)
	}
	retry, err := PromoteSkill(ctx, partitioned, candidate.ID, root, "operator")
	if err != nil || retry.Revision != promoted.Revision || retry.Digest != promoted.Digest {
		t.Fatalf("retry returned latest instead of exact provenance revision: %+v %v", retry, err)
	}
}

func TestAutomaticSkillDraftTrustedEvidenceIdempotentAndSelectionPreserved(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "channel-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	root := filepath.Join(canonicalDreamTemp(t), "managed")
	base := dreamAuthoredSkill(t, "managed")
	catalog, err := managedskills.Update(root, "import", base.ID, "operator", "", "", 0, &base)
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.Records[0].Revisions[0].Entry
	for _, action := range []string{"review", "publish", "enable"} {
		catalog, err = managedskills.Update(root, action, first.ID, "operator", first.Digest, "reviewed base", first.Revision, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	candidate := seedAutomaticSkillCandidate(t, store, p, "automatic-revision", SkillTarget{ID: first.ID, Revision: first.Revision, Digest: first.Digest})
	result, err := DraftSkillAutomatically(ctx, store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().UTC())
	if err != nil || result.Status != "drafted" || result.Revision != 2 {
		t.Fatalf("automatic draft failed: %+v %v", result, err)
	}
	retry, err := DraftSkillAutomatically(ctx, store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().UTC())
	if err != nil || retry.Revision != result.Revision || retry.Digest != result.Digest {
		t.Fatalf("automatic retry duplicated or returned another revision: %+v %v", retry, err)
	}
	record := mustDreamSkillRecord(t, root, first.ID)
	if len(record.Revisions) != 2 || !record.Enabled || record.Selected != 1 || record.Revisions[1].Published || record.Revisions[1].Reviewer != "" {
		t.Fatalf("automatic draft changed selected/enabled state: %+v", record)
	}
}

func TestAutomaticSkillDraftRejectsUntrustedAndChangedSource(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "channel-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	root := filepath.Join(canonicalDreamTemp(t), "managed")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	candidate := seedAutomaticSkillCandidate(t, store, p, "changed-source", SkillTarget{})
	group := p
	group.AudienceID = "group"
	if _, err = DraftSkillAutomatically(ctx, store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, group, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "trusted owner partition") {
		t.Fatal("group partition received automatic Skill authority", err)
	}
	e := event.Event{RunID: "run-changed-source", Type: "agent.result", Phase: "execute", Status: "completed", Data: map[string]any{"changed": true}}
	if err = store.WithPartition(p.AgentID, p.AudienceID).AppendEvent(ctx, &e); err != nil {
		t.Fatal(err)
	}
	result, err := DraftSkillAutomatically(ctx, store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().UTC())
	if err != nil || result.Status != "blocked" || !strings.Contains(result.Reason, "source changed") {
		t.Fatalf("changed canonical source was not durably rejected: %+v %v", result, err)
	}
	stored, err := store.WithPartition(p.AgentID, p.AudienceID).DreamCandidate(ctx, candidate.ID)
	if err != nil || stored.Status != "pending" || stored.SkillDraftStatus != "blocked" || stored.SkillDraftReason == "" {
		t.Fatalf("changed source did not remain pending and inspectable: %+v %v", stored, err)
	}
	if _, err = DraftSkillAutomatically(ctx, store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, err = store.WithPartition(p.AgentID, p.AudienceID).DreamCandidate(ctx, candidate.ID)
	if err != nil || stored.SkillDraftAttempts != 1 {
		t.Fatalf("unchanged blocked evidence retried: %+v %v", stored, err)
	}
}

func TestAutomaticNewSkillDraftRemainsDisabled(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "channel-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	root := filepath.Join(canonicalDreamTemp(t), "managed")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	candidate := seedAutomaticSkillCandidate(t, store, p, "new-skill", SkillTarget{})
	result, err := DraftSkillAutomatically(context.Background(), store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().UTC())
	if err != nil || result.Status != "drafted" || result.Revision != 1 {
		t.Fatalf("automatic new Skill draft failed: %+v %v", result, err)
	}
	record := mustDreamSkillRecord(t, root, result.SkillID)
	if record.Enabled || record.Selected != 0 || record.Revisions[0].Published || record.Revisions[0].Reviewer != "" {
		t.Fatalf("automatic new Skill was activated or reviewed: %+v", record)
	}
}

func TestAutomaticSkillDraftStaleTargetRemainsPending(t *testing.T) {
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := state.DreamPartition{PersonID: "channel-owner", AgentID: "agent", AudienceID: state.OwnerAudienceID, Scope: "scope"}
	root := filepath.Join(canonicalDreamTemp(t), "managed")
	base := dreamAuthoredSkill(t, "managed")
	catalog, err := managedskills.Update(root, "import", base.ID, "operator", "", "", 0, &base)
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.Records[0].Revisions[0].Entry
	if _, err = managedskills.AppendDraftRevision(root, first.ID, "operator", first.Revision, first.Digest, managedskills.DraftRevisionChange{Provenance: "manual:newer", ReferencePath: "references/newer.md", ReferenceBody: "# Newer\n", SkillAppendix: "## Newer draft\n\nRead references/newer.md."}); err != nil {
		t.Fatal(err)
	}
	candidate := seedAutomaticSkillCandidate(t, store, p, "stale-target", SkillTarget{ID: first.ID, Revision: first.Revision, Digest: first.Digest})
	result, err := DraftSkillAutomatically(context.Background(), store.WithPartition(p.AgentID, p.AudienceID), candidate.ID, root, p, time.Now().UTC())
	if err != nil || result.Status != "blocked" || !strings.Contains(result.Reason, "revision changed") {
		t.Fatalf("stale target was not blocked: %+v %v", result, err)
	}
	stored, err := store.WithPartition(p.AgentID, p.AudienceID).DreamCandidate(context.Background(), candidate.ID)
	if err != nil || stored.Status != "pending" || stored.SkillDraftStatus != "blocked" {
		t.Fatalf("stale target did not remain pending: %+v %v", stored, err)
	}
}

func seedAutomaticSkillCandidate(t *testing.T, store *state.Store, p state.DreamPartition, suffix string, target SkillTarget) state.DreamCandidate {
	t.Helper()
	ctx := context.Background()
	partitioned := store.WithPartition(p.AgentID, p.AudienceID)
	message := state.ChannelMessage{ID: "message-" + suffix, Channel: "fixture", AccountID: "app", TenantID: "tenant", MessageID: "native-message-" + suffix, ConversationID: "owner-dm", ConversationType: "1", SenderID: p.PersonID, AgentID: p.AgentID, AudienceID: p.AudienceID, Profile: "profile-" + suffix, Binding: "binding-" + suffix, Scope: p.Scope, MessageType: "text", Text: "procedure", ReceivedAt: time.Now().UTC(), ReplyExpiresAt: time.Now().Add(time.Hour).UTC()}
	if inserted, err := partitioned.EnqueueChannelMessage(ctx, message, "https://example.invalid/reply"); err != nil || !inserted {
		t.Fatalf("enqueue channel evidence: inserted=%v err=%v", inserted, err)
	}
	message, err := partitioned.ClaimNextChannelMessage(ctx, message.Profile)
	if err != nil {
		t.Fatal(err)
	}
	session := state.Session{ID: "session-" + suffix, AgentID: p.AgentID, AudienceID: p.AudienceID, Scope: p.Scope, NativeSessionID: "native-" + suffix}
	if err := partitioned.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := partitioned.BindChannelSession(ctx, message.Binding, session.ID); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "run-" + suffix, SessionID: session.ID, AgentID: p.AgentID, AudienceID: p.AudienceID, Scope: p.Scope, Input: message.Text, NativeSessionID: session.NativeSessionID}
	if err := partitioned.CreateChannelRun(ctx, run, message.ID); err != nil {
		t.Fatal(err)
	}
	if !target.empty() {
		source := harness.SkillSource{Name: "managed", SkillID: target.ID, Revision: target.Revision, ContentDigest: target.Digest}
		e := event.Event{RunID: run.ID, Type: "skills.load.observed", Phase: "execute", Status: "completed", Data: map[string]any{"source": source}}
		if err := partitioned.AppendEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	if err := partitioned.CompleteRun(ctx, run.ID, "completed", "completed", "observed", "", state.RunJudgment{Outcome: "accepted", Reason: "fixture", Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	record, err := partitioned.DreamCanonicalRunByID(ctx, p, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := canonicalRunSource(record, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	receipt := state.DreamReceipt{ID: "receipt-" + suffix, IdempotencyKey: "key-" + suffix, Source: "host-run:" + run.ID, SourceDigest: snapshot.Work.EvidenceDigest, Partition: p, Status: "extracted", CanonicalRunID: run.ID, CanonicalSessionID: session.ID, Attribution: "host_observed", CreatedAt: now}
	candidate := state.DreamCandidate{ID: "dream-candidate-" + suffix, ReceiptID: receipt.ID, Partition: p, Kind: "procedure", Text: "Use the corrected procedure.", SourceRefs: []string{"artifact:canonical-outcome.json"}, ExpiresAt: now.Add(time.Hour), TargetSkillID: target.ID, TargetSkillRevision: target.Revision, TargetSkillDigest: target.Digest}
	if _, err = partitioned.SaveDreamExtraction(ctx, receipt, []state.DreamCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func dreamAuthoredSkill(t *testing.T, id string) managedskills.Entry {
	t.Helper()
	root := canonicalDreamTemp(t)
	if err := os.Mkdir(filepath.Join(root, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: managed\ndescription: Managed fixture Skill\n---\n\nRead references/base.md.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "references/base.md"), []byte("# Base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := managedskills.Import(root, managedskills.Entry{ID: id, Owner: "operator", Applicability: "fixture", Agents: []string{"agent"}, Audiences: []string{state.OwnerAudienceID}, Scopes: []string{"scope"}, Provenance: []string{"fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func canonicalDreamTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustDreamSkillRecord(t *testing.T, root, id string) managedskills.Record {
	t.Helper()
	catalog, err := managedskills.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range catalog.Records {
		if record.ID == id {
			return record
		}
	}
	t.Fatalf("Skill record %s not found", id)
	return managedskills.Record{}
}
