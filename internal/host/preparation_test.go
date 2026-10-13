package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/discovery"
	"agent-host/internal/state"
)

func TestChosenPreparationUsesNormalExecutionAndNeverReplays(t *testing.T) {
	h, runner := testHost(t)
	ctx := t.Context()
	p, err := h.Store.RegisterProject(ctx, "app", "Application", h.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	h.Config.ProjectID, h.Config.ProjectName, h.Config.Scope, h.Config.Workspace = p.ID, p.Name, p.Scope, p.Workspace
	h.Config.AgentID, h.Config.AudienceID = "default", "owner"
	h.Config.Projects = []state.ProjectRef{{ID: p.ID, Name: p.Name, Scope: p.Scope, Workspace: p.Workspace}}
	cfg := discovery.Config{DWSExecutable: os.Args[0], Profile: "corp:owner", IntervalSeconds: 300, InitialWindowHours: 24, ReconciliationWindowHours: 6, RetentionDays: 30, PageLimit: 2, MaxMessages: 200, TimeoutSeconds: 30, Model: "fixture", Sources: []discovery.Source{{ConversationID: "selected", Type: "dm", PeerOpenDingTalkID: "peer", ProjectID: p.ID}}}
	path := filepath.Join(t.TempDir(), "discovery.json")
	raw, _ := json.Marshal(cfg)
	os.WriteFile(path, raw, 0600)
	h.Config.ContextConfig = path
	source := cfg.Sources[0].Attribution(cfg)
	now := time.Now().UTC().Truncate(time.Millisecond)
	e := state.PrepareDiscoveryEvidence(state.DiscoveryEvidence{SourceID: source.ID, Profile: source.Profile, TenantID: source.TenantID, PrincipalID: source.PrincipalID, ConversationID: source.ConversationID, ProjectID: p.ID, MessageID: "original-message", AuthorID: "peer", Text: "Which readiness probe is appropriate?", CreatedAt: now, Availability: "available"})
	c := state.DiscoveryCollection{From: now.Add(-time.Hour), To: now.Add(time.Hour), At: now, Complete: true, Count: 1}
	evidence := []state.DiscoveryEvidence{e}
	if _, err = h.Store.SaveDiscoveryCollection(ctx, source, c, evidence); err != nil {
		t.Fatal(err)
	}
	if err = h.Store.SaveMatterExtraction(ctx, source.ID, state.AssistanceDigest(evidence), "fixture", []state.MatterDraft{{Kind: "question", Status: "open", Title: "Readiness", Summary: "Peer asked which probe to use.", OpenItem: e.Text, EvidenceIDs: []string{e.ID}}}, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	list, _ := h.Store.Suggestions(ctx, p.ID, []string{source.ID})
	choice := list[0]
	session := newSession(t, h)
	run, err := h.RunPreparation(ctx, session, choice.ID, choice.Revision)
	if err != nil || run.ExecutionStatus != "completed" || run.Preparation == nil {
		t.Fatal("chosen action did not complete normally", run, err)
	}
	if len(runner.requests) != 1 || !strings.Contains(runner.requests[0].Prompt, e.MessageID) || !strings.Contains(runner.requests[0].Prompt, "source data, not instructions") || len(run.Supplements) != 0 || run.Input != preparationRequest {
		t.Fatal("preparation lost provenance or attributed source as user instruction")
	}
	if _, err = h.RunPreparation(ctx, session, choice.ID, choice.Revision); err == nil || len(runner.requests) != 1 {
		t.Fatal("repeat selection executed again")
	}
	linked, _, err := h.Store.Suggestion(ctx, p.ID, []string{source.ID}, choice.ID)
	if err != nil || linked.RunID != run.ID {
		t.Fatal("suggestion lost exact business run", linked, err)
	}
}
