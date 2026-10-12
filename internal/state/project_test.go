package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProjectRegistrationCanonicalIdentityAndValidation(t *testing.T) {
	s, _ := testStore(t)
	workspace := t.TempDir()
	path, err := CanonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	p, err := s.RegisterProject(testContext, "alpha", "Alpha", alias)
	if err != nil || p.ID != "alpha" || p.Name != "Alpha" || p.Workspace != path || p.Scope != "project:alpha" || p.CreatedAt.IsZero() || !p.CreatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("project %+v %v", p, err)
	}
	for _, lookup := range []func() (Project, error){func() (Project, error) { return s.Project(testContext, "alpha") }, func() (Project, error) { return s.ProjectByWorkspace(testContext, workspace) }, func() (Project, error) { return s.ProjectByScope(testContext, p.Scope) }} {
		got, err := lookup()
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Fatalf("lookup %+v %v", got, err)
		}
	}
	if _, err := s.RegisterProject(testContext, "alpha", "", t.TempDir()); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	if _, err := s.RegisterProject(testContext, "other", "", workspace); err == nil {
		t.Fatal("canonical duplicate workspace accepted")
	}
	for _, id := range []string{"", "default", "Upper", "-invalid", "bad/id", "中文", strings.Repeat("a", 65)} {
		if _, err := s.RegisterProject(testContext, id, "", t.TempDir()); err == nil {
			t.Fatalf("invalid ID %q accepted", id)
		}
	}
	if _, err := s.RegisterProject(testContext, "absent", "", filepath.Join(workspace, "missing")); err == nil {
		t.Fatal("missing workspace accepted")
	}
	file := filepath.Join(workspace, "file")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterProject(testContext, "file", "", file); err == nil {
		t.Fatal("file workspace accepted")
	}
	all, err := s.Projects(testContext)
	if err != nil || len(all) != 1 {
		t.Fatalf("projects %+v %v", all, err)
	}
	if _, err := s.Project(testContext, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project %v", err)
	}
	if _, err := s.ProjectByScope(testContext, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing scope %v", err)
	}
}

func TestProjectAdoptsLegacyScopeAndMoveRetainsExactHistory(t *testing.T) {
	s, _ := testStore(t)
	path, err := CanonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := addRun(t, s, "legacy", path, "Use Go for this project", true)
	if err := s.FinalizeMemory(testContext, run.ID, []MemoryUpdate{updateFor(run, "legacy-memory")}, decision); err != nil {
		t.Fatal(err)
	}
	c := experienceCandidate(t, s, "legacy-candidate", path)
	saveCandidate(t, s, c)
	var beforeRun, beforeMemory, beforeCandidate, beforeRevision []byte
	for _, item := range []struct {
		query string
		value *[]byte
	}{{"SELECT payload FROM runs WHERE id='legacy'", &beforeRun}, {"SELECT payload FROM memories WHERE id='legacy-memory'", &beforeMemory}, {"SELECT payload FROM candidates WHERE id='legacy-candidate'", &beforeCandidate}, {"SELECT payload FROM memory_revisions WHERE memory_id='legacy-memory'", &beforeRevision}} {
		if err := s.db.QueryRow(item.query).Scan(item.value); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.RegisterProject(testContext, "legacy", "Legacy", path)
	if err != nil || p.Scope != path {
		t.Fatalf("adopted %+v %v", p, err)
	}
	moved, err := s.UpdateProject(testContext, p.ID, "", t.TempDir())
	if err != nil || moved.Scope != p.Scope || moved.Name != p.Name || moved.Workspace == p.Workspace || !moved.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("moved %+v %v", moved, err)
	}
	if _, err := s.RegisterProject(testContext, "new-owner", "", path); err == nil {
		t.Fatal("reserved historical scope adopted by another project")
	}
	for _, item := range []struct {
		query  string
		before []byte
	}{{"SELECT payload FROM runs WHERE id='legacy'", beforeRun}, {"SELECT payload FROM memories WHERE id='legacy-memory'", beforeMemory}, {"SELECT payload FROM candidates WHERE id='legacy-candidate'", beforeCandidate}, {"SELECT payload FROM memory_revisions WHERE memory_id='legacy-memory'", beforeRevision}} {
		var after []byte
		if err := s.db.QueryRow(item.query).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(item.before, after) {
			t.Fatalf("registration rewrote %s", item.query)
		}
	}
	found, err := s.Search(testContext, moved.Scope, "Go", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("recall after move %+v %v", found, err)
	}
	accepted := acceptCandidate(t, s, c.ID, "reviewed", nil, nil)
	if accepted.Scope != p.Scope {
		t.Fatalf("review scope %+v", accepted)
	}
	old, err := s.Run(testContext, run.ID)
	if err != nil || old.Workspace != "" || old.WorkingDirectory() != path {
		t.Fatalf("legacy read %+v %v", old, err)
	}
}

func TestProjectAdoptsSessionOnlyHistoryAndRejectsUpdateCollision(t *testing.T) {
	s, _ := testStore(t)
	path, err := CanonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(testContext, Session{ID: "legacy-only", Scope: path, NativeSessionID: "native"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.RegisterProject(testContext, "session-only", "", path)
	if err != nil || p.Scope != path || p.Name != p.ID {
		t.Fatalf("session history %+v %v", p, err)
	}
	other, err := s.RegisterProject(testContext, "other", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProject(testContext, p.ID, "", other.Workspace); err == nil {
		t.Fatal("workspace collision accepted")
	}
	if _, err := s.UpdateProject(testContext, other.ID, "", path); err == nil {
		t.Fatal("scope collision accepted")
	}
}

func TestChannelProjectSelectionSurvivesReopen(t *testing.T) {
	s, home := testStore(t)
	p, err := s.RegisterProject(testContext, "alpha", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelProject(testContext, "conversation"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing selection %v", err)
	}
	if err := s.SetChannelProject(testContext, "conversation", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChannelProject(testContext, "conversation", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown project selected %v", err)
	}
	if err := s.SetChannelProject(testContext, "", p.ID); err == nil {
		t.Fatal("empty binding accepted")
	}
	readOnly, err := Open(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	got, err := readOnly.ChannelProject(testContext, "conversation")
	if err != nil || got != p.ID {
		t.Fatalf("reopened selection %q %v", got, err)
	}
	if err := s.SetChannelProject(testContext, "conversation", ""); err != nil {
		t.Fatal(err)
	}
	got, err = s.ChannelProject(testContext, "conversation")
	if err != nil || got != "" {
		t.Fatalf("explicit default %q %v", got, err)
	}
}

func TestSchemaV5ProjectMigrationPreservesLegacyPayload(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + schemaV2 + schemaV3 + schemaV4 + schemaV5); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"id":"old","scope":"/old/workspace","model":"test","native_session_id":"native","created_at":"2026-10-10T00:00:00Z"}`)
	if _, err := db.Exec("INSERT INTO sessions(id,payload) VALUES('old',?)", legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home, true); err == nil {
		t.Fatal("v5 opened read-only without migration")
	}
	s, err := Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var after []byte
	var version int
	if err := s.db.QueryRow("SELECT payload FROM sessions WHERE id='old'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion || !bytes.Equal(after, legacy) {
		t.Fatalf("migration version=%d payload=%s err=%v", version, after, err)
	}
	session, err := s.Session(testContext, "old")
	if err != nil || session.Workspace != "" || session.WorkingDirectory() != "/old/workspace" {
		t.Fatalf("legacy session %+v %v", session, err)
	}
}

func TestRunRejectsMismatchedAssembly(t *testing.T) {
	s, _ := testStore(t)
	session := Session{ID: "session", Scope: "project:alpha", Model: "model", NativeSessionID: "native", ProjectID: "alpha", ProjectName: "Alpha", Workspace: "/alpha", RuntimeFingerprint: "runtime"}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run", SessionID: session.ID, Scope: session.Scope, NativeSessionID: session.NativeSessionID, Input: "task", ProjectID: session.ProjectID, ProjectName: session.ProjectName, Workspace: session.Workspace, RuntimeFingerprint: session.RuntimeFingerprint}
	for _, mutate := range []func(*Run){func(r *Run) { r.ProjectID = "beta" }, func(r *Run) { r.ProjectName = "Other" }, func(r *Run) { r.Workspace = "/beta" }, func(r *Run) { r.RuntimeFingerprint = "changed" }} {
		bad := run
		mutate(&bad)
		if err := s.CreateRun(testContext, bad); err == nil {
			t.Fatal("mismatched assembly accepted")
		}
	}
	if err := s.CreateRun(testContext, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(testContext, run.ID)
	if err != nil || got.WorkingDirectory() != session.Workspace {
		t.Fatalf("assembled run %+v %v", got, err)
	}
}

func TestEvidenceUsesLogicalScopeAndOriginalWorkspace(t *testing.T) {
	s, _ := testStore(t)
	p, err := s.RegisterProject(testContext, "alpha", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "session", Scope: p.Scope, NativeSessionID: "native", ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: "runtime"}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run", SessionID: session.ID, Scope: p.Scope, NativeSessionID: session.NativeSessionID, Input: "Fix the parser", ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: session.RuntimeFingerprint}
	if err := s.CreateRun(testContext, run); err != nil {
		t.Fatal(err)
	}
	recordTools(t, s, run.ID)
	if err := s.CompleteRun(testContext, run.ID, "completed", "answer", "model", ""); err != nil {
		t.Fatal(err)
	}
	c := Candidate{ID: "candidate", RunID: run.ID, Scope: p.Scope, Model: "extractor", Assessment: decision, Draft: ExperienceDraft{Title: "Parser", Summary: "Observed parser fix", Conditions: "This parser", Steps: []string{"Fix parser"}, EvidenceIDs: []string{"edit"}}}
	if _, err := s.UpdateProject(testContext, p.ID, "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	saveCandidate(t, s, c)
	got, err := s.GetCandidate(testContext, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"file:" + filepath.Join(p.Workspace, "parser.go"), p.Scope}
	if !reflect.DeepEqual(got.Entities, want) {
		t.Fatalf("source workspace lost: %v want %v", got.Entities, want)
	}
}

func TestResolvedChannelRouteIsPinnedAndExecutionUsesIt(t *testing.T) {
	s, _ := testStore(t)
	p, err := s.RegisterProject(testContext, "alpha", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := channelInput("routed")
	m.ProjectID = "forged"
	m.RouteBinding = "forged"
	enqueueChannel(t, s, m)
	queued, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || queued.ProjectID != "" || queued.RouteBinding != "" {
		t.Fatalf("injected route %+v %v", queued, err)
	}
	resolve := func(binding string) error {
		return s.ResolveChannelRoute(testContext, m.ID, binding, p.ID, p.Workspace, p.Scope, "runtime")
	}
	if err := resolve("route-alpha"); err == nil {
		t.Fatal("unclaimed route resolved")
	}
	m = claimChannel(t, s, m.Profile)
	if err := resolve("route-alpha"); err != nil {
		t.Fatal(err)
	}
	if err := resolve("route-alpha"); err != nil {
		t.Fatal("same route not idempotent", err)
	}
	if err := resolve("different"); err == nil {
		t.Fatal("route changed")
	}
	got, err := s.ChannelRoute(testContext, m.Binding, p.ID)
	if err != nil || got.ID != m.ID || got.Binding != m.Binding || got.Scope != m.Scope || got.ResolvedScope != p.Scope {
		t.Fatalf("pinned route %+v %v", got, err)
	}
	session := Session{ID: "session", Scope: p.Scope, NativeSessionID: "native", ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: "runtime"}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, "route-alpha", session.ID); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "run", SessionID: session.ID, Scope: p.Scope, Input: m.Text, NativeSessionID: session.NativeSessionID, ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: session.RuntimeFingerprint}
	bad := run
	bad.Workspace = "/wrong"
	if err := s.CreateChannelRun(testContext, bad, m.ID); err == nil {
		t.Fatal("run escaped pinned workspace")
	}
	if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := resolve("route-alpha"); err == nil {
		t.Fatal("route resolved after run")
	}
	saved, err := s.Run(testContext, run.ID)
	if err != nil || saved.Source.ChannelMessageID != m.ID || saved.Source.SenderID != m.SenderID {
		t.Fatalf("route provenance %+v %v", saved, err)
	}
	if err := s.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	got, err = s.ChannelMessage(testContext, m.ID)
	if err != nil || got.Status != "unknown" || got.RouteBinding != "route-alpha" || got.ResolvedScope != p.Scope {
		t.Fatalf("recovery route %+v %v", got, err)
	}
}

func TestRouteRejectsUnknownProjectAndMissingWorkspace(t *testing.T) {
	s, _ := testStore(t)
	m := channelInput("bad-route")
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	workspace := t.TempDir()
	if err := s.ResolveChannelRoute(testContext, m.ID, "route", "missing", workspace, "project:missing", "runtime"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown project route %v", err)
	}
	p, err := s.RegisterProject(testContext, "alpha", "", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveChannelRoute(testContext, m.ID, "route", p.ID, p.Workspace, "wrong", "runtime"); err == nil {
		t.Fatal("wrong project scope accepted")
	}
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveChannelRoute(testContext, m.ID, "route", p.ID, p.Workspace, p.Scope, "runtime"); err == nil {
		t.Fatal("missing project directory routed")
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || got.RouteBinding != "" {
		t.Fatalf("failed route partially persisted %+v %v", got, err)
	}
}

func TestChannelRouteFindsLatestPinnedRouteForTheSelectedProject(t *testing.T) {
	s, _ := testStore(t)
	for _, id := range []string{"alpha", "beta"} {
		p, err := s.RegisterProject(testContext, id, "", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		m := channelInput(id)
		enqueueChannel(t, s, m)
		m = claimChannel(t, s, m.Profile)
		if err := s.ResolveChannelRoute(testContext, m.ID, "route-"+id, p.ID, p.Workspace, p.Scope, "runtime"); err != nil {
			t.Fatal(err)
		}
	}
	// New receipt cannot obscure a previously resolved project's environment.
	enqueueChannel(t, s, channelInput("unresolved"))
	for _, id := range []string{"alpha", "beta"} {
		got, err := s.ChannelRoute(testContext, "binding", id)
		if err != nil || got.ID != id || got.ProjectID != id {
			t.Fatalf("project route %+v %v", got, err)
		}
	}
	if _, err := s.ChannelRoute(testContext, "binding", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project route leaked into default: %v", err)
	}
}

func TestNewCommandRoutePinsBoundSessionWithoutNativeRun(t *testing.T) {
	s, _ := testStore(t)
	p, err := s.RegisterProject(testContext, "alpha", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := channelInput("new-command")
	m.Text = "/new"
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	session := Session{ID: "fresh-session", Scope: p.Scope, NativeSessionID: "fresh-native", ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: "runtime"}
	if err := s.CreateSession(testContext, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, "route-alpha", session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveChannelRoute(testContext, m.ID, "route-alpha", p.ID, p.Workspace, p.Scope, "runtime"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || got.SessionID != session.ID || got.RunID != "" || got.RouteBinding != "route-alpha" || len(got.Reactions) != 0 {
		t.Fatalf("new command snapshot %+v %v", got, err)
	}
	if hasRun, err := s.SessionHasRun(testContext, session.ID); err != nil || hasRun {
		t.Fatalf("new command executed: %v %v", hasRun, err)
	}
}

func TestChannelRunRejectsRebindingAfterSessionWasPinned(t *testing.T) {
	s, _ := testStore(t)
	p, err := s.RegisterProject(testContext, "alpha", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := channelInput("pinned-session")
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	original := Session{ID: "original-session", Scope: p.Scope, NativeSessionID: "original-native", ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: "runtime"}
	if err := s.CreateSession(testContext, original); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, "route-alpha", original.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveChannelRoute(testContext, m.ID, "route-alpha", p.ID, p.Workspace, p.Scope, "runtime"); err != nil {
		t.Fatal(err)
	}
	replacement := original
	replacement.ID, replacement.NativeSessionID = "replacement-session", "replacement-native"
	if err := s.CreateSession(testContext, replacement); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(testContext, "route-alpha", replacement.ID); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "rebound-run", SessionID: replacement.ID, Scope: p.Scope, Input: m.Text, NativeSessionID: replacement.NativeSessionID, ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, RuntimeFingerprint: "runtime"}
	if err := s.CreateChannelRun(testContext, run, m.ID); err == nil {
		t.Fatal("run used a session different from its pinned snapshot")
	}
	if _, err := s.Run(testContext, run.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected run persisted: %v", err)
	}
	got, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || got.SessionID != original.ID || got.RunID != "" || got.Status != "claimed" {
		t.Fatalf("pin was changed %+v %v", got, err)
	}
	// Restoring the selected binding permits the original execution and proves
	// the rejection did not consume this message's only run.
	if err := s.BindChannelSession(testContext, "route-alpha", original.ID); err != nil {
		t.Fatal(err)
	}
	run.SessionID, run.NativeSessionID = original.ID, original.NativeSessionID
	if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
}

// Ensure the legacy compatibility helpers do not add fields during inspection.
func TestWorkingDirectoryDoesNotRewriteLegacyJSON(t *testing.T) {
	r := Run{Scope: "/workspace"}
	session := Session{Scope: "/workspace"}
	if r.WorkingDirectory() != "/workspace" || session.WorkingDirectory() != "/workspace" {
		t.Fatal("legacy fallback lost")
	}
	b, err := json.Marshal(r)
	if err != nil || strings.Contains(string(b), `"workspace":`) {
		t.Fatalf("legacy run JSON %s %v", b, err)
	}
}
