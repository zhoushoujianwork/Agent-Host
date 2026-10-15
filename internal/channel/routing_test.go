package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-host/internal/assembly"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/decision/jev"
	"agent-host/internal/harness"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

type serviceRouter func(context.Context, routing.Request) (routing.Decision, error)

func (f serviceRouter) Decide(ctx context.Context, request routing.Request) (routing.Decision, error) {
	return f(ctx, request)
}

func newRoutingFixture(t *testing.T) *projectFixture {
	t.Helper()
	f := newProjectFixture(t)
	workspace, err := state.CanonicalWorkspace(f.host.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	f.host.Config.Scope = workspace
	profile := t.TempDir()
	f.service.ResolveProjects = func(ctx context.Context, ids []string, primary string) (Host, assembly.Runtime, error) {
		var projects []state.ProjectRef
		for _, id := range ids {
			p, err := f.service.Store.Project(ctx, id)
			if err != nil {
				return nil, assembly.Runtime{}, err
			}
			projects = append(projects, state.ProjectRef{ID: p.ID, Name: p.Name, Workspace: p.Workspace, Scope: p.Scope})
		}
		runtime, err := assembly.AssembleProjects(projects, primary, workspace, workspace, assembly.Configuration{Model: "fixture-model", ToolsDisabled: true, NativeProfile: profile, KnowledgeFingerprint: f.policy})
		if err != nil {
			return nil, runtime, err
		}
		config := f.host.Config
		config.Scope, config.Workspace, config.ProjectID, config.ProjectName = runtime.Scope, runtime.Workspace, runtime.ProjectID, runtime.ProjectName
		config.Projects, config.AdditionalDirectories, config.RuntimeFingerprint = runtime.Projects, runtime.AdditionalDirectories, runtime.Fingerprint
		return &host.Host{Store: f.service.Store, Config: config, Runner: f.runner, Decider: f.host.Decider, Objects: f.host.Objects}, runtime, nil
	}
	f.service.Router = serviceRouter(func(ctx context.Context, request routing.Request) (routing.Decision, error) {
		if strings.HasPrefix(request.Text, "ambiguous") || strings.Contains(request.Text, "gamma") || strings.Contains(request.Text, "凤凰") {
			return routing.Decision{Kind: "clarify", Reason: "请选择本次项目。", CandidateIDs: []string{"alpha", "beta"}}, nil
		}
		if request.Text == "general" {
			return routing.Decision{Kind: "default", Reason: "General request."}, nil
		}
		return routing.New(nil).Decide(ctx, request)
	})
	return f
}

func choiceFor(m state.ChannelMessage) string { return Key("project-choice-v1", m.ID)[:16] }

func TestMultiProjectAutomaticQueueSessionsAndMemoryPolicy(t *testing.T) {
	f := newRoutingFixture(t)
	a := f.process(t, f.message(t, "alpha", "Fix project alpha.", "text", "dm"))
	if a.Status != "completed" || a.RunID == "" {
		t.Fatal("explicit alpha route failed", a)
	}
	switchInput := f.message(t, "joint-status", "/status", "text", "dm")
	jointInput := f.message(t, "joint", "Compare alpha and beta", "text", "dm")
	f.receive(t, switchInput)
	f.receive(t, jointInput)
	for range 2 {
		if err := f.service.process(context.Background(), f.next(t)); err != nil {
			t.Fatal(err)
		}
	}
	joint := f.receipt(t, jointInput.ID)
	run := f.run(t, joint)
	if joint.ProjectID != "" || len(joint.Projects) != 2 || joint.Workspace != f.host.Config.Scope || len(joint.AdditionalDirectories) != 2 || run.MemoryStatus != "skipped" || run.ExperienceStatus != "skipped" {
		t.Fatalf("joint task did not use a neutral cwd and skipped memory: %+v run=%+v", joint, run)
	}
	if strings.Contains(joint.ReplyText, joint.Workspace) || strings.Contains(joint.ReplyText, joint.SessionID) || !reflect.DeepEqual(f.runner.snapshot()[1].AdditionalDirectories, joint.AdditionalDirectories) {
		t.Fatal("joint scope was not exposed to native/reply", joint)
	}
	aResume := f.process(t, f.message(t, "a-resume", "Inspect alpha", "text", "dm"))
	jointResume := f.process(t, f.message(t, "joint-resume", "Compare beta and alpha", "text", "dm"))
	f.process(t, f.message(t, "new-ab", "/new", "text", "dm"))
	jointFresh := f.process(t, f.message(t, "joint-fresh", "Compare alpha and beta", "text", "dm"))
	aAgain := f.process(t, f.message(t, "a-again", "Inspect alpha", "text", "dm"))
	requests := f.runner.snapshot()
	if a.SessionID != aResume.SessionID || aAgain.SessionID != a.SessionID || jointResume.SessionID != joint.SessionID || jointFresh.SessionID == joint.SessionID || !requests[2].Resume || !requests[3].Resume || requests[4].Resume || !requests[5].Resume {
		t.Fatalf("single/joint session reuse and targeted reset failed: %+v", requests)
	}
	f.service.Router = serviceRouter(func(_ context.Context, r routing.Request) (routing.Decision, error) {
		if r.Text != "Compare both projects, with beta as the primary project." {
			t.Fatal("unexpected primary-project request", r.Text)
		}
		return routing.Decision{Kind: "project", ProjectIDs: []string{"alpha", "beta"}, PrimaryID: "beta"}, nil
	})
	withPrimary := f.process(t, f.message(t, "primary-task", "Compare both projects, with beta as the primary project.", "text", "dm"))
	if withPrimary.ProjectID != "beta" || withPrimary.Workspace != f.projects["beta"].Workspace || len(withPrimary.AdditionalDirectories) != 1 || withPrimary.SessionID == joint.SessionID {
		t.Fatal("explicit primary was not applied", withPrimary)
	}
}

func TestLegacyRouteAuditAndDelayedChoiceExecutionOrder(t *testing.T) {
	f := newRoutingFixture(t)
	original := legacyPending(t, f, f.message(t, "waiting", "ambiguous check without guessing", "text", "dm"))
	qid := choiceFor(original)
	if original.Status != "awaiting" || original.RunID != "" || original.ReplyStatus != "accepted" || len(original.RoutingDecision) == 0 {
		t.Fatal("clarification was executed or lost its durable reason", original)
	}
	b := f.process(t, f.message(t, "b-first", "Fix project beta.", "text", "dm"))
	var audit routing.Decision
	if err := json.Unmarshal(b.RoutingDecision, &audit); err != nil || audit.Kind != "project" || len(audit.Evidence) == 0 {
		t.Fatal("successful auto route lost its evidence", string(b.RoutingDecision), err)
	}
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
	choice, err := store.ChannelChoice(context.Background(), qid)
	if err != nil || choice.Status != "pending" || f.receipt(t, "waiting").Status != "awaiting" {
		t.Fatal("restart discarded a waiting request", choice, err)
	}
	confirmation := f.process(t, f.message(t, "confirm", "确认 "+qid+" alpha beta -- 只读检查，不要修改", "text", "dm"))
	original = f.receipt(t, "waiting")
	run := f.run(t, original)
	choice, err = store.ChannelChoice(context.Background(), qid)
	if err != nil || choice.Status != "resolved" || original.Status != "completed" || confirmation.RunID != "" || run.Input != "ambiguous check without guessing" || len(run.Supplements) != 1 || run.Supplements[0].MessageID != "confirm" || run.Supplements[0].Text != "只读检查，不要修改" || run.Source == nil || run.Source.MessageID != "waiting" {
		t.Fatalf("confirmation replaced or replayed original source: choice=%+v original=%+v confirmation=%+v run=%+v err=%v", choice, original, confirmation, run, err)
	}
	if !strings.Contains(f.runner.snapshot()[1].Prompt, "只读检查，不要修改") || !strings.Contains(f.replier.snapshot()[2].url, "cap-confirm") || original.ReplyText != f.replier.snapshot()[0].text {
		t.Fatal("supplement/fresh reply capability/original question audit lost")
	}
	recent, err := store.RecentChannelRoutes(context.Background(), original.Binding, 5)
	if err != nil || len(recent) != 2 || recent[0].ID != original.ID || recent[1].ID != b.ID {
		t.Fatal("delayed confirmation was ordered by receipt rather than execution", recent, err)
	}
	continued := f.process(t, f.message(t, "continue", "继续", "text", "dm"))
	if len(continued.Projects) != 2 || continued.SessionID != original.SessionID || !f.runner.snapshot()[2].Resume {
		t.Fatal("continuation did not use latest actual delayed execution", continued)
	}
	f.receive(t, f.message(t, "confirm", "确认 "+qid+" alpha beta -- 只读检查，不要修改", "text", "dm"))
	if _, err := store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate callback replayed confirmation", err)
	}
	again := f.process(t, f.message(t, "confirm-again", "确认 "+qid+" alpha", "text", "dm"))
	if again.Status != "completed" || !strings.Contains(again.ReplyText, "已处理") || len(f.runner.snapshot()) != 3 {
		t.Fatal("duplicate confirmation replayed native work", again)
	}
}

func TestAutomaticTargetChangesAndInvalidChoiceNeverFallback(t *testing.T) {
	f := newRoutingFixture(t)
	f.process(t, f.message(t, "first", "Fix project alpha", "text", "dm"))
	changed := f.process(t, f.message(t, "conflict", "Fix project beta", "text", "dm"))
	unknown := f.process(t, f.message(t, "unknown", "Fix project gamma.", "text", "dm"))
	chineseBefore := f.process(t, f.message(t, "chinese-before", "修复凤凰项目的登录问题", "text", "dm"))
	chineseAfter := f.process(t, f.message(t, "chinese-after", "项目 凤凰 的测试", "text", "dm"))
	if changed.Status != "completed" || changed.ProjectID != "beta" || unknown.Status != "completed" || unknown.ProjectID != "" || chineseBefore.Status != "completed" || chineseAfter.Status != "completed" || len(f.runner.snapshot()) != 5 {
		t.Fatal("automatic target change or unknown project routing failed", changed, unknown)
	}
	legacy := legacyPending(t, f, f.message(t, "legacy", "A historical request", "text", "dm"))
	qid := choiceFor(legacy)
	wrongConversation := f.process(t, f.message(t, "wrong-owner", "确认 "+qid+" beta", "text", "other-dm"))
	missing := f.process(t, f.message(t, "unknown-choice-project", "确认 "+qid+" missing", "text", "dm"))
	if err := os.RemoveAll(f.projects["beta"].Workspace); err != nil {
		t.Fatal(err)
	}
	lostDirectory := f.process(t, f.message(t, "lost-choice-directory", "确认 "+qid+" beta", "text", "dm"))
	choice, err := f.service.Store.ChannelChoice(context.Background(), qid)
	if err != nil || choice.Status != "pending" || wrongConversation.Status != "failed" || missing.Status != "failed" || lostDirectory.Status != "failed" || len(f.runner.snapshot()) != 5 {
		t.Fatal("invalid confirmation consumed waiting request or fell back", choice, err)
	}
	canceled := f.process(t, f.message(t, "cancel", "取消 "+qid, "text", "dm"))
	stale := f.process(t, f.message(t, "stale", "确认 "+qid+" alpha", "text", "dm"))
	if canceled.Status != "completed" || stale.Status != "completed" || f.receipt(t, "legacy").Status != "failed" || len(f.runner.snapshot()) != 5 {
		t.Fatal("canceled request replayed", canceled, stale)
	}
	if _, err := f.service.Store.ChannelSelection(context.Background(), changed.Binding); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("task confirmation created a persistent project selection", err)
	}
}

func TestChoiceReplyAndReactionRemainIndependentAcrossPolicyRestart(t *testing.T) {
	f := newRoutingFixture(t)
	original := legacyPending(t, f, f.message(t, "waiting", "ambiguous original", "text", "dm"))
	f.service.Profile = "changed-profile"
	f.policy = "fixture-policy-v2"
	f.replier.reply = func(context.Context, replyCall) (string, error) {
		return "failed", errors.New("platform rejected result")
	}
	confirmed := f.process(t, f.message(t, "confirm", "确认 "+choiceFor(original)+" alpha beta", "text", "dm"))
	original = f.receipt(t, "waiting")
	run := f.run(t, original)
	if original.Profile == confirmed.Profile || original.Status != "completed" || original.ReplyStatus != "accepted" || confirmed.ReplyStatus != "failed" || run.ExecutionStatus != "completed" || run.MemoryStatus != "skipped" {
		t.Fatal("delivery failure changed execution or historical queue policy", original, confirmed, run)
	}
	f.service.Reactor.(*serviceReactor).send = func(context.Context, reactionCall) error { return errors.New("reaction failed independently") }
	drainReactions(t, f.service)
	original = f.receipt(t, "waiting")
	found := false
	for _, reaction := range original.Reactions {
		if reaction.Stage == "failed" {
			found = reaction.Status == "unknown"
		}
	}
	if !found || original.Status != "completed" || f.receipt(t, "confirm").ReplyStatus != "failed" {
		t.Fatal("result reaction did not follow final confirmation delivery and current policy", original)
	}
}

func TestMultiProjectRuntimeRotationAndNamesDoNotConfuseReuse(t *testing.T) {
	f := newRoutingFixture(t)
	first := f.process(t, f.message(t, "first", "Compare alpha and beta", "text", "dm"))
	if _, err := f.service.Store.UpdateProjectMetadata(context.Background(), "alpha", "Renamed alpha", "New description", []string{"alpha alias"}); err != nil {
		t.Fatal(err)
	}
	renamed := f.process(t, f.message(t, "renamed", "continue", "text", "dm"))
	f.policy = "fixture-policy-v2"
	changed := f.process(t, f.message(t, "changed", "continue", "text", "dm"))
	f.policy = "fixture-policy-v1"
	rolledBack := f.process(t, f.message(t, "rollback", "continue", "text", "dm"))
	if renamed.SessionID != first.SessionID || changed.SessionID == first.SessionID || rolledBack.SessionID == first.SessionID || rolledBack.SessionID == changed.SessionID || !strings.Contains(rolledBack.ReplyText, "处理方式已更新") || f.runner.snapshot()[3].Resume {
		t.Fatal("runtime rollback reused a stale session or name rename rotated it", first, renamed, changed, rolledBack)
	}
	if _, err := f.service.Store.UpdateProject(context.Background(), "beta", "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	moved := f.process(t, f.message(t, "moved", "continue", "text", "dm"))
	if moved.SessionID == rolledBack.SessionID || !strings.Contains(moved.ReplyText, "处理方式已更新") || f.runner.snapshot()[4].Resume {
		t.Fatal("secondary directory change was not a new runtime", moved)
	}
}

func TestAutomaticRoutingIgnoresHistoricalPinAndRejectsInventedContinuation(t *testing.T) {
	f := newRoutingFixture(t)
	status := f.process(t, f.message(t, "status", "/status", "text", "dm"))
	legacy := state.ChannelSelection{Mode: "manual", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha"}
	if err := f.service.Store.SetChannelSelection(context.Background(), status.Binding, legacy); err != nil {
		t.Fatal(err)
	}
	general := f.process(t, f.message(t, "default", "general", "text", "dm"))
	if general.Status != "completed" || general.ProjectID != "" || general.Workspace != f.host.Config.Scope {
		t.Fatal("historical manual selection pinned a new general task", general)
	}
	selection, err := f.service.Store.ChannelSelection(context.Background(), status.Binding)
	if err != nil || !reflect.DeepEqual(selection, legacy) {
		t.Fatal("automatic routing rewrote historical selection evidence", selection, err)
	}
	f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
		return routing.Decision{Kind: "project", ProjectIDs: []string{"alpha"}, PrimaryID: "alpha", ContinuationMessageID: "invented"}, nil
	})
	bad := f.process(t, f.message(t, "bad", "continue an invented task", "text", "dm"))
	if bad.Status != "failed" || bad.RunID != "" || len(f.runner.snapshot()) != 1 {
		t.Fatal("invented continuation executed", bad)
	}
}

func TestNewResetsAutomaticContinuationButRetainsCurrentScope(t *testing.T) {
	f := newRoutingFixture(t)
	first := f.process(t, f.message(t, "a", "Fix project alpha", "text", "dm"))
	reset := f.process(t, f.message(t, "reset", "/new", "text", "dm"))
	current := f.process(t, f.message(t, "current", "/status", "text", "dm"))
	if reset.ProjectID != "alpha" || !strings.Contains(current.ReplyText, "新对话尚无") || reset.SessionID == first.SessionID {
		t.Fatal("reset changed the current scope or reused native state", reset, current)
	}
	f.service.Router = serviceRouter(func(ctx context.Context, request routing.Request) (routing.Decision, error) {
		if request.Text == "继续" && len(request.Recent) == 0 {
			return routing.Decision{Kind: "clarify", Reason: "新会话没有可延续的任务，请说明项目和任务。"}, nil
		}
		return routing.New(nil).Decide(ctx, request)
	})
	continued := f.process(t, f.message(t, "continue", "继续", "text", "dm"))
	if continued.Status != "completed" || continued.RunID == "" || continued.ProjectID != "" || continued.SessionID == first.SessionID || len(f.runner.snapshot()) != 2 {
		t.Fatal("new silently reused stale follow-up context", continued)
	}
	fresh := f.process(t, f.message(t, "fresh", "Fix project alpha", "text", "dm"))
	if fresh.SessionID != reset.SessionID || f.runner.snapshot()[2].Resume {
		t.Fatal("explicit task after reset did not start in fresh current session", fresh)
	}
}

func TestMultiProjectMetadataStaysLocalAndPublicReplyKeepsByteBudget(t *testing.T) {
	var projects []state.ProjectRef
	var directories []string
	for i := range 50 {
		projects = append(projects, state.ProjectRef{ID: fmt.Sprintf("project-%02d-%s", i, strings.Repeat("<&中", 80))})
		directories = append(directories, "/"+strings.Repeat("<&中", 1000))
	}
	const session = "host-session-must-remain-visible"
	run := state.Run{ID: "run", Input: strings.Repeat("long input ", 100), Output: strings.Repeat("正文\n", 10000), Model: strings.Repeat("<&", 300), Projects: projects, ProjectID: projects[0].ID, Workspace: directories[0], AdditionalDirectories: directories, SessionID: session}
	metadata := projectSetMetadata(run.Projects, run.ProjectID, "", run.Workspace, run.AdditionalDirectories, session)
	text := resultText(run, nil)
	if len(metadata) > 1800 || len(text) > dingtalk.MaxReplyBytes || !utf8.ValidString(text) || strings.Contains(text, session) || !strings.Contains(metadata, "共 50 个") || strings.Contains(text, "联合任务不自动写回记忆") || strings.Contains(metadata, "<") {
		t.Fatalf("large route metadata exceeded budget or hid session: metadataBytes=%d totalBytes=%d text=%s", len(metadata), len(text), text)
	}
}

func TestGenericProjectFollowupsAlwaysPassThroughAutomaticRouting(t *testing.T) {
	f := newRoutingFixture(t)
	first := f.process(t, f.message(t, "joint", "Compare alpha and beta", "text", "dm"))
	if first.Status != "completed" {
		t.Fatal(first)
	}
	calls := 0
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		calls++
		if len(req.Recent) == 0 || len(req.Recent[0].ProjectIDs) != 2 {
			t.Fatal("followup lost actual project context", req)
		}
		return routing.Decision{Kind: "project", ProjectIDs: []string{"alpha", "beta"}, ContinuationMessageID: req.Recent[0].MessageID}, nil
	})
	for i, text := range []string{"报告两个项目的规则标记，不要修改文件。", "读取两个项目 README.md。", "Check the current project rules", "检查当前项目的测试", "读取本项目的规则", "报告这两个项目的状态"} {
		m := f.process(t, f.message(t, fmt.Sprintf("generic-%d", i), text, "text", "dm"))
		if m.Status != "completed" || m.RunID == "" || len(m.Projects) != 2 || m.ProjectID != "" || m.SessionID != first.SessionID {
			t.Fatal("grounded joint-task continuation failed", m)
		}
	}
	if calls != 6 || len(f.runner.snapshot()) != 7 {
		t.Fatal("some followups bypassed automatic routing", calls)
	}
}

func TestMultiplePendingTasksRequireNumberAndDoNotConsumeNewTask(t *testing.T) {
	f := newRoutingFixture(t)
	original := legacyPending(t, f, f.message(t, "waiting", "ambiguous original", "text", "dm"))
	legacyPending(t, f, f.message(t, "other-waiting", "ambiguous other task", "text", "dm"))
	qid := choiceFor(original)
	bare := f.process(t, f.message(t, "bare", "general", "text", "dm"))
	choice, err := f.service.Store.ChannelChoice(context.Background(), qid)
	if err != nil || choice.Status != "pending" || bare.Status != "completed" || bare.RunID == "" || len(f.runner.snapshot()) != 1 {
		t.Fatal("ordinary input claimed a historical request", bare, choice, err)
	}
	independent := f.process(t, f.message(t, "new-task", "Fix project alpha.", "text", "dm"))
	if independent.Status != "completed" || independent.RunID == "" || f.receipt(t, "waiting").RunID != "" || len(f.runner.snapshot()) != 2 {
		t.Fatal("ordinary concrete task was blocked by pending clarification", independent)
	}
}

func TestNumberedLegacyProjectConfirmationAfterRestartExactlyOnce(t *testing.T) {
	for _, answer := range []string{"alpha", "Project alpha", "账单", "Project alpha、Project beta", "alpha 和 beta"} {
		t.Run(answer, func(t *testing.T) {
			f := newRoutingFixture(t)
			if _, err := f.service.Store.UpdateProjectMetadata(context.Background(), "alpha", "Project alpha", "", []string{"账单"}); err != nil {
				t.Fatal(err)
			}
			original := legacyPending(t, f, f.message(t, "waiting", "ambiguous original request", "text", "dm"))
			if !strings.Contains(original.ReplyText, "回复项目名称") || strings.Contains(original.ReplyText, "PROJECT_ID") || strings.Contains(original.ReplyText, "/choose") {
				t.Fatal("ordinary clarification exposed command syntax", original.ReplyText)
			}
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
			confirmationText := "确认 " + choiceFor(original) + " " + answer
			confirmation := f.process(t, f.message(t, "answer", confirmationText, "text", "dm"))
			original = f.receipt(t, "waiting")
			run := f.run(t, original)
			choice, err := store.ChannelChoice(context.Background(), choiceFor(original))
			if err != nil || choice.Status != "resolved" || choice.ConfirmationMessageID != confirmation.ID || original.Status != "completed" || confirmation.RunID != "" || confirmation.Text != confirmationText || run.Input != "ambiguous original request" || run.Source == nil || run.Source.MessageID != "waiting" {
				t.Fatalf("natural answer lost task/confirmation identity: choice=%+v run=%+v confirmation=%+v err=%v", choice, run, confirmation, err)
			}
			if strings.Contains(answer, "beta") {
				if len(run.Projects) != 2 || run.ProjectID != "" {
					t.Fatal("joint answer selected a primary", run)
				}
			} else if run.ProjectID != "alpha" || run.Workspace != f.projects["alpha"].Workspace {
				t.Fatal("name/alias did not select alpha", run)
			}
			if !strings.Contains(f.replier.snapshot()[1].url, "cap-answer") {
				t.Fatal("answer did not use fresh reply capability")
			}
			f.receive(t, f.message(t, "answer", confirmationText, "text", "dm"))
			if _, err := store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("duplicate delivery queued an answer", err)
			}
			again := f.process(t, f.message(t, "answer-again", confirmationText, "text", "dm"))
			if again.RunID != "" || len(f.runner.snapshot()) != 1 || !strings.Contains(again.ReplyText, "已处理") {
				t.Fatal("repeated answer started native work", again)
			}
		})
	}
}

func TestNumberedLegacyConfirmationRevalidatesDirectoryAndConversation(t *testing.T) {
	f := newRoutingFixture(t)
	original := legacyPending(t, f, f.message(t, "waiting", "ambiguous original", "text", "dm"))
	f.process(t, f.message(t, "other-conversation", "确认 "+choiceFor(original)+" alpha", "text", "other-dm"))
	if err := os.RemoveAll(f.projects["alpha"].Workspace); err != nil {
		t.Fatal(err)
	}
	answer := f.process(t, f.message(t, "answer", "确认 "+choiceFor(original)+" Project alpha", "text", "dm"))
	choice, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(original))
	if err != nil || choice.Status != "pending" || answer.Status != "failed" || len(f.runner.snapshot()) != 0 {
		t.Fatal("invalid natural confirmation executed or consumed pending work", choice, answer, err)
	}
	cancelled := f.process(t, f.message(t, "cancel", "取消 "+choiceFor(original), "text", "dm"))
	choice, err = f.service.Store.ChannelChoice(context.Background(), choiceFor(original))
	if err != nil || choice.Status != "canceled" || cancelled.Status != "completed" || len(f.runner.snapshot()) != 0 {
		t.Fatal("natural cancellation did not cancel the unique pending task", choice, cancelled, err)
	}
}

func TestProjectReplyRecognizesOnlyCompleteUnambiguousNames(t *testing.T) {
	projects := []state.Project{{ID: "alpha", Name: "Billing API", Aliases: []string{"共享", "账单"}}, {ID: "beta", Name: "Identity Service", Aliases: []string{"共享"}}}
	for _, tc := range []struct {
		text      string
		ids       []string
		ambiguous bool
	}{
		{"Billing API", []string{"alpha"}, false},
		{"Billing API、Identity Service", []string{"alpha", "beta"}, false},
		{"账单", []string{"alpha"}, false},
		{"alpha beta", []string{"alpha", "beta"}, false},
		{"共享", nil, true},
		{"alpha alpha", nil, true},
		{"alpha、账单", nil, true},
		{"好的", nil, false},
		{"yes", nil, false},
		{"不要 alpha", nil, false},
		{"Fix project alpha.", nil, false},
		{"hi 能给我干啥不", nil, false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			ids, recognized, err := projectReplyIDs(tc.text, projects)
			if !reflect.DeepEqual(ids, tc.ids) || recognized != (len(tc.ids) > 0 || tc.ambiguous) || (err != nil) != tc.ambiguous {
				t.Fatalf("reply interpretation: ids=%v recognized=%v err=%v", ids, recognized, err)
			}
		})
	}
}

// Replay the production mode/association split through the real router and
// channel worker, including queued execution and an unrelated pending task.
type greetingEvaluator struct{}

func (greetingEvaluator) Provider() string { return "test" }
func (greetingEvaluator) Endpoint() string { return "https://example.invalid" }
func (greetingEvaluator) Model() string    { return "recorded-greeting" }
func (greetingEvaluator) Evaluate(_ context.Context, _ any, questions map[string]jev.Question) (jev.Response, error) {
	r := jev.Response{Model: "recorded-greeting", Answers: map[string]jev.Answer{}}
	for key, q := range questions {
		selected := "unrelated"
		if key == "primary" || strings.HasPrefix(key, "e_") {
			selected = "none"
		}
		switch key {
		case "project_need":
			selected = "not_required"
		case "latest_relation":
			selected = "independent"
		case "scope_change":
			selected = "change"
		}
		probabilities := map[string]float64{}
		for option := range q.Criteria.(map[string]string) {
			probabilities[option] = 0
		}
		probabilities[selected] = 1
		r.Answers[key] = jev.Answer{Type: "choice", Choice: selected, Probabilities: probabilities}
	}
	r.Answers["mode"] = jev.Answer{Type: "choice", Choice: "clarify", Probabilities: map[string]float64{"clarify": 0.54, "general": 0.42, "continuation": 0.04, "project": 0}}
	r.Answers["association"] = jev.Answer{Type: "choice", Choice: "general", Probabilities: map[string]float64{"general": 0.94, "known": 0.01, "unknown": 0.05}}
	return r, nil
}

func TestGreetingRunsInDefaultWorkspaceWhileProjectTaskWaits(t *testing.T) {
	f := newRoutingFixture(t)
	f.process(t, f.message(t, "previous-project", "Fix alpha", "text", "dm"))
	original := legacyPending(t, f, f.message(t, "waiting", "ambiguous project task", "text", "dm"))
	f.service.Router = routing.New(greetingEvaluator{})
	input := f.message(t, "greeting", "hi 能给我干啥不", "text", "dm")
	f.receive(t, input)
	f.receive(t, input)
	if err := f.service.process(context.Background(), f.next(t)); err != nil {
		t.Fatal(err)
	}
	greeting := f.receipt(t, "greeting")
	choice, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(original))
	if err != nil || choice.Status != "pending" || greeting.Status != "completed" || greeting.RunID == "" || greeting.ProjectID != "" || greeting.Workspace != f.host.Config.Scope || strings.Contains(greeting.ReplyText, "/choose") || len(f.runner.snapshot()) != 2 {
		t.Fatal("greeting inherited project, consumed pending work or did not execute", greeting, choice, err)
	}
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate greeting queued", err)
	}
}

func TestAutomaticSessionRecoveryPreservesProjectMemoryAndOtherSessions(t *testing.T) {
	f := newRoutingFixture(t)
	const preference = "alpha: I prefer concise answers."
	f.host.Decider = serviceDecider(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "preference", Store: input == preference, Model: "fixture"}, nil
	})
	a := f.process(t, f.message(t, "a", preference, "text", "dm"))
	b := f.process(t, f.message(t, "b", "Inspect beta.", "text", "dm"))
	f.runner.run = func(context.Context, harness.Request) (harness.Result, error) {
		return harness.Result{Status: "failed"}, errors.New("fixture API error")
	}
	failed := f.process(t, f.message(t, "failed-a", "Inspect alpha.", "text", "dm"))
	if failed.Status != "failed" || failed.SessionID != a.SessionID {
		t.Fatal("fixture did not fail alpha session", failed)
	}
	f.runner.run = nil
	recovered := f.process(t, f.message(t, "recover-a", "Check alpha again.", "text", "dm"))
	run := f.run(t, recovered)
	session, err := f.service.Store.Session(context.Background(), recovered.SessionID)
	if err != nil || recovered.Status != "completed" || recovered.SessionID == a.SessionID || run.Scope != f.projects["alpha"].Scope || run.Workspace != f.projects["alpha"].Workspace || len(run.Context) != 1 || run.Context[0].Text != preference || session.PreviousSessionID != a.SessionID || session.RecoveryRunID != failed.RunID || !strings.Contains(recovered.ReplyText, "本条消息") || f.runner.snapshot()[3].Resume {
		t.Fatal("project recovery lost memory or runtime identity", run, session, err)
	}
	resumedB := f.process(t, f.message(t, "resume-b", "Check beta again.", "text", "dm"))
	if resumedB.SessionID != b.SessionID || !f.runner.snapshot()[4].Resume || strings.Contains(resumedB.ReplyText, "本条消息") {
		t.Fatal("recovering alpha reset beta", resumedB)
	}
	if f.run(t, failed).ExecutionStatus != "failed" {
		t.Fatal("old failure was rewritten")
	}
}
