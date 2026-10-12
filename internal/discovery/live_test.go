package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-host/internal/harness/claude"
	"agent-host/internal/state"
)

// This acceptance invokes real native Claude only on synthetic conversations.
// It never connects to DWS, reads an account, creates a task, or sends a reply.
func TestNativeDiscoveryLive(t *testing.T) {
	if os.Getenv("AGENT_HOST_LIVE_DISCOVERY") != "1" {
		t.Skip("opt-in native synthetic discovery acceptance")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixtureConfig(t)
	cfg.TimeoutSeconds = 180
	if model := os.Getenv("AGENT_HOST_LIVE_MODEL"); model != "" {
		cfg.Model = model
	} else {
		cfg.Model = "sonnet"
	}
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.RegisterProject(testContext, "project-a", "A", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	evidence := []state.DiscoveryEvidence{
		fixtureEvidence(cfg, now.Add(-3*time.Hour), "synthetic-message-1", "Owner: We need to decide whether deployment automation should automatically roll back when readiness checks fail. Please capture this as a project discussion topic; no deployment is authorized."),
		fixtureEvidence(cfg, now.Add(-2*time.Hour), "synthetic-message-2", "Peer: In the staging test I ran, the readiness probe failed after the update. I reported that a manual rollback restored staging. This is my report, not an independently observed production test."),
		fixtureEvidence(cfg, now.Add(-time.Hour), "synthetic-message-3", "Owner: The open decision is whether automatic rollback should be enabled. First compare the current readiness signal and rollback behavior; we have not approved automatic production changes."),
	}
	for _, index := range []int{0, 2} {
		evidence[index].AuthorID = "owner"
		evidence[index].Author = json.RawMessage(`"Owner"`)
		evidence[index] = state.PrepareDiscoveryEvidence(evidence[index])
	}
	provider := providerFunc(func(context.Context, Source, Range) (Collection, error) {
		return Collection{Coverage: state.DiscoveryCollection{Complete: true, Ledger: json.RawMessage(`{"synthetic_fixture":true,"complete":true}`)}, Evidence: evidence}, nil
	})
	c := NewCollector(store, cfg, provider, NewExtractor(claude.New(binary), cfg.Model))
	c.now = func() time.Time { return now }
	ctx, cancel := context.WithTimeout(testContext, 240*time.Second)
	defer cancel()
	report, err := c.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	topics, err := store.TopicProposals(ctx, "pending", 10)
	if err != nil || len(topics) == 0 {
		t.Fatalf("native extractor produced no actionable topics: report=%+v err=%v", report, err)
	}
	for _, p := range topics {
		review, err := store.TopicReview(ctx, p.ID)
		if err != nil || len(review.Evidence) == 0 {
			t.Fatalf("native topic lacks reviewable source %v", err)
		}
		for _, e := range review.Evidence {
			if e.Profile != cfg.Profile || e.ConversationID != cfg.Sources[0].ConversationID || e.ProjectID != cfg.Sources[0].ProjectID || e.AuthorID == "" {
				t.Fatal("native topic lost source attribution")
			}
		}
	}
	accepted, err := store.AcceptTopic(ctx, topics[0].ID, cfg.Sources[0].ProjectID)
	if err != nil || accepted.Proposal.Status != "accepted" || len(accepted.Evidence) == 0 {
		t.Fatalf("native topic acceptance failed: %v", err)
	}
	// Read inspection via Store APIs: none of the synthetic source material is
	// turned into a business run or learned memory.
	memories, err := store.Search(ctx, project.Scope, "deployment", 10)
	if err != nil {
		t.Fatalf("inspect learned memory after topic acceptance: %v", err)
	}
	if len(memories) != 0 {
		t.Fatal("topic acceptance published learned memory")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"sessions", "runs", "memories", "candidates", "channel_messages"} {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("inspect %s after topic acceptance: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("topic acceptance created %s records: %d", table, count)
		}
	}
	second, err := c.Collect(ctx)
	if err != nil || second.Sources[0].AddedEvidence != 0 || second.Sources[0].AddedTopics != 0 {
		t.Fatalf("restart-like overlap repeated topic: %+v %v", second, err)
	}
	if path := os.Getenv("AGENT_HOST_LIVE_REPORT"); path != "" {
		structural := map[string]any{"report_version": 1, "date": now.Format("2006-01-02"), "method": "real_native_claude_synthetic_source_acceptance", "passed": true, "source": "synthetic_selected_private_conversation", "topics": len(topics), "review_before_acceptance": true, "exact_source_references": true, "accepted_evidence_count": len(accepted.Evidence), "unchanged_overlap_new_topics": second.Sources[0].AddedTopics, "live_dws_reads": 0, "messages_sent": 0, "memory_published": false, "unverified": []string{"live_DWS_continuous_intake", "group_noise_quality", "complete_provider_coverage", "conversation_learning_review"}}
		b, _ := json.MarshalIndent(structural, "", "  ")
		if err = os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
