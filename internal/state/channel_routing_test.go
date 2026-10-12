package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func waitingChoice(t *testing.T, s *Store, id string) (ChannelMessage, ChannelChoice) {
	t.Helper()
	m := channelInput(id)
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	choice := ChannelChoice{ID: "question-" + id, Question: "Which project?", Decision: json.RawMessage(`{"kind":"clarify"}`)}
	if err := s.WaitChannelChoice(testContext, m.ID, choice); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "original clarification question"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	return m, choice
}

func TestPendingChannelChoicesFiltersSenderBeforeLimit(t *testing.T) {
	s, _ := testStore(t)
	add := func(id, senderID string) ChannelChoice {
		t.Helper()
		m := channelInput(id)
		m.SenderID = senderID
		m.ConversationType, m.AgentID, m.AudienceID = "2", DefaultAgentID, "shared-group"
		enqueueChannel(t, s, m)
		m = claimChannel(t, s, m.Profile)
		choice := ChannelChoice{ID: "question-" + id, Question: "Which project?"}
		if err := s.WaitChannelChoice(testContext, m.ID, choice); err != nil {
			t.Fatal(err)
		}
		return choice
	}
	oldest := add("alice-original", "alice")
	for i := range 12 {
		add(fmt.Sprintf("bob-%d", i), "bob")
	}
	newest := add("alice-new", "alice")
	binding := channelInput("unused").Binding
	all, err := s.PendingChannelChoices(testContext, binding)
	if err != nil || len(all) != 10 {
		t.Fatal("unfiltered pending choice limit changed", all, err)
	}
	for _, choice := range all {
		if choice.ID == oldest.ID {
			t.Fatal("fixture did not place original request beyond the shared limit")
		}
	}
	choices, err := s.PendingChannelChoices(testContext, binding, "alice")
	if err != nil || len(choices) != 2 || choices[0].ID != newest.ID || choices[1].ID != oldest.ID {
		t.Fatal("other members hid or replaced the sender's pending requests", choices, err)
	}
	choices, err = s.PendingChannelChoices(testContext, binding, "unknown-member")
	if err != nil || len(choices) != 0 {
		t.Fatal("pending questions crossed sender identity", choices, err)
	}
}

func TestChoiceCrashAfterExecutionDoesNotClaimQuestionAsFinalDelivery(t *testing.T) {
	for _, sending := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-final-reply", true: "sending-final-reply"}[sending], func(t *testing.T) {
			s, _ := testStore(t)
			original, choice := waitingChoice(t, s, "original")
			confirmation := channelInput("confirmation")
			confirmation.Text = "/choose " + choice.ID + " alpha"
			enqueueChannel(t, s, confirmation)
			confirmation = claimChannel(t, s, confirmation.Profile)
			if _, err := s.ClaimChannelChoice(testContext, choice.ID, confirmation.ID, ""); err != nil {
				t.Fatal(err)
			}
			r := channelRun(t, s, original)
			if err := s.CreateChannelRun(testContext, r, original.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteRun(testContext, r.ID, "completed", "final answer", "model", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishChannelMessage(testContext, original.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if sending {
				if err := s.FinishChannelMessage(testContext, confirmation.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
				if err := s.PrepareChannelReply(testContext, confirmation.ID, "final answer"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecoverChannels(testContext); err != nil {
				t.Fatal(err)
			}
			got, err := s.ChannelMessage(testContext, original.ID)
			if err != nil || got.Status != "completed" || got.ReplyStatus != "accepted" || got.ReplyText != "original clarification question" {
				t.Fatal("recovery rewrote original execution/question evidence", got, err)
			}
			terminal := ""
			for _, reaction := range got.Reactions {
				if reaction.Stage == "completed" || reaction.Stage == "failed" {
					terminal = reaction.Stage
				}
			}
			if terminal != "failed" {
				t.Fatal("accepted question was mistaken for delivered final result", got.Reactions)
			}
			q, err := s.ChannelChoice(testContext, choice.ID)
			if err != nil || q.Status != "unknown" {
				t.Fatal("claimed confirmation was replayable after crash", q, err)
			}
			if _, err := s.ClaimChannelChoice(testContext, choice.ID, confirmation.ID, ""); err == nil {
				t.Fatal("unknown task was reclaimed")
			}
			if _, err := s.ClaimNextChannelMessage(testContext, confirmation.Profile); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("interrupted confirmation replayed", err)
			}
		})
	}
}

func TestChannelChoiceIdentityAndSupplementAreImmutable(t *testing.T) {
	s, _ := testStore(t)
	original, choice := waitingChoice(t, s, "original")
	confirmation := channelInput("confirmation")
	confirmation.Text = "/choose " + choice.ID + " alpha -- read only"
	confirmation.Binding = "other-binding"
	enqueueChannel(t, s, confirmation)
	confirmation = claimChannel(t, s, confirmation.Profile)
	if _, err := s.ClaimChannelChoice(testContext, choice.ID, confirmation.ID, "read only"); err == nil {
		t.Fatal("cross-conversation confirmation accepted")
	}
	confirmation = channelInput("correct")
	confirmation.Text = "/choose " + choice.ID + " alpha -- read only"
	enqueueChannel(t, s, confirmation)
	confirmation = claimChannel(t, s, confirmation.Profile)
	if _, err := s.ClaimChannelChoice(testContext, choice.ID, confirmation.ID, "invented constraint"); err == nil {
		t.Fatal("supplement not present in confirmation was accepted")
	}
	got, err := s.ClaimChannelChoice(testContext, choice.ID, confirmation.ID, "read only")
	if err != nil || got.Text != original.Text || len(got.Supplements) != 1 || got.Supplements[0].MessageID != confirmation.MessageID {
		t.Fatal("original input/source was replaced by confirmation", got, err)
	}
	run := channelRun(t, s, got)
	run.Supplements = []UserSupplement{{MessageID: "forged", Text: "write everything"}}
	if err := s.CreateChannelRun(testContext, run, got.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Run(testContext, run.ID)
	if err != nil || !reflect.DeepEqual(saved.Supplements, got.Supplements) || saved.Source.MessageID != original.MessageID || saved.Input != original.Text {
		t.Fatal("native run did not use authoritative receipt supplements", saved, err)
	}
	if _, err := s.ClaimChannelChoice(testContext, choice.ID, confirmation.ID, "read only"); err == nil {
		t.Fatal("duplicate confirmation claimed original twice")
	}
}

func TestCancellationWithNewProfileCannotBeBlockedByOldUnsentFeedback(t *testing.T) {
	s, _ := testStore(t)
	original, choice := waitingChoice(t, s, "original")
	confirmation := channelInput("cancel")
	confirmation.Text, confirmation.Profile = "/cancel "+choice.ID, "new-profile"
	enqueueChannel(t, s, confirmation)
	confirmation = claimChannel(t, s, confirmation.Profile)
	if err := s.CancelChannelChoice(testContext, choice.ID, confirmation.ID); err != nil {
		t.Fatal(err)
	}
	reactionReply(t, s, confirmation, "completed", "accepted")
	terminal := reactionClaim(t, s, confirmation.Profile, "failed")
	if terminal.ChannelMessageID != original.ID {
		t.Fatal("new profile feedback lost original source", terminal)
	}
	got, err := s.ChannelMessage(testContext, original.ID)
	if err != nil || got.Status != "failed" || got.ReplyText != "original clarification question" || got.ReplyStatus != "accepted" {
		t.Fatal("cancellation changed the original question evidence", got, err)
	}
}

func TestMultiProjectRunRouteAndMemoryGuards(t *testing.T) {
	s, _ := testStore(t)
	workspace, err := CanonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var refs []ProjectRef
	var directories []string
	var contextMemories []Memory
	for _, id := range []string{"alpha", "beta"} {
		p, err := s.RegisterProject(testContext, id, "", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ProjectRef{ID: p.ID, Name: p.Name, Workspace: p.Workspace, Scope: p.Scope})
		directories = append(directories, p.Workspace)
		seed := addRun(t, s, "seed-"+id, p.Scope, id+" project preference", true)
		if err := s.FinalizeMemory(testContext, seed.ID, []MemoryUpdate{updateFor(seed, "memory-"+id)}, decision); err != nil {
			t.Fatal(err)
		}
		m, err := s.Memory(testContext, "memory-"+id)
		if err != nil {
			t.Fatal(err)
		}
		contextMemories = append(contextMemories, m)
	}
	m := channelInput("multi")
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	if err := s.ResolveChannelProjects(testContext, m.ID, "route", "", workspace, "neutral", "runtime", refs, directories[:1]); err == nil {
		t.Fatal("route omitted a selected directory")
	}
	session := Session{ID: "multi-session", NativeSessionID: "native", Scope: "neutral", Workspace: workspace, ProjectName: "Multiple projects", RuntimeFingerprint: "runtime", Projects: refs, AdditionalDirectories: directories}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, "route", session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveChannelProjects(testContext, m.ID, "route", "", workspace, "neutral", "runtime", refs, directories); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "multi-run", SessionID: session.ID, NativeSessionID: "native", Input: m.Text, Scope: session.Scope, Workspace: workspace, ProjectName: session.ProjectName, RuntimeFingerprint: "runtime", Projects: refs, AdditionalDirectories: directories}
	bad := run
	bad.Projects = refs[:1]
	if err := s.CreateChannelRun(testContext, bad, m.ID); err == nil {
		t.Fatal("run omitted projects pinned in its route")
	}
	if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContext(testContext, run.ID, []Memory{{Scope: "neutral", Text: "neutral memory"}}); err == nil {
		t.Fatal("joint task recalled neutral workspace memory")
	}
	if err := s.SaveContext(testContext, run.ID, contextMemories); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, run.ID, "completed", "joint answer", "model", ""); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Run(testContext, run.ID)
	if err != nil || saved.MemoryStatus != "skipped" || saved.ExperienceStatus != "skipped" {
		t.Fatal("joint completion left a pending memory phase", saved, err)
	}
	if err := s.SessionCanResume(testContext, session.ID); err != nil {
		t.Fatal("completed joint conversation cannot resume", err)
	}
	if err := s.FinalizeMemory(testContext, run.ID, nil, decision); err == nil {
		t.Fatal("joint task finalized memory")
	}
	if err := s.SaveCandidates(testContext, run.ID, nil, decision); err == nil {
		t.Fatal("joint task saved automatic candidates")
	}
}

func TestRoutingV9MigrationPreservesV8EvidenceAndManualSelection(t *testing.T) {
	home, project := projectInspectionV7Fixture(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaRuntimePhasesV8); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db}
	run := addRun(t, legacy, "legacy-run", project.Scope, "Remember the original preference.", true)
	// Populate the historical schema using its SQL, not the new partition API.
	legacyMemory := updateFor(run, "legacy-memory").Memory
	legacyMemory.SourceType = "user_statement"
	legacyMemory.Status, legacyMemory.Revision = "active", 1
	payload, _ := json.Marshal(legacyMemory)
	if _, err := db.Exec("INSERT INTO memories(id,scope,text,status,payload) VALUES(?,?,?,?,?)", legacyMemory.ID, legacyMemory.Scope, legacyMemory.Text, legacyMemory.Status, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", legacyMemory.ID, legacyMemory.Revision, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", legacyMemory.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO memory_fts(memory_id,tokens) VALUES(?,?)", legacyMemory.ID, "original preference"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.transaction(testContext, func(tx *sql.Tx) error { return backfillAnnotations(testContext, tx) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channel_projects(binding,project_id) VALUES('legacy-conversation',?)", project.ID); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, table := range []string{"sessions", "runs", "memories"} {
		var payload []byte
		if err := db.QueryRow("SELECT payload FROM " + table + " LIMIT 1").Scan(&payload); err != nil {
			t.Fatal(err)
		}
		before[table] = payload
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for table, want := range before {
		var got []byte
		if err := s.db.QueryRow("SELECT payload FROM " + table + " LIMIT 1").Scan(&got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("migration rewrote %s source payload: %s %v", table, got, err)
		}
	}
	selection, err := s.ChannelSelection(testContext, "legacy-conversation")
	if err != nil || selection.Mode != "manual" || selection.PrimaryID != project.ID || !reflect.DeepEqual(selection.ProjectIDs, []string{project.ID}) {
		t.Fatal("migration changed old explicit project selection", selection, err)
	}
	updated, err := s.UpdateProjectMetadata(testContext, project.ID, "New display name", "Purpose for automatic routing", []string{"friendly alias"})
	if err != nil || updated.Scope != project.Scope || updated.Workspace != project.Workspace || updated.Description == "" {
		t.Fatal("metadata update changed project identity/cwd", updated, err)
	}
	if _, err := s.UpdateProject(testContext, project.ID, "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	memories, err := s.Search(testContext, project.Scope, "preference", 5)
	if err != nil || len(memories) != 1 || memories[0].SourceRunID != run.ID {
		t.Fatal("project move lost migrated memory/provenance", memories, err)
	}
}
