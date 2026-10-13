package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"agent-host/internal/state"
)

func assistanceCLIFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	path, cfg, called := discoveryCLIConfig(t)
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.RegisterProject(t.Context(), cfg.Sources[0].ProjectID, "Application", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	source := cfg.Sources[0].Attribution(cfg)
	now := time.Now().UTC().Truncate(time.Millisecond)
	e := state.PrepareDiscoveryEvidence(state.DiscoveryEvidence{SourceID: source.ID, Profile: source.Profile, TenantID: source.TenantID, PrincipalID: source.PrincipalID, ConversationID: source.ConversationID, ProjectID: source.ProjectID, MessageID: "selected-question", AuthorID: "owner", Text: "Which readiness probe should deployment use?", CreatedAt: now, Availability: "available"})
	evidence := []state.DiscoveryEvidence{e}
	c := state.DiscoveryCollection{From: now.Add(-time.Hour), To: now.Add(time.Hour), At: now, Complete: true, Count: 1}
	if _, err = store.SaveDiscoveryCollection(t.Context(), source, c, evidence); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveMatterExtraction(t.Context(), source.ID, state.AssistanceDigest(evidence), "fixture", []state.MatterDraft{{Kind: "question", Status: "open", Title: "Deployment readiness", Summary: "Owner requested readiness guidance.", OpenItem: e.Text, EvidenceIDs: []string{e.ID}}}, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	list, err := store.Suggestions(t.Context(), source.ProjectID, []string{source.ID})
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	return home, path, list[0].ID, called
}

func TestAssistancePrivateListAndFeedbackRequireCurrentScopeWithoutCredentials(t *testing.T) {
	home, path, id, called := assistanceCLIFixture(t)
	t.Setenv("JEV_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	common := []string{"--home", home, "--project", "registered-app", "--config", path}
	var out bytes.Buffer
	if err := Run(t.Context(), append([]string{"assistance", "suggestions"}, common...), nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Prepare a cited brief")) || !bytes.Contains(out.Bytes(), []byte("participant")) {
		t.Fatal("private list omitted action or evidence boundary", out.String())
	}
	args := append([]string{"assistance", "dismiss"}, common...)
	args = append(args, "--revision", "1", id)
	if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(t.Context(), append([]string{"assistance", "suggestions"}, common...), nil, &out, io.Discard); err != nil || out.String() != "[]\n" {
		t.Fatal("dismissed suggestion returned", out.String(), err)
	}
	if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("stale revision accepted")
	}
	for _, args := range [][]string{{"assistance", "suggestions", "--home", home, "--project", "other-app", "--config", path}, {"assistance", "suggestions", "--home", home, "--project", "registered-app", "--config", path, "--audience", "group:one"}} {
		if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("unauthorized private list accepted")
		}
	}
	assertDiscoveryNotExecuted(t, called)
}

func TestAssistanceNeedsExplicitSelectionAndContextMCPCannotChooseGrants(t *testing.T) {
	for _, args := range [][]string{{"assistance", "prepare"}, {"context-mcp", "--home", "relative", "--run", "guessed", "--fingerprint", "guessed"}, {"context-mcp", "--home", t.TempDir(), "--run", "guessed", "--fingerprint", "guessed", "--project", "other-app"}} {
		if err := Run(t.Context(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid assistance entry accepted")
		}
	}
}
