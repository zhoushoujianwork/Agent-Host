package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/harness"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

type projectFixture struct {
	*serviceFixture
	projects map[string]state.Project
	policy   string
}

func newProjectFixture(t *testing.T) *projectFixture {
	t.Helper()
	f := &projectFixture{serviceFixture: newServiceFixture(t), projects: map[string]state.Project{}, policy: "fixture-policy-v1"}
	for _, id := range []string{"alpha", "beta"} {
		p, err := f.service.Store.RegisterProject(context.Background(), id, "Project "+id, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		f.projects[id] = p
	}
	// These assembly tests use deterministic project recognition; semantic
	// classification is exercised by the routing and daily-conversation suites.
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		for _, id := range []string{"alpha", "beta"} {
			if strings.Contains(strings.ToLower(req.Text), "project "+id) {
				return routing.Decision{Kind: "project", ProjectIDs: []string{id}, PrimaryID: id, Reason: "Synthetic named project task."}, nil
			}
		}
		return routing.Decision{Kind: "default", Reason: "Synthetic independent general request."}, nil
	})
	f.service.ResolveHost = func(ctx context.Context, id string) (Host, assembly.Runtime, error) {
		p := state.Project{Name: "Default workspace", Workspace: f.host.Config.Scope, Scope: f.host.Config.Scope}
		var err error
		if id != "" {
			p, err = f.service.Store.Project(ctx, id)
			if err != nil {
				return nil, assembly.Runtime{}, err
			}
		}
		workspace, err := state.CanonicalWorkspace(p.Workspace)
		if err != nil {
			return nil, assembly.Runtime{}, err
		}
		runtime := assembly.Runtime{ProjectID: p.ID, ProjectName: p.Name, Workspace: workspace, Scope: p.Scope, Fingerprint: f.policy}
		config := f.host.Config
		config.ProjectID, config.ProjectName, config.Scope = runtime.ProjectID, runtime.ProjectName, runtime.Scope
		config.Workspace, config.RuntimeFingerprint = runtime.Workspace, runtime.Fingerprint
		h := &host.Host{Store: f.service.Store, Config: config, Runner: f.runner, Decider: f.host.Decider, Objects: f.host.Objects}
		return h, runtime, nil
	}
	return f
}

func TestRetiredProjectCommandsLeaveLegacySelectionAndMemoryUntouched(t *testing.T) {
	f := newProjectFixture(t)
	initial := f.process(t, f.message(t, "status", "/status", "text", "owner-dm"))
	if err := f.service.Store.SetChannelSelection(context.Background(), initial.Binding, state.ChannelSelection{Mode: "manual", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/projects", "/project", "/project beta", "/project missing", "/project default", "/routing auto", "/choose missing beta", "/cancel missing"} {
		m := f.process(t, f.message(t, text, text, "text", "owner-dm"))
		if m.Status != "completed" || m.RunID != "" || m.SessionID != "" || m.ReplyStatus != "accepted" {
			t.Fatalf("retired command created native work or failed: %+v", m)
		}
		selection, err := f.service.Store.ChannelSelection(context.Background(), m.Binding)
		if err != nil || selection.Mode != "manual" || selection.PrimaryID != "alpha" {
			t.Fatalf("retired command rewrote historical selection: %+v %v", selection, err)
		}
		if strings.Contains(m.ReplyText, f.projects["alpha"].Workspace) || strings.Contains(m.ReplyText, f.projects["beta"].Workspace) {
			t.Fatal("retired command leaked local paths", m.ReplyText)
		}
	}
	drainReactions(t, f.service)
	if len(f.runner.snapshot()) != 0 || len(f.service.Reactor.(*serviceReactor).snapshot()) != 0 {
		t.Fatal("retired commands created native work or agent reactions")
	}
	for _, p := range f.projects {
		memories, err := f.service.Store.Memories(context.Background(), p.Scope)
		if err != nil || len(memories) != 0 {
			t.Fatal("command entered memory", memories, err)
		}
	}
	// Legacy selection remains readable as history, but cannot pin new tasks.
	general := f.process(t, f.message(t, "general", "Draft a general checklist.", "text", "owner-dm"))
	beta := f.process(t, f.message(t, "beta-task", "Inspect project beta.", "text", "owner-dm"))
	if general.Status != "completed" || general.ProjectID != "" || beta.Status != "completed" || beta.ProjectID != "beta" || len(f.runner.snapshot()) != 2 {
		t.Fatal("historical pin still controls new messages", general, beta)
	}
}

func TestProjectIsolationResumeAndResetRetainScopedMemory(t *testing.T) {
	f := newProjectFixture(t)
	const alphaStatement = "I prefer project alpha answers with a short summary."
	const betaStatement = "I prefer project beta answers with detailed examples."
	f.host.Decider = serviceDecider(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		return memory.Decision{Store: strings.HasPrefix(input, "I prefer"), Kind: "preference", Model: "fixture-decision"}, nil
	})
	a := f.process(t, f.message(t, "a", alphaStatement, "text", "dm"))
	b := f.process(t, f.message(t, "b", betaStatement, "text", "dm"))
	aResume := f.process(t, f.message(t, "a-resume", "Explain project alpha.", "text", "dm"))
	reset := f.process(t, f.message(t, "reset-a", "/new", "text", "dm"))
	aFresh := f.process(t, f.message(t, "a-fresh", "Explain project alpha again.", "text", "dm"))
	bResume := f.process(t, f.message(t, "b-resume", "Explain project beta.", "text", "dm"))
	requests := f.runner.snapshot()
	if len(requests) != 5 || a.SessionID == b.SessionID || aResume.SessionID != a.SessionID || aFresh.SessionID == a.SessionID || bResume.SessionID != b.SessionID || !requests[2].Resume || requests[3].Resume || !requests[4].Resume {
		t.Fatalf("project sessions conflated: a=%s b=%s resumedA=%s freshA=%s resumedB=%s requests=%+v", a.SessionID, b.SessionID, aResume.SessionID, aFresh.SessionID, bResume.SessionID, requests)
	}
	if reset.RunID != "" || reset.RouteBinding == "" || reset.ProjectID != "alpha" {
		t.Fatal("reset lacked current-project route or ran native work", reset)
	}
	for i, project := range []string{"alpha", "beta", "alpha", "alpha", "beta"} {
		if requests[i].Workspace != f.projects[project].Workspace {
			t.Fatalf("task %d entered wrong cwd: %+v", i, requests[i])
		}
	}
	freshRun := f.run(t, aFresh)
	if freshRun.ProjectID != "alpha" || freshRun.Workspace != f.projects["alpha"].Workspace || freshRun.Scope != f.projects["alpha"].Scope || len(freshRun.Context) != 1 || freshRun.Context[0].Text != alphaStatement || strings.Contains(requests[3].Prompt, betaStatement) {
		t.Fatalf("fresh alpha context lost or leaked memory: %+v", freshRun)
	}
	for _, m := range []state.ChannelMessage{a, b, aResume, aFresh, bResume} {
		if m.Binding != a.Binding || m.Scope != f.service.Scope || m.RouteBinding == "" || m.ResolvedScope != f.projects[m.ProjectID].Scope || m.RuntimeFingerprint != f.policy || strings.Contains(m.ReplyText, m.Workspace) || strings.Contains(m.ReplyText, m.SessionID) {
			t.Fatalf("queue identity changed or execution route absent: %+v", m)
		}
	}
}

func TestQueuedStatusThenProjectTaskResolvesAfterClaimAndDeduplicates(t *testing.T) {
	f := newProjectFixture(t)
	switchInput := f.message(t, "status", "/status", "text", "dm")
	taskInput := f.message(t, "task", "Explain project alpha.", "text", "dm")
	f.receive(t, switchInput)
	f.receive(t, taskInput)
	f.receive(t, switchInput)
	queued := f.receipt(t, taskInput.ID)
	if queued.ProjectID != "" || queued.RouteBinding != "" {
		t.Fatal("receive resolved stale project before queued control was processed", queued)
	}
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		pinned := f.receipt(t, taskInput.ID)
		if pinned.ProjectID != "alpha" || pinned.RouteBinding == "" || pinned.RunID == "" || pinned.Workspace != req.Workspace || pinned.RuntimeFingerprint != f.policy {
			t.Errorf("native execution started before project route was durable: %+v", pinned)
		}
		return harness.Result{Status: "completed", Text: "Alpha answer.", Model: req.Model, NativeSessionID: req.NativeSessionID}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.replier.reply = func(context.Context, replyCall) (string, error) {
		if len(f.replier.snapshot()) == 2 {
			cancel()
		}
		return "accepted", nil
	}
	if err := f.service.Work(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("worker shutdown", err)
	}
	task := f.receipt(t, taskInput.ID)
	if task.ProjectID != "alpha" || task.Status != "completed" || task.ReplyStatus != "accepted" || len(f.runner.snapshot()) != 1 || f.runner.snapshot()[0].Workspace != f.projects["alpha"].Workspace {
		t.Fatal("queued task entered stale project", task)
	}
	f.receive(t, switchInput)
	f.receive(t, taskInput)
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate command or task replayed", err)
	}
}

func TestProjectRoutesAndSessionsPersistAcrossRestart(t *testing.T) {
	f := newProjectFixture(t)
	first := f.process(t, f.message(t, "first", "Explain project alpha.", "text", "dm"))
	f.receive(t, f.message(t, "queued", "Continue project alpha.", "text", "dm"))
	if err := f.service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	f.service.Store, f.host.Store = store, store
	if err := store.RecoverChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.service.process(context.Background(), f.next(t)); err != nil {
		t.Fatal(err)
	}
	second := f.receipt(t, "queued")
	if second.ProjectID != "alpha" || second.SessionID != first.SessionID || !f.runner.snapshot()[1].Resume {
		t.Fatal("restart lost project/session selection", second)
	}
	other := f.process(t, f.message(t, "other-conversation", "General question.", "text", "another-dm"))
	if other.ProjectID != "" || other.Workspace == second.Workspace || other.SessionID == second.SessionID {
		t.Fatal("project selection crossed conversation boundary", other)
	}
}

func TestRuntimeChangesStartNewSessionAndMissingDirectoryDoesNotFallback(t *testing.T) {
	f := newProjectFixture(t)
	first := f.process(t, f.message(t, "first", "Explain project alpha.", "text", "dm"))
	f.policy = "fixture-policy-v2"
	changed := f.process(t, f.message(t, "changed-policy", "Continue project alpha.", "text", "dm"))
	if first.SessionID == changed.SessionID || f.runner.snapshot()[1].Resume || !strings.Contains(changed.ReplyText, "处理方式已更新") {
		t.Fatal("runtime change resumed incompatible session or was hidden", changed)
	}
	p, err := f.service.Store.UpdateProject(context.Background(), "alpha", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	moved := f.process(t, f.message(t, "moved", "Continue project alpha in the moved directory.", "text", "dm"))
	if moved.SessionID == changed.SessionID || moved.Workspace != p.Workspace || f.runner.snapshot()[2].Resume || !strings.Contains(moved.ReplyText, "处理方式已更新") {
		t.Fatal("directory change resumed old session", moved)
	}
	if err := os.RemoveAll(p.Workspace); err != nil {
		t.Fatal(err)
	}
	missing := f.process(t, f.message(t, "missing", "Inspect project alpha without falling back.", "text", "dm"))
	if missing.Status != "failed" || missing.RunID != "" || len(f.runner.snapshot()) != 3 || strings.Contains(missing.ReplyText, "no such file") {
		t.Fatal("missing project silently fell back or leaked diagnostics", missing)
	}
	var decision routing.Decision
	if err := json.Unmarshal(missing.RoutingDecision, &decision); err != nil || decision.Kind != "project" || decision.PrimaryID != "alpha" {
		t.Fatal("missing directory lost the attempted project route", decision, err)
	}
}

func TestProfileRotationPreservesVerifiedConversationSelection(t *testing.T) {
	f := newProjectFixture(t)
	first := f.process(t, f.message(t, "first", "Explain project alpha.", "text", "dm"))
	f.service.Profile = "different-shared-configuration"
	f.policy = "different-runtime-policy"
	second := f.process(t, f.message(t, "second", "Continue project alpha.", "text", "dm"))
	expected := Key("project-conversation-v1", "dingtalk", f.service.ClientID, first.TenantID, first.ConversationID, first.SenderID)
	if first.Binding != expected || second.Binding != expected || first.Profile == second.Profile || second.ProjectID != "alpha" || second.SessionID == first.SessionID || f.runner.snapshot()[1].Resume || !strings.Contains(second.ReplyText, "处理方式已更新") {
		t.Fatalf("profile change lost project selection or reused native session: first=%+v second=%+v", first, second)
	}
	if first.Binding != second.Binding {
		t.Fatal("profile rotation changed verified conversation identity")
	}
}

func TestRuntimeRollbackCreatesFreshSessionAndThenResumesIt(t *testing.T) {
	for _, change := range []string{"configuration", "directory"} {
		t.Run(change, func(t *testing.T) {
			f := newProjectFixture(t)
			first := f.process(t, f.message(t, "first", "Inspect project alpha before the change.", "text", "dm"))
			if change == "configuration" {
				f.policy = "changed-policy"
			} else if _, err := f.service.Store.UpdateProject(context.Background(), "alpha", "", t.TempDir()); err != nil {
				t.Fatal(err)
			}
			second := f.process(t, f.message(t, "second", "Inspect project alpha after the change.", "text", "dm"))
			if change == "configuration" {
				f.policy = first.RuntimeFingerprint
			} else if _, err := f.service.Store.UpdateProject(context.Background(), "alpha", "", first.Workspace); err != nil {
				t.Fatal(err)
			}
			rollback := f.process(t, f.message(t, "rollback", "Inspect project alpha after returning to the original runtime.", "text", "dm"))
			continued := f.process(t, f.message(t, "continued", "Continue the latest task in project alpha.", "text", "dm"))
			requests := f.runner.snapshot()
			if len(requests) != 4 || rollback.RouteBinding != first.RouteBinding || rollback.SessionID == first.SessionID || rollback.SessionID == second.SessionID || requests[2].Resume || requests[2].NativeSessionID == requests[0].NativeSessionID || !strings.Contains(rollback.ReplyText, "已开始新的对话") {
				t.Fatalf("rollback silently revived the old conversation: first=%+v second=%+v rollback=%+v requests=%+v", first, second, rollback, requests)
			}
			if continued.SessionID != rollback.SessionID || !requests[3].Resume || requests[3].NativeSessionID != requests[2].NativeSessionID || strings.Contains(continued.ReplyText, "已开始新的对话") {
				t.Fatal("ordinary follow-up did not resume the new conversation", continued)
			}
		})
	}
}

func TestLegacyBindingStartsFreshAndFailedRouteRecoversAutomatically(t *testing.T) {
	f := newProjectFixture(t)
	input := f.message(t, "legacy", "Legacy default turn.", "text", "dm")
	f.receive(t, input)
	m := f.next(t)
	legacy, err := f.host.Session(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	legacyBinding := Key(f.service.Profile, m.TenantID, m.ConversationID, m.SenderID)
	if err := f.service.Store.BindChannelSession(context.Background(), legacyBinding, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	first := f.receipt(t, input.ID)
	if first.SessionID == legacy.ID || f.runner.snapshot()[0].Resume || !strings.Contains(first.ReplyText, "已开始新的对话") {
		t.Fatal("legacy binding resumed without compatibility evidence", first)
	}
	f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{}, errors.New("private fixture memory diagnostic")
	})
	failed := f.process(t, f.message(t, "failed", "Explain project alpha.", "text", "dm"))
	// The answer left before the failing closeout: message and reply stay successful.
	if failed.Status != "completed" || f.run(t, failed).MemoryStatus != "failed" || len(f.runner.snapshot()) != 2 || strings.Contains(failed.ReplyText, "private fixture") {
		t.Fatal("failed route lost its outcome or leaked diagnostics", failed)
	}
	f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "none"}, nil
	})
	fresh := f.process(t, f.message(t, "fresh", "A fresh task in project alpha.", "text", "dm"))
	if fresh.Status != "completed" || fresh.SessionID == failed.SessionID || f.runner.snapshot()[2].Resume || !strings.Contains(fresh.ReplyText, "本条消息") {
		t.Fatal("automatic current-project recovery did not unblock", fresh)
	}
}
