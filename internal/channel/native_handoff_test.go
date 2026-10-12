package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func TestEmptyProjectCatalogBypassesClassifierForPrivateAndGroupTasks(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "group"}[group], func(t *testing.T) {
			f := newRoutingFixture(t)
			calls := 0
			f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
				calls++
				return routing.Decision{}, errors.New("classifier should not be called without a catalog")
			})
			empty := func(context.Context) ([]state.Project, error) { return nil, nil }
			input := f.message(t, "question", "Explain how a session token is validated.", "text", "dm")
			if group {
				instanceDispatch(t, f, map[string]string{})
				resolve := f.service.ResolveAgent
				f.service.ResolveAgent = func(ctx context.Context, m state.ChannelMessage) (*Service, error) {
					s, err := resolve(ctx, m)
					if s != nil {
						s.Projects = empty
					}
					return s, err
				}
				input = groupInput(t, f.serviceFixture, input.ID, "alice", "group", "@Helper "+input.Text)
			} else {
				f.service.Projects = empty
			}
			m := f.process(t, input)
			assertNativeHandoff(t, f, m, input)
			if calls != 0 {
				t.Fatal("empty catalog called the classifier", calls)
			}
		})
	}
}

func TestUncertainProjectRoutesHandOffToAgentAndRetainOriginalAudit(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, reason := range []string{"insufficient confidence", "unregistered target"} {
			t.Run(map[bool]string{false: "private/", true: "group/"}[group]+reason, func(t *testing.T) {
				f := newRoutingFixture(t)
				original := routing.Decision{Kind: "clarify", Reason: reason, CandidateIDs: []string{"alpha"}, Evidence: []routing.Evidence{{Kind: "synthetic", Text: "independent assessment"}}, Details: json.RawMessage(`{"policy":"synthetic-classifier","probability":0.72}`)}
				calls := 0
				f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
					calls++
					return original, nil
				})
				input := f.message(t, "question", "Explain the service's authentication options.", "text", "dm")
				if group {
					instanceDispatch(t, f, map[string]string{})
					input = groupInput(t, f.serviceFixture, input.ID, "alice", "group", "@Helper "+input.Text)
				}
				m := f.process(t, input)
				assertNativeHandoff(t, f, m, input)
				var decision routing.Decision
				var audit struct {
					Policy   string           `json:"policy"`
					Original routing.Decision `json:"original_decision"`
				}
				if err := json.Unmarshal(m.RoutingDecision, &decision); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(decision.Details, &audit); err != nil || audit.Policy != "channel-native-handoff-v2" || !reflect.DeepEqual(audit.Original, original) || calls != 1 {
					t.Fatal("handoff replaced the classifier evidence", audit, original, err)
				}
			})
		}
	}
}

func assertNativeHandoff(t *testing.T, f *projectFixture, m state.ChannelMessage, input dingtalk.Message) {
	t.Helper()
	var decision routing.Decision
	if err := json.Unmarshal(m.RoutingDecision, &decision); err != nil || decision.Kind != "default" || m.Status != "completed" || m.RunID == "" || m.ProjectID != "" || len(m.Projects) != 0 || m.Workspace != f.host.Config.Scope || m.Text != input.Text || len(f.runner.snapshot()) != 1 {
		t.Fatal("question did not enter the default native agent", m, err)
	}
	if strings.Contains(m.ReplyText, "在哪个项目") || strings.Contains(m.ReplyText, "请联系管理员") {
		t.Fatal("Host inserted a project-selection form", m.ReplyText)
	}
	if _, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(m)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("new task created a pending project choice", err)
	}
	run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(context.Background(), m.RunID)
	if err != nil || run.Input != input.Text || run.Source == nil || run.Source.MessageID != input.ID || run.Source.SenderID != input.SenderID {
		t.Fatal("handoff lost its verified source or exact task input", run, err)
	}
	if run.MemoryAttribution != "" || run.MemoryStatus == "skipped" || run.ExperienceStatus == "skipped" {
		t.Fatal("default scope unnecessarily disabled ordinary learning", run)
	}
}

func TestNativeHandoffDoesNotHideProviderCatalogOrDirectoryFailures(t *testing.T) {
	for _, failure := range []string{"provider", "catalog", "directory"} {
		t.Run(failure, func(t *testing.T) {
			f := newRoutingFixture(t)
			f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
				if failure == "provider" {
					return routing.Decision{}, errors.New("synthetic provider unavailable")
				}
				return routing.Decision{Kind: "clarify", Reason: "uncertain"}, nil
			})
			if failure == "catalog" {
				f.service.Projects = func(context.Context) ([]state.Project, error) { return nil, errors.New("synthetic catalog error") }
			}
			if failure == "directory" {
				if err := os.RemoveAll(f.host.Config.Scope); err != nil {
					t.Fatal(err)
				}
			}
			m := f.process(t, f.message(t, "question", "Inspect a general issue.", "text", "dm"))
			if m.Status != "failed" || m.RunID != "" || len(f.runner.snapshot()) != 0 {
				t.Fatal("operational failure was hidden by native handoff", m)
			}
			if _, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(m)); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("failed task created a choice", err)
			}
		})
	}
}

func TestBareProjectAndCancellationAnswersCannotClaimHistoricalTasks(t *testing.T) {
	f := newRoutingFixture(t)
	original := legacyPending(t, f, f.message(t, "historical", "Inspect the previous issue.", "text", "dm"))
	f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
		return routing.Decision{Kind: "default"}, nil
	})
	for _, text := range []string{"alpha", "取消"} {
		m := f.process(t, f.message(t, text, text, "text", "dm"))
		if m.Status != "completed" || m.RunID == "" || f.run(t, m).Input != text {
			t.Fatal("ordinary answer was intercepted before reaching the agent", m)
		}
	}
	choice, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(original))
	if err != nil || choice.Status != "pending" || f.receipt(t, "historical").RunID != "" || len(f.runner.snapshot()) != 2 {
		t.Fatal("ordinary answer claimed a stale request", choice, err)
	}
	confirmed := f.process(t, f.message(t, "explicit", "确认 "+choiceFor(original)+" alpha", "text", "dm"))
	if confirmed.Status != "completed" || f.receipt(t, "historical").RunID == "" || len(f.runner.snapshot()) != 3 {
		t.Fatal("explicit historical confirmation stopped working", confirmed)
	}
}

func TestNativeHandoffRestartPreservesEvidenceAndDoesNotReplayClaimedWork(t *testing.T) {
	f := newRoutingFixture(t)
	legacy := legacyPending(t, f, f.message(t, "historical", "A historical task.", "text", "dm"))
	firstInput := f.message(t, "first", "ambiguous current question", "text", "dm")
	first := f.process(t, firstInput)
	interruptedInput := f.message(t, "interrupted", "ambiguous interrupted question", "text", "dm")
	f.receive(t, interruptedInput)
	interrupted := f.next(t)
	_, _, decision, err := f.service.routeTask(context.Background(), interrupted)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.SaveChannelRoutingDecision(context.Background(), interrupted.ID, encoded); err != nil {
		t.Fatal(err)
	}
	f.receive(t, f.message(t, "queued", "ambiguous fresh question", "text", "dm"))
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
	f.receive(t, firstInput)
	f.receive(t, interruptedInput)
	queued := f.next(t)
	if queued.MessageID != "queued" {
		t.Fatal("restart selected previous work for replay", queued)
	}
	if err := f.service.process(context.Background(), queued); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate or claimed task was requeued", err)
	}
	choice, err := store.ChannelChoice(context.Background(), choiceFor(legacy))
	interrupted = f.receipt(t, "interrupted")
	if err != nil || choice.Status != "pending" || f.receipt(t, "historical").RunID != "" || interrupted.Status != "unknown" || interrupted.RunID != "" || string(interrupted.RoutingDecision) != string(encoded) || f.receipt(t, "first").RunID != first.RunID || len(f.runner.snapshot()) != 2 {
		t.Fatal("restart replayed a task or discarded historical evidence", choice, interrupted, err)
	}
}

func TestDefaultHandoffContinuationKeepsLearningWithoutRotatingDefaultSession(t *testing.T) {
	f := newRoutingFixture(t)
	f.host.Config.HostInstructions = host.ChannelInstructions("Answer the current question.", false)
	const oldTopic = "Explain how plain text is sorted."
	const unresolved = "Which authentication strategy fits the service?"
	const followup = "Use the handbook to explain that choice."
	const anotherFollowup = "Summarize the same tradeoff."
	const independent = "I prefer short answers for general explanations."
	var memoryInputs []string
	f.host.Decider = serviceDecider(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		memoryInputs = append(memoryInputs, input)
		return memory.Decision{Store: input == independent, Kind: "preference", Model: "synthetic-memory"}, nil
	})
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		switch req.Text {
		case oldTopic, independent:
			return routing.Decision{Kind: "default", Reason: "Independent ordinary request."}, nil
		case unresolved:
			if len(req.Recent) != 1 || req.Recent[0].Text != oldTopic {
				t.Fatal("fixture lost its unrelated preceding topic", req.Recent)
			}
			return routing.Decision{Kind: "clarify", Reason: "No project can be reliably selected."}, nil
		case followup, anotherFollowup:
			if len(req.Recent) == 0 || len(req.Recent[0].ProjectIDs) != 0 {
				t.Fatal("follow-up lost the actual default task source", req.Recent)
			}
			return continueLatestTask(req.Recent[0]), nil
		default:
			t.Fatal("unexpected synthetic request", req.Text)
			return routing.Decision{}, nil
		}
	})
	inputs := []string{oldTopic, unresolved, followup, anotherFollowup, independent}
	var messages []state.ChannelMessage
	for _, input := range inputs {
		m := f.process(t, f.message(t, input, input, "text", "dm"))
		if m.Status != "completed" || m.RunID == "" || m.ProjectID != "" || len(m.Projects) != 0 {
			t.Fatal("default dialogue failed or selected an unrelated project", m)
		}
		messages = append(messages, m)
	}
	requests := f.runner.snapshot()
	if len(requests) != len(inputs) || !reflect.DeepEqual(memoryInputs, inputs) {
		t.Fatal("default handoff or continuation blocked ordinary learning", len(requests), memoryInputs)
	}
	for i, m := range messages {
		run := f.run(t, m)
		if m.SessionID != messages[0].SessionID || requests[i].NativeSessionID != requests[0].NativeSessionID || requests[i].Resume != (i > 0) || requests[i].Workspace != f.host.Config.Scope || run.Input != inputs[i] {
			t.Fatal("per-turn attribution rotated the default conversation or changed input", i, m, requests[i])
		}
		if run.MemoryAttribution != "" || run.MemoryStatus == "skipped" || strings.Contains(requests[i].HostInstructions, "will not automatically publish memory") {
			t.Fatal("default dialogue kept the old writeback restriction", i, run, requests[i].HostInstructions)
		}
		for _, reminder := range []string{"Current-request scope supplied by the Host", "An older default-workspace topic does not establish this request's target"} {
			if !strings.Contains(requests[i].HostInstructions, reminder) {
				t.Fatal("default invocation lost its current-scope reminder", i, reminder, requests[i].HostInstructions)
			}
		}
		if i > 0 && strings.Contains(requests[i].Prompt, oldTopic) {
			t.Fatal("Host substituted an older default topic into the current task", i, requests[i].Prompt)
		}
		if !strings.Contains(requests[i].HostInstructions, "Authenticated current-message provenance supplied by the Host") {
			t.Fatal("resumed invocation lost authenticated sender guidance", i)
		}
		if i == 2 || i == 3 {
			var decision routing.Decision
			if err := json.Unmarshal(m.RoutingDecision, &decision); err != nil || decision.Kind != "default" || decision.ContinuationRunID != messages[i-1].RunID || decision.ContinuationMessageID != messages[i-1].MessageID {
				t.Fatal("continuation lost its authenticated routing source", decision, err)
			}
			var audit struct {
				Policy   string           `json:"policy"`
				Original routing.Decision `json:"original_decision"`
			}
			if err := json.Unmarshal(decision.Details, &audit); err != nil || audit.Policy != "channel-default-continuation-v2" || audit.Original.ContinuationRunID != decision.ContinuationRunID || audit.Original.ContinuationMessageID != decision.ContinuationMessageID {
				t.Fatal("continuation discarded its original classifier audit", audit, err)
			}
		}
	}
	if run := f.run(t, messages[4]); run.MemoryStatus != "saved" {
		t.Fatal("independent default preference did not resume normal learning", run)
	}
}
