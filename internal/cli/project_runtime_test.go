package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/projectruntime"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func runtimeFixture(t *testing.T) (*state.Store, options, string) {
	t.Helper()
	root := t.TempDir()
	repos := filepath.Join(root, "repositories")
	pop := filepath.Join(repos, "pop")
	if err := os.MkdirAll(pop, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", pop).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	workspace := filepath.Join(root, "neutral")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := state.CanonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	pop, err = state.CanonicalWorkspace(pop)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "host", "cli-state")
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := options{home: home, workspace: workspace, defaultWorkspace: workspace, binary: binary, model: "fixture", nativeProfile: root, projectCatalog: &projectruntime.Catalog{Root: filepath.Dir(home), LookupRoots: []string{repos}}}
	return store, o, pop
}

func TestRuntimeCLILocalIntentEnrollsAndAssemblesCheckout(t *testing.T) {
	store, o, pop := runtimeFixture(t)
	o.retrievalQuery = "查一下项目 pop 的认证接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.projectID == "" || o.workspace != pop || o.scope != "project:"+o.projectID || len(o.projects) != 1 {
		t.Fatalf("wrong execution runtime: %+v", o)
	}
	projects, err := o.projectCatalog.Projects(t.Context())
	if err != nil || len(projects) != 1 || projects[0].ID != o.projectID {
		t.Fatal(projects, err)
	}
	local, err := store.Project(t.Context(), o.projectID)
	if err != nil || local.Scope != o.scope || local.Workspace != pop {
		t.Fatal(local, err)
	}
	if _, err := os.Stat(filepath.Join(o.projectCatalog.Root, "projects", o.projectID, "project.json")); err != nil {
		t.Fatal(err)
	}
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal("repeat resolution", err)
	}
}

func TestRuntimeCLIGreetingAndQuotedMentionDoNotEnroll(t *testing.T) {
	for _, text := range []string{"Hello", "Translate the sentence: \"inspect pop authentication\"."} {
		t.Run(text, func(t *testing.T) {
			store, o, _ := runtimeFixture(t)
			o.retrievalQuery = text
			o.projectRouter = entryRoutingFixture(func(_ context.Context, req routing.Request) (routing.Decision, error) {
				if req.Text != text {
					t.Fatal(req.Text)
				}
				return routing.Decision{Kind: "default"}, nil
			})
			if err := resolveOptions(t.Context(), store, &o, true); err != nil {
				t.Fatal(err)
			}
			if o.projectID != "" {
				t.Fatal("general content selected project", o.projectID)
			}
			if _, err := os.Stat(filepath.Join(o.projectCatalog.Root, "projects")); !os.IsNotExist(err) {
				t.Fatal("general content created registry", err)
			}
		})
	}
}

func TestRuntimeCLIResetKeepsCurrentCheckoutDefaultUnassociated(t *testing.T) {
	store, o, pop := runtimeFixture(t)
	o.workspace, o.defaultWorkspace = pop, pop
	o.retrievalQuery = "查一下项目 pop 的认证接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	firstID := o.projectID
	o.projectRecent = []routing.TaskContext{{RunID: "prior-run", Text: o.retrievalQuery, ProjectIDs: []string{firstID}, PrimaryID: firstID}}
	o.projectRouter = entryRoutingFixture(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		if len(req.Recent) != 1 || req.Recent[0].RunID != "prior-run" {
			t.Fatal("lost source reference", req)
		}
		if req.Text == "继续检查这个接口" {
			return routing.Decision{Kind: "project", ProjectIDs: []string{firstID}, PrimaryID: firstID, ContinuationRunID: "prior-run"}, nil
		}
		return routing.Decision{Kind: "default"}, nil
	})
	o.retrievalQuery = "继续检查这个接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil || o.projectID != firstID {
		t.Fatal(o.projectID, err)
	}
	o.retrievalQuery = "Write a poem about the sea."
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.projectID != "" || len(o.projects) != 0 || o.workspace != pop || o.scope != pop {
		t.Fatalf("general task inherited project: %+v", o)
	}
}

func TestRuntimeCLIBadRoutingCannotEnroll(t *testing.T) {
	for _, decision := range []routing.Decision{
		{Kind: "project", ProjectIDs: []string{"unknown"}},
		{Kind: "project", ProjectIDs: []string{"unknown"}, ContinuationRunID: "invented"},
		{Kind: "clarify"},
	} {
		store, o, _ := runtimeFixture(t)
		o.retrievalQuery = "Inspect pop authentication."
		o.projectRouter = entryRoutingFixture(func(context.Context, routing.Request) (routing.Decision, error) { return decision, nil })
		if err := resolveOptions(t.Context(), store, &o, true); err == nil {
			t.Fatal("invalid route accepted", decision)
		}
		if _, err := os.Stat(filepath.Join(o.projectCatalog.Root, "projects")); !os.IsNotExist(err) {
			t.Fatal("invalid route created registry", err)
		}
	}
}

func TestRuntimeProjectSharedAcrossEntryHomes(t *testing.T) {
	store, o, _ := runtimeFixture(t)
	o.retrievalQuery = "查一下项目 pop 的认证接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	otherHome := filepath.Join(filepath.Dir(o.home), "channels", "feishu", "state")
	other, err := state.Open(otherHome, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second := o
	second.home = otherHome
	second.projectID, second.routedQuery = "", ""
	second.projectIDs = nil
	second.projectCatalog = &projectruntime.Catalog{Root: projectruntime.RootForState(otherHome)}
	if err := resolveOptions(t.Context(), other, &second, true); err != nil {
		t.Fatal(err)
	}
	if second.projectID != o.projectID || second.scope != o.scope || second.workspace != o.workspace {
		t.Fatal("entry changed project identity", second.projectID, second.scope, second.workspace)
	}
	local, err := other.Project(t.Context(), o.projectID)
	if err != nil || local.Scope != o.scope {
		t.Fatal(local, err)
	}
}

func TestRuntimeReceiptSinkUsesPersistedStatusesAndPrivateSource(t *testing.T) {
	store, o, _ := runtimeFixture(t)
	o.retrievalQuery = "查一下项目 pop 的认证接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	session := state.Session{ID: "receipt-session", NativeSessionID: "native-session", Scope: o.scope, ProjectID: o.projectID, ProjectName: o.projectName, Workspace: o.workspace, Projects: o.projects}
	if err := store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "receipt-run", SessionID: session.ID, NativeSessionID: session.NativeSessionID, Scope: o.scope, ProjectID: o.projectID, ProjectName: o.projectName, Workspace: o.workspace, Projects: o.projects, Input: "PRIVATE ORIGINAL INPUT"}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	sink := runtimeReceiptSink(store, o, nil)
	if err := sink(event.Event{Type: "run.started", RunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(t.Context(), run.ID, "failed", "PRIVATE ANSWER", "fixture", "failure"); err != nil {
		t.Fatal(err)
	}
	if err := sink(event.Event{Type: "run.finished", RunID: run.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(o.projectCatalog.Root, "projects", o.projectID, "tasks", run.ID, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt projectruntime.RunReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ExecutionStatus != "failed" || receipt.AcceptanceStatus != "unverified" || receipt.RunID != run.ID || receipt.StateHome == "" || receipt.Kind != "execution_receipt" {
		t.Fatal(receipt)
	}
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal("shared receipt disclosed conversation")
	}
}

func TestRuntimeCLIExplicitLegacyGitSelectionEnrollsSharedIdentity(t *testing.T) {
	store, o, pop := runtimeFixture(t)
	p, err := store.RegisterProject(t.Context(), "legacy-pop", "pop", pop)
	if err != nil {
		t.Fatal(err)
	}
	o.projectID = p.ID
	o.projectIDs = []string{p.ID}
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	shared, err := o.projectCatalog.Projects(t.Context())
	if err != nil || len(shared) != 1 || shared[0].ID != p.ID || shared[0].Scope != p.Scope {
		t.Fatal(shared, err)
	}
}

func TestRuntimeCLIWorktreeContinuationPreservesSourceCheckout(t *testing.T) {
	store, o, pop := runtimeFixture(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", pop}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	o.retrievalQuery = "查一下项目 pop 的认证接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "pop-worktree")
	git("worktree", "add", "--detach", worktree, "HEAD")
	worktree, err := state.CanonicalWorkspace(worktree)
	if err != nil {
		t.Fatal(err)
	}
	o.defaultWorkspace = worktree
	o.workspace = worktree
	o.routedQuery = ""
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.workspace != worktree {
		t.Fatal("initial task left worktree", o.workspace)
	}
	rememberRoutingRun(&o, state.Run{ID: "worktree-run", Input: o.retrievalQuery, ProjectID: o.projectID, Workspace: worktree, Projects: o.projects})
	previousID := o.projectID
	o.projectRouter = entryRoutingFixture(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		if len(req.Recent) != 1 || req.Recent[0].RunID != "worktree-run" {
			t.Fatal(req)
		}
		return routing.Decision{Kind: "project", ProjectIDs: []string{previousID}, PrimaryID: previousID, ContinuationRunID: "worktree-run"}, nil
	})
	o.retrievalQuery = "Continue"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal(err)
	}
	if o.workspace != worktree {
		t.Fatal("continuation silently switched checkout", o.workspace)
	}
	shared, err := o.projectCatalog.Projects(t.Context())
	if err != nil || len(shared) != 1 || shared[0].Workspace != pop {
		t.Fatal("execution replaced primary checkout", shared, err)
	}
}

func TestCLIDefaultStateStaysInInvocationDirectory(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	o, _, err := parse("chat", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.home != filepath.Join(cwd, ".agent-host", "cli-state") {
		t.Fatal("CLI default escaped the project directory", o.home)
	}
	explicit := filepath.Join(cwd, "other-state")
	o, _, err = parse("chat", []string{"--home", explicit}, io.Discard)
	if err != nil || o.home != explicit {
		t.Fatal("explicit state selection changed", err)
	}
}

// Long-term use of automatic enrollment accumulates projects past
// routing.MaxProjects. An exact, explicitly named request must keep working:
// the surplus of unrelated enrolled projects is trimmed, never the request's
// own target, and never silently beyond the documented bound.
func TestRuntimeCandidatesCapSurvivesSurplusEnrolledProjects(t *testing.T) {
	store, o, pop := runtimeFixture(t)
	o.retrievalQuery = "查一下项目 pop 的认证接口"
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal("enroll pop", err)
	}
	root := filepath.Dir(filepath.Dir(pop))
	extraRoot := filepath.Join(root, "extra-repositories")
	if err := os.MkdirAll(extraRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < routing.MaxProjects+10; i++ {
		dir := filepath.Join(extraRoot, fmt.Sprintf("repo-%03d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
		dir, err := state.CanonicalWorkspace(dir)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("extra-%03d", i)
		if _, err := o.projectCatalog.Enroll(t.Context(), state.Project{ID: id, Name: id, Workspace: dir, Scope: "project:" + id}, projectruntime.Origin{Entry: "test"}); err != nil {
			t.Fatal("enroll extra project", err)
		}
	}
	known, err := o.projectCatalog.Projects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(known) <= routing.MaxProjects {
		t.Fatalf("fixture did not exceed MaxProjects: got %d", len(known))
	}
	candidates, err := runtimeCandidates(t.Context(), store, &o, "查一下项目 pop 的认证接口")
	if err != nil {
		t.Fatal("candidates for an exact request must not fail once enrollment grows", err)
	}
	if len(candidates) > routing.MaxProjects {
		t.Fatalf("candidates exceed routing.MaxProjects: got %d", len(candidates))
	}
	found := false
	for _, p := range candidates {
		if p.Workspace == pop {
			found = true
		}
	}
	if !found {
		t.Fatal("the explicitly requested project was trimmed instead of surplus enrollment", candidates)
	}
	o.projectID, o.routedQuery, o.projectIDs = "", "", nil
	if err := resolveOptions(t.Context(), store, &o, true); err != nil {
		t.Fatal("exact request failed after enrollment grew past the cap", err)
	}
	if o.workspace != pop {
		t.Fatal("wrong workspace selected", o.workspace)
	}
}
