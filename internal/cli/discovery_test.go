package cli

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/discovery"
	"agent-host/internal/state"
)

func discoveryCLIConfig(t *testing.T) (path string, config discovery.Config, called string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	called = filepath.Join(root, "remote-called")
	t.Setenv("AGENT_HOST_DISCOVERY_TEST_CALLED", called)
	executable := filepath.Join(root, "fixture-dws")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'called\\n' > \"$AGENT_HOST_DISCOVERY_TEST_CALLED\"\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config = discovery.Config{DWSExecutable: executable, Profile: "fixture-corp:fixture-owner", IntervalSeconds: 300,
		InitialWindowHours: 24, ReconciliationWindowHours: 6, RetentionDays: 30, PageLimit: 2, MaxMessages: 200, TimeoutSeconds: 10,
		Model: "fixture-model", Sources: []discovery.Source{{ConversationID: "selected-daily-conversation", Type: "dm", PeerOpenDingTalkID: "selected-peer", ProjectID: "registered-app"}}}
	path = filepath.Join(root, "discovery.json")
	writeBotJSON(t, path, config)
	return path, config, called
}

func assertDiscoveryNotExecuted(t *testing.T, called string) {
	t.Helper()
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("local discovery inspection invoked the remote executable", err)
	}
}

func discoveryDatabaseDigest(t *testing.T, home string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestDiscoveryCheckUsesPrivateLocalConfigurationWithoutRemoteOrStateCreation(t *testing.T) {
	configPath, cfg, called := discoveryCLIConfig(t)
	home := filepath.Join(t.TempDir(), "not-created")
	var out bytes.Buffer
	if err := Run(t.Context(), []string{"discovery", "check", "--config", configPath, "--home", home}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report["status"] != "local_configuration_valid" || report["remote_verified"] != false || report["native_auth_verified"] != false || report["sources"] != float64(1) {
		t.Fatal("local discovery check claimed remote acceptance", report, err)
	}
	for _, private := range []string{cfg.Profile, cfg.Sources[0].ConversationID, cfg.Sources[0].PeerOpenDingTalkID, cfg.DWSExecutable} {
		if strings.Contains(out.String(), private) {
			t.Fatal("local check exposed private source configuration")
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("local discovery check created Host state", err)
	}
	if err := os.Chmod(configPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), []string{"discovery", "check", "--config", configPath, "--home", home}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("public discovery configuration was accepted by the CLI")
	}
	assertDiscoveryNotExecuted(t, called)
}

func TestDiscoveryCheckRequiresRegisteredProjectWithoutMutatingDatabase(t *testing.T) {
	path, config, called := discoveryCLIConfig(t)
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject(t.Context(), config.Sources[0].ProjectID, "Application", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := discoveryDatabaseDigest(t, home)
	if err := Run(t.Context(), []string{"discovery", "check", "--config", path, "--home", home}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	config.Sources[0].ProjectID = "unregistered-app"
	writeBotJSON(t, path, config)
	if err := Run(t.Context(), []string{"discovery", "check", "--config", path, "--home", home}, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unregistered-app") {
		t.Fatal("discovery check accepted an unregistered project", err)
	}
	if after := discoveryDatabaseDigest(t, home); after != before {
		t.Fatal("read-only discovery check changed the database")
	}
	assertDiscoveryNotExecuted(t, called)
}

func discoveryTopicCLIFixture(t *testing.T) (home, topicID, evidenceID, called string) {
	t.Helper()
	_, config, called := discoveryCLIConfig(t)
	home = t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RegisterProject(t.Context(), "registered-app", "Application", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject(t.Context(), "other-app", "Other application", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	source := config.Sources[0].Attribution(config)
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	evidence := state.PrepareDiscoveryEvidence(state.DiscoveryEvidence{
		SourceID: source.ID, Profile: source.Profile, TenantID: source.TenantID, PrincipalID: source.PrincipalID,
		ConversationID: source.ConversationID, ProjectID: source.ProjectID, MessageID: "daily-message", AuthorID: "selected-peer",
		Text: "Synthetic retained source: review the deployment decision.", CreatedAt: now.Add(-time.Hour), Availability: "available",
	})
	collection := state.DiscoveryCollection{From: now.Add(-2 * time.Hour), To: now, At: now, Complete: true, Count: 1, PagesFetched: 1,
		Ledger: json.RawMessage(`{"private_ledger":"synthetic-hidden-ledger"}`)}
	if _, err := store.SaveDiscoveryCollection(t.Context(), source, collection, []state.DiscoveryEvidence{evidence}); err != nil {
		t.Fatal(err)
	}
	draft := state.TopicDraft{Title: "Deployment decision", Summary: "A participant requested a deployment review.", UnresolvedQuestion: "Which deployment option should be accepted?", EvidenceIDs: []string{evidence.ID}}
	if _, err := store.SaveTopicExtraction(t.Context(), source.ID, discovery.EvidenceDigest([]state.DiscoveryEvidence{evidence}), "fixture-model", []state.TopicDraft{draft}, json.RawMessage(`{"private_audit":"synthetic-hidden-extraction-audit"}`), ""); err != nil {
		t.Fatal(err)
	}
	topics, err := store.TopicProposals(t.Context(), "pending", 10)
	if err != nil || len(topics) != 1 {
		t.Fatal("topic fixture was not retained", topics, err)
	}
	return home, topics[0].ID, evidence.ID, called
}

func discoveryBusinessSnapshot(t *testing.T, home string) map[string]int {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(home, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := map[string]int{}
	for _, table := range []string{"sessions", "runs", "memories", "candidates", "channel_messages"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func TestDiscoverySourceInspectionOmitsCacheContentsAndPrivateProcessingEvidence(t *testing.T) {
	home, _, _, called := discoveryTopicCLIFixture(t)
	before := discoveryDatabaseDigest(t, home)
	var out bytes.Buffer
	if err := Run(t.Context(), []string{"discovery", "sources", "--home", home}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var sources []map[string]any
	if err := json.Unmarshal(out.Bytes(), &sources); err != nil || len(sources) != 1 || sources[0]["extraction_status"] != "completed" || sources[0]["last_collection"] == nil || sources[0]["checkpoint"] == nil {
		t.Fatal("source coverage or extraction status was lost", sources, err)
	}
	for _, private := range []string{"Synthetic retained source", "synthetic-hidden-ledger", "synthetic-hidden-extraction-audit", "fixture-corp:fixture-owner", "extraction_audit", "ledger"} {
		if strings.Contains(out.String(), private) {
			t.Fatal("source overview exposed private cache or processing evidence", private)
		}
	}
	if after := discoveryDatabaseDigest(t, home); after != before {
		t.Fatal("source inspection mutated discovery state")
	}
	assertDiscoveryNotExecuted(t, called)
}

func TestDiscoveryTopicReviewAndExplicitAcceptancePreserveIndependentOutcomes(t *testing.T) {
	home, topicID, evidenceID, called := discoveryTopicCLIFixture(t)
	before := discoveryBusinessSnapshot(t, home)
	var out bytes.Buffer
	if err := Run(t.Context(), []string{"discovery", "show", "--home", home, topicID}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var review state.TopicExport
	if err := json.Unmarshal(out.Bytes(), &review); err != nil || review.Proposal.Status != "pending" || review.Disposition != "review_only" || len(review.Evidence) != 1 || review.Evidence[0].ID != evidenceID || !strings.Contains(review.Evidence[0].Text, "Synthetic retained source") {
		t.Fatal("pre-acceptance review did not include exact cited snapshots", review, err)
	}
	for _, args := range [][]string{
		{"discovery", "accept", "--home", home, topicID},
		{"discovery", "accept", "--project", "other-app", "--home", home, topicID},
	} {
		if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("topic acceptance omitted or widened the explicit project grant")
		}
	}
	out.Reset()
	if err := Run(t.Context(), []string{"discovery", "accept", "--project", "registered-app", "--home", home, topicID}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var accepted state.TopicExport
	if err := json.Unmarshal(out.Bytes(), &accepted); err != nil || accepted.Proposal.Status != "accepted" || accepted.Disposition != "accepted_for_owner_private_handoff_only" || len(accepted.Evidence) != 1 || accepted.Evidence[0].ID != evidenceID || accepted.Proposal.Audience != "owner-private" {
		t.Fatal("explicit topic acceptance lost its bounded owner-private evidence", accepted, err)
	}
	if after := discoveryBusinessSnapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Fatal("topic review/acceptance executed a task, sent a reply or published memory", before, after)
	}
	// Read-only show remains available after acceptance, including recovery from
	// an output failure during the original accepted evidence export.
	out.Reset()
	if err := Run(t.Context(), []string{"discovery", "show", "--home", home, topicID}, nil, &out, io.Discard); err != nil || !strings.Contains(out.String(), evidenceID) {
		t.Fatal("accepted evidence cannot be reviewed again", err)
	}
	assertDiscoveryNotExecuted(t, called)
}

func TestDiscoveryRecurringFailureStatusIsVisibleWithoutUnderlyingContent(t *testing.T) {
	privateFailure := errors.New("synthetic-private-source-and-provider-error")
	for _, test := range []struct {
		name    string
		sources []discovery.SourceReport
		err     error
		want    string
	}{
		{name: "empty success", sources: []discovery.SourceReport{}, want: "completed"},
		{name: "complete source", sources: []discovery.SourceReport{{Complete: true}}, want: "completed"},
		{name: "partial collection", sources: []discovery.SourceReport{{Complete: false}}, want: "partial"},
		{name: "partial extraction", sources: []discovery.SourceReport{{Complete: true, ExtractionPartial: true}}, want: "partial"},
		{name: "empty failure", sources: []discovery.SourceReport{}, err: privateFailure, want: "failed"},
		{name: "partial failed source", sources: []discovery.SourceReport{{Complete: false}}, err: privateFailure, want: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := discoveryCycleStatus(discovery.Report{Sources: test.sources}, test.err)
			raw, err := json.Marshal(status)
			if err != nil || status["status"] != test.want || strings.Contains(string(raw), privateFailure.Error()) || test.err != nil && status["error"] == nil {
				t.Fatal("discovery coverage/failure was hidden or disclosed provider/source content", status, err)
			}
		})
	}
}

func TestDiscoveryTopicInspectionAllIncludesPendingAndAcceptedProposals(t *testing.T) {
	home, acceptedID, _, called := discoveryTopicCLIFixture(t)
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.DiscoverySources(t.Context())
	if err != nil || len(sources) != 1 || sources[0].LastCollection == nil {
		t.Fatal("missing discovery fixture source", sources, err)
	}
	source := sources[0]
	evidence, err := store.DiscoveryEvidence(t.Context(), source.ID, 10)
	if err != nil || len(evidence) != 1 {
		t.Fatal("missing discovery fixture evidence", evidence, err)
	}
	second := evidence[0]
	second.MessageID, second.Text = "daily-second-message", "A second synthetic topic remains pending."
	second = state.PrepareDiscoveryEvidence(second)
	if _, err := store.SaveDiscoveryCollection(t.Context(), source, *source.LastCollection, []state.DiscoveryEvidence{second}); err != nil {
		t.Fatal(err)
	}
	draft := state.TopicDraft{Title: "Second topic", Summary: "Another participant question awaits owner review.", EvidenceIDs: []string{second.ID}}
	if _, err := store.SaveTopicExtraction(t.Context(), source.ID, discovery.EvidenceDigest([]state.DiscoveryEvidence{second}), "fixture-model", []state.TopicDraft{draft}, json.RawMessage(`{"result":"synthetic"}`), ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), []string{"discovery", "accept", "--project", "registered-app", "--home", home, acceptedID}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	before := discoveryDatabaseDigest(t, home)
	if err := Run(t.Context(), []string{"discovery", "topics", "--status", "all", "--home", home}, nil, &out, io.Discard); err != nil {
		t.Fatal("documented all-status topic inspection failed", err)
	}
	var topics []state.TopicProposal
	if err := json.Unmarshal(out.Bytes(), &topics); err != nil || len(topics) != 2 {
		t.Fatal("all-status inspection omitted retained topics", topics, err)
	}
	statuses := map[string]int{}
	for _, topic := range topics {
		statuses[topic.Status]++
	}
	if statuses["pending"] != 1 || statuses["accepted"] != 1 {
		t.Fatal("all-status inspection did not include pending and terminal decisions", statuses)
	}
	if after := discoveryDatabaseDigest(t, home); before != after {
		t.Fatal("all-status topic inspection mutated state")
	}
	assertDiscoveryNotExecuted(t, called)
}

func TestServiceDiscoveryConfigurationIsOptionalAndCheckedLocally(t *testing.T) {
	layout, config := newServiceFixture(t)
	if strings.Contains(strings.Join(config.arguments(layout, "serve"), " "), "--discovery-config") {
		t.Fatal("ordinary managed service enabled daily-chat collection")
	}
	path, discoveryConfig, called := discoveryCLIConfig(t)
	config.DiscoveryConfig = path
	writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
	args := strings.Join(config.arguments(layout, "serve"), " ")
	if !strings.Contains(args, "--discovery-config "+path) {
		t.Fatal("managed service lost explicit discovery selection", args)
	}
	store, err := state.Open(layout.State, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject(t.Context(), discoveryConfig.Sources[0].ProjectID, "Application", layout.Workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := discoveryDatabaseDigest(t, layout.State)
	if err := Run(t.Context(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if after := discoveryDatabaseDigest(t, layout.State); after != before {
		t.Fatal("managed discovery preflight mutated Host state")
	}
	assertDiscoveryNotExecuted(t, called)
}
