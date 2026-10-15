package projectruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/state"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnvironment()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func repository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, path, "init", "-q")
	canonical, err := state.CanonicalWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func testCatalog(t *testing.T) (*Catalog, string) {
	t.Helper()
	dir := t.TempDir()
	return &Catalog{Root: filepath.Join(dir, "runtime")}, dir
}

func candidate(t *testing.T, c *Catalog, path string) state.Project {
	t.Helper()
	projects, err := c.Candidates(context.Background(), path, nil)
	if err != nil || len(projects) != 1 {
		t.Fatalf("candidates: %+v %v", projects, err)
	}
	return projects[0]
}

func TestRootForState(t *testing.T) {
	for home, want := range map[string]string{
		"/data/.agent-host/cli-state":               "/data/.agent-host",
		"/data/.agent-host/channels/dingtalk/state": "/data/.agent-host",
		"/data/.agent-host/state":                   "/data/.agent-host",
		"/custom/host":                              "/custom/host",
		"/custom/host/":                             "/custom/host",
	} {
		if got := RootForState(home); got != want {
			t.Errorf("%q: got %q want %q", home, got, want)
		}
	}
}

func TestCandidateDiscoveryAndSymlinkIdentity(t *testing.T) {
	c, dir := testCatalog(t)
	root := filepath.Join(dir, "github")
	repo := repository(t, filepath.Join(root, "team", "pop"))
	if err := os.MkdirAll(filepath.Join(repo, "nested", "arbitrary", "depth"), 0700); err != nil {
		t.Fatal(err)
	}
	c.LookupRoots = []string{root, root, filepath.Join(dir, "missing")}
	projects, err := c.Candidates(context.Background(), filepath.Join(repo, "nested"), nil)
	if err != nil || len(projects) != 1 {
		t.Fatalf("candidates: %+v %v", projects, err)
	}
	p := projects[0]
	if p.Name != "pop" || p.Workspace != repo || !strings.HasPrefix(p.ID, "local-") || p.Scope != "project:"+p.ID {
		t.Fatalf("candidate %+v", p)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	linked := candidate(t, c, link)
	if linked.ID != p.ID || linked.Workspace != p.Workspace {
		t.Fatalf("symlink identity changed: %+v %+v", p, linked)
	}
	if _, err := os.Stat(c.directory()); !os.IsNotExist(err) {
		t.Fatalf("discovery enrolled inventory: %v", err)
	}
}

func TestKnownLegacyProjectReused(t *testing.T) {
	c, dir := testCatalog(t)
	repo := repository(t, filepath.Join(dir, "pop"))
	known := state.Project{ID: "old-pop", Name: "POP service", Workspace: repo, Scope: repo, Aliases: []string{"pop"}}
	projects, err := c.Candidates(context.Background(), repo, []state.Project{known})
	if err != nil || len(projects) != 1 || projects[0].Scope != repo || projects[0].ID != known.ID {
		t.Fatalf("legacy candidate %+v %v", projects, err)
	}
	enrolled, err := c.Enroll(context.Background(), projects[0], Origin{Entry: "cli"})
	if err != nil || enrolled.ID != known.ID || enrolled.Scope != repo {
		t.Fatalf("legacy enrollment %+v %v", enrolled, err)
	}
}

func TestIdempotentEnrollmentAcrossStateRoots(t *testing.T) {
	c, dir := testCatalog(t)
	repo := repository(t, filepath.Join(dir, "pop"))
	p := candidate(t, c, repo)
	for i := 0; i < 2; i++ {
		if _, err := c.Enroll(context.Background(), p, Origin{Entry: "cli", AudienceID: "owner-local", MessageID: "one"}); err != nil {
			t.Fatal(err)
		}
	}
	other := &Catalog{Root: RootForState(filepath.Join(c.Root, "channels", "test", "state"))}
	shared, err := other.Projects(context.Background())
	if err != nil || len(shared) != 1 || shared[0].ID != p.ID {
		t.Fatalf("shared enrollment %+v %v", shared, err)
	}
	if _, err := other.Enroll(context.Background(), shared[0], Origin{Entry: "test", AudienceID: "group:one", MessageID: "two"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.directory(), p.ID, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record projectRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Origins) != 2 {
		t.Fatalf("origins: %+v", record.Origins)
	}
	for _, path := range []string{filepath.Join(c.directory(), "state.db"), filepath.Join(c.directory(), p.ID, "project.json"), filepath.Join(c.directory(), p.ID, "PROJECT.md")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s mode %v", path, info.Mode())
		}
	}
}

func TestIdentityAndScopeCollisions(t *testing.T) {
	c, dir := testCatalog(t)
	p := candidate(t, c, repository(t, filepath.Join(dir, "pop")))
	if _, err := c.Enroll(context.Background(), p, Origin{}); err != nil {
		t.Fatal(err)
	}
	q := candidate(t, &Catalog{Root: filepath.Join(dir, "other-runtime")}, repository(t, filepath.Join(dir, "other")))
	idCollision := q
	idCollision.ID = p.ID
	scopeCollision := q
	scopeCollision.Scope = p.Scope
	identityCollision := p
	identityCollision.ID = "another-id"
	identityCollision.Scope = "project:another-id"
	for _, collision := range []state.Project{idCollision, scopeCollision, identityCollision} {
		if _, err := c.Enroll(context.Background(), collision, Origin{}); err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("expected collision, got %v", err)
		}
	}
	projects, err := c.Projects(context.Background())
	if err != nil || len(projects) != 1 {
		t.Fatalf("collision modified catalog %+v %v", projects, err)
	}
}

func TestWorktreeSharesRepositoryIdentity(t *testing.T) {
	c, dir := testCatalog(t)
	repo := repository(t, filepath.Join(dir, "repo"))
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	worktree := filepath.Join(dir, "worktree")
	git(t, repo, "worktree", "add", "--detach", worktree)
	p := candidate(t, c, repo)
	q := candidate(t, c, worktree)
	if p.ID != q.ID {
		t.Fatalf("different identities %+v %+v", p, q)
	}
	if _, err := c.Enroll(context.Background(), p, Origin{Entry: "cli"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enroll(context.Background(), q, Origin{Entry: "test"}); err != nil {
		t.Fatal(err)
	}
	projects, err := c.Projects(context.Background())
	if err != nil || len(projects) != 1 || projects[0].Workspace != repo {
		t.Fatalf("checkout changed %+v %v", projects, err)
	}
	c.LookupRoots = []string{repo}
	selected, err := c.Candidates(context.Background(), worktree, projects)
	canonicalWorktree, canonicalErr := state.CanonicalWorkspace(worktree)
	if err != nil || canonicalErr != nil || len(selected) != 1 || selected[0].Workspace != canonicalWorktree || selected[0].ID != p.ID || selected[0].Scope != p.Scope {
		t.Fatalf("current worktree selection moved to registry checkout: %+v %v %v", selected, err, canonicalErr)
	}
}

func TestDiscoveryLimitsAndCancellation(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		c, dir := testCatalog(t)
		root := filepath.Join(dir, "github")
		repository(t, filepath.Join(root, "a", "b", "c", "deep"))
		c.LookupRoots = []string{root}
		if _, err := c.Candidates(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "depth") {
			t.Fatalf("expected depth error: %v", err)
		}
	})
	t.Run("directories", func(t *testing.T) {
		c, dir := testCatalog(t)
		root := filepath.Join(dir, "github")
		for i := 0; i < MaxDiscoveryDirectories; i++ {
			if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("d-%04d", i)), 0700); err != nil {
				t.Fatal(err)
			}
		}
		c.LookupRoots = []string{root}
		if _, err := c.Candidates(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "directories") {
			t.Fatalf("expected directory error: %v", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		c, dir := testCatalog(t)
		repo := repository(t, filepath.Join(dir, "pop"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.Candidates(ctx, repo, nil); err == nil {
			t.Fatal("expected cancellation")
		}
	})
}

func TestOverlappingLookupDepth(t *testing.T) {
	c, dir := testCatalog(t)
	root := filepath.Join(dir, "github")
	repository(t, filepath.Join(root, "namespace", "pop"))
	c.LookupRoots = []string{root}
	projects, err := c.Candidates(context.Background(), root, nil)
	if err != nil || len(projects) != 1 {
		t.Fatalf("overlapping discovery %+v %v", projects, err)
	}
}

func TestDiscoverySkipsDependencyAndHiddenTrees(t *testing.T) {
	c, dir := testCatalog(t)
	root := filepath.Join(dir, "github")
	repo := repository(t, filepath.Join(root, "pop"))
	for _, name := range []string{"node_modules", "vendor", "target", "build", "dist", ".cache", "__pycache__"} {
		if err := os.MkdirAll(filepath.Join(root, name, "a", "b", "c", "d", "e"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	c.LookupRoots = []string{root}
	projects, err := c.Candidates(context.Background(), "", nil)
	if err != nil || len(projects) != 1 || projects[0].Workspace != repo {
		t.Fatalf("ignored trees: %+v %v", projects, err)
	}
}

func TestNonGitSourceExportDoesNotBlockCheckoutDiscovery(t *testing.T) {
	c, dir := testCatalog(t)
	root := filepath.Join(dir, "github")
	repo := repository(t, filepath.Join(root, "pop"))
	if err := os.MkdirAll(filepath.Join(root, "public-source-export", "internal", "channel", "adapter", "private", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	c.LookupRoots = []string{root}
	projects, err := c.Candidates(context.Background(), "", nil)
	if err != nil || len(projects) != 1 || projects[0].Workspace != repo {
		t.Fatalf("nonGit source export blocked project: %+v %v", projects, err)
	}
}

func TestRuntimeRejectsSymlinkFilesAndPreservesCheckout(t *testing.T) {
	c, dir := testCatalog(t)
	repo := repository(t, filepath.Join(dir, "pop"))
	dirt := filepath.Join(repo, "uncommitted.txt")
	if err := os.WriteFile(dirt, []byte("user work"), 0600); err != nil {
		t.Fatal(err)
	}
	p := candidate(t, c, repo)
	if _, err := c.Enroll(context.Background(), p, Origin{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dirt)
	if err != nil || string(data) != "user work" {
		t.Fatalf("external checkout changed: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "PROJECT.md")); !os.IsNotExist(err) {
		t.Fatalf("record written into checkout: %v", err)
	}
	other := &Catalog{Root: filepath.Join(dir, "other")}
	if err := os.MkdirAll(other.directory(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dirt, filepath.Join(other.directory(), "state.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Enroll(context.Background(), p, Origin{}); err == nil {
		t.Fatal("symlink state file accepted")
	}
	data, err = os.ReadFile(dirt)
	if err != nil || string(data) != "user work" {
		t.Fatalf("symlink source modified: %s %v", data, err)
	}
}

func TestRunReceiptsPreserveOriginWithoutPrivateText(t *testing.T) {
	c, dir := testCatalog(t)
	p := candidate(t, c, repository(t, filepath.Join(dir, "pop")))
	if _, err := c.Enroll(context.Background(), p, Origin{Entry: "test", AudienceID: "group:one", MessageID: "one"}); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(c.Root, "channels", "test", "state")
	run := state.Run{ID: "run-one", ProjectID: p.ID, Workspace: p.Workspace, SessionID: "session-one", Input: "private original question", Output: "private response", ExecutionStatus: "running", AcceptanceStatus: "pending", MemoryStatus: "pending", CreatedAt: time.Now().UTC(), Source: &state.RunSource{Channel: "test", AgentID: "agent-one", AudienceID: "group:one", MessageID: "one"}}
	if err := c.RecordRun(context.Background(), home, run); err != nil {
		t.Fatal(err)
	}
	run.ExecutionStatus = "completed"
	run.MemoryStatus = "saved"
	if err := c.RecordRun(context.Background(), home, run); err != nil {
		t.Fatal(err)
	}
	dirView := filepath.Join(c.directory(), p.ID, "tasks", run.ID)
	data, err := os.ReadFile(filepath.Join(dirView, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt RunReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.StateHome != home || receipt.RunID != run.ID || receipt.MessageID != "one" || receipt.AudienceID != "group:one" || receipt.AgentID != "agent-one" || receipt.ExecutionStatus != "completed" || receipt.AcceptanceStatus != "pending" {
		t.Fatalf("receipt %+v", receipt)
	}
	for _, path := range []string{filepath.Join(dirView, "task.json"), filepath.Join(dirView, "TASK.md"), filepath.Join(c.directory(), p.ID, "PROJECT.md")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), run.Input) || strings.Contains(string(data), run.Output) {
			t.Fatalf("shared view leaked private conversation: %s", path)
		}
	}
	if err := c.RecordRun(context.Background(), filepath.Join(c.Root, "cli-state"), run); err == nil {
		t.Fatal("conflicting source overwrote shared receipt")
	}
}

func TestRunRequiresEnrolledSafeIdentity(t *testing.T) {
	c, _ := testCatalog(t)
	for _, run := range []state.Run{{ID: "../outside", ProjectID: "pop"}, {ID: "run", ProjectID: "../outside"}, {ID: "run", ProjectID: "missing"}} {
		if err := c.RecordRun(context.Background(), "/state", run); err == nil {
			t.Fatalf("unsafe or unenrolled run %+v succeeded", run)
		}
	}
}

func TestConcurrentEnrollment(t *testing.T) {
	c, dir := testCatalog(t)
	p := candidate(t, c, repository(t, filepath.Join(dir, "pop")))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := (&Catalog{Root: c.Root}).Enroll(context.Background(), p, Origin{Entry: fmt.Sprintf("entry-%d", i)})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(c.directory(), p.ID, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record projectRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Origins) != 4 {
		t.Fatalf("lost concurrent origin %+v", record.Origins)
	}
}
