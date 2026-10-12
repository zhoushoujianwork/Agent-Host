package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func discoveryFixture(t *testing.T, s *Store) (DiscoverySource, DiscoveryCollection, DiscoveryEvidence) {
	t.Helper()
	if _, err := s.RegisterProject(testContext, "discovery-project", "Discovery", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	source := DiscoverySource{Profile: "corp:owner", TenantID: "corp", PrincipalID: "owner", ConversationID: "c1", Type: "dm", PeerOpenDingTalkID: "peer", ProjectID: "discovery-project"}
	source.ID = DiscoverySourceID(source.Profile, source.ConversationID, source.ProjectID, source.Type, source.PeerOpenDingTalkID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	collection := DiscoveryCollection{From: now.Add(-time.Hour), To: now.Add(time.Hour), At: now, Complete: true, Count: 1, Ledger: json.RawMessage(`{"complete":true}`)}
	e := PrepareDiscoveryEvidence(DiscoveryEvidence{SourceID: source.ID, Profile: source.Profile, TenantID: source.TenantID, PrincipalID: source.PrincipalID, ConversationID: source.ConversationID, ProjectID: source.ProjectID, MessageID: "m1", AuthorID: "peer", Text: "A deployment decision is needed", CreatedAt: now, Availability: "available"})
	return source, collection, e
}

func TestDiscoveryCollectionAtomicScopeAndCheckpoint(t *testing.T) {
	s, _ := testStore(t)
	source, collection, e := discoveryFixture(t, s)
	bad := e
	bad.ConversationID = "another"
	bad = PrepareDiscoveryEvidence(bad)
	if _, err := s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{bad}); err == nil {
		t.Fatal("foreign evidence accepted")
	}
	if _, err := s.DiscoverySource(testContext, source.ID); err == nil {
		t.Fatal("invalid collection advanced source state")
	}
	added, err := s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{e})
	if err != nil || added != 1 {
		t.Fatalf("collection %+v %v", added, err)
	}
	added, err = s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{e})
	if err != nil || added != 0 {
		t.Fatal("overlapping collection duplicated evidence")
	}
	gap := collection
	gap.From = collection.To.Add(time.Minute)
	gap.To = collection.To.Add(time.Hour)
	gap.Count = 0
	if _, err = s.SaveDiscoveryCollection(testContext, source, gap, nil); err == nil {
		t.Fatal("collection skipped an uncovered range")
	}
	partial := collection
	partial.Complete, partial.Partial, partial.HasMore = false, true, true
	partial.To = collection.To.Add(time.Hour)
	if _, err = s.SaveDiscoveryCollection(testContext, source, partial, []DiscoveryEvidence{e}); err != nil {
		t.Fatal(err)
	}
	got, err := s.DiscoverySource(testContext, source.ID)
	if err != nil || !got.Checkpoint.CompleteThrough.Equal(collection.To) || !got.Checkpoint.PendingTo.Equal(partial.To) {
		t.Fatalf("lost checkpoint %+v %v", got, err)
	}
	changedPending := partial
	changedPending.To = partial.To.Add(time.Hour)
	if _, err = s.SaveDiscoveryCollection(testContext, source, changedPending, []DiscoveryEvidence{e}); err == nil {
		t.Fatal("pending range silently changed during retry")
	}
}

func TestTopicReviewAcceptanceDedupAndRetention(t *testing.T) {
	s, _ := testStore(t)
	source, collection, e := discoveryFixture(t, s)
	if _, err := s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{e}); err != nil {
		t.Fatal(err)
	}
	drafts := []TopicDraft{{Title: "Deployment decision", Summary: "A participant requested a deployment decision.", EvidenceIDs: []string{e.ID}}}
	added, err := s.SaveTopicExtraction(testContext, source.ID, "digest", "model", drafts, json.RawMessage(`{"complete":true}`), "")
	if err != nil || added != 1 {
		t.Fatal(err)
	}
	// Rephrasing the same source-backed proposal cannot notify twice.
	drafts[0].Title = "Same deployment decision"
	added, err = s.SaveTopicExtraction(testContext, source.ID, "digest", "model", drafts, json.RawMessage(`{"complete":true}`), "")
	if err != nil || added != 0 {
		t.Fatal("unchanged evidence created another topic")
	}
	proposals, err := s.TopicProposals(testContext, "pending", 10)
	if err != nil || len(proposals) != 1 {
		t.Fatal(err)
	}
	review, err := s.TopicReview(testContext, proposals[0].ID)
	if err != nil || review.Disposition != "review_only" || len(review.Evidence) != 1 || review.Evidence[0].Text != e.Text || review.Proposal.Status != "pending" {
		t.Fatalf("review requires premature acceptance: %+v %v", review, err)
	}
	if _, err = s.AcceptTopic(testContext, proposals[0].ID, "other-project"); err == nil {
		t.Fatal("cross-project topic export accepted")
	}
	accepted, err := s.AcceptTopic(testContext, proposals[0].ID, source.ProjectID)
	if err != nil || accepted.Proposal.Status != "accepted" || len(accepted.Evidence) != 1 || accepted.Evidence[0].AuthorID != e.AuthorID {
		t.Fatalf("topic acceptance %+v %v", accepted, err)
	}
	if err = s.PruneDiscoveryCache(testContext, collection.At.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	retained, err := s.TopicReview(testContext, proposals[0].ID)
	if err != nil || len(retained.Evidence) != 1 || retained.Proposal.Status != "accepted" {
		t.Fatal("retention lost topic-cited evidence")
	}
	active, err := s.DiscoveryEvidence(testContext, source.ID, 10)
	if err != nil || len(active) != 0 {
		t.Fatal("expired topic citation stayed active for new discovery extraction")
	}
	for _, table := range []string{"runs", "memories", "candidates", "channel_messages"} {
		var count int
		if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("topic acceptance wrote %s: count=%d err=%v", table, count, err)
		}
	}
}

func TestTopicUnknownReferencesAndFailureRetention(t *testing.T) {
	s, _ := testStore(t)
	source, collection, e := discoveryFixture(t, s)
	s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{e})
	bad := []TopicDraft{{Title: "Invented", Summary: "Unsupported", EvidenceIDs: []string{"guessed-evidence"}}}
	if _, err := s.SaveTopicExtraction(testContext, source.ID, "digest", "model", bad, json.RawMessage(`{}`), ""); err == nil {
		t.Fatal("unknown source reference accepted")
	}
	if _, err := s.SaveTopicExtraction(testContext, source.ID, "digest", "model", nil, json.RawMessage(`{"status":"failed"}`), "native failed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.DiscoverySource(testContext, source.ID)
	if err != nil || got.ExtractionStatus != "failed" || !strings.Contains(got.ExtractionError, "failed") {
		t.Fatal("extraction failure not retained")
	}
	retained, err := s.DiscoveryEvidence(testContext, source.ID, 10)
	if err != nil || len(retained) != 1 {
		t.Fatal("extraction failure lost observations")
	}
}

func TestDiscoveryImmutableRevisionsKeepNewestAndTopicSnapshot(t *testing.T) {
	s, _ := testStore(t)
	source, collection, e := discoveryFixture(t, s)
	e.UpdatedAt = e.CreatedAt
	e = PrepareDiscoveryEvidence(e)
	s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{e})
	drafts := []TopicDraft{{Title: "Decision", Summary: "Original request", EvidenceIDs: []string{e.ID}}}
	s.SaveTopicExtraction(testContext, source.ID, "digest", "model", drafts, json.RawMessage(`{}`), "")
	changed := e
	changed.Text = "The request was corrected"
	changed.UpdatedAt = e.UpdatedAt.Add(time.Minute)
	changed = PrepareDiscoveryEvidence(changed)
	if added, err := s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{changed}); err != nil || added != 1 {
		t.Fatal(err)
	}
	if _, err := s.SaveDiscoveryCollection(testContext, source, collection, []DiscoveryEvidence{e}); err != nil {
		t.Fatal(err)
	}
	current, err := s.DiscoveryEvidence(testContext, source.ID, 10)
	if err != nil || len(current) != 1 || current[0].ID != changed.ID {
		t.Fatal("stale overlap replaced newer revision")
	}
	topics, _ := s.TopicProposals(testContext, "pending", 10)
	original, err := s.TopicReview(testContext, topics[0].ID)
	if err != nil || original.Evidence[0].ID != e.ID || original.Evidence[0].Text != e.Text {
		t.Fatal("topic lost cited immutable revision")
	}
}
