package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func TestRecoveredQuotedFollowupReceivesFailedQuestionWithoutHistoryTools(t *testing.T) {
	f := newRoutingFixture(t)
	const question = "你知道如何操作cbs cli +s3-proxy操作对象存储吗"
	const followup = "再试试"
	const unverifiedOutput = "Object upload succeeded."
	var failed state.ChannelMessage
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		d := routing.Decision{Kind: "clarify", Reason: "No registered project is established for this object-storage question."}
		if req.Text == followup {
			if len(req.Recent) != 1 || req.Recent[0].Text != question || req.Recent[0].RunID != failed.RunID {
				t.Fatal("follow-up routing lost the failed question", req.Recent)
			}
			return continueLatestTask(req.Recent[0]), nil
		}
		return d, nil
	})
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		err := errors.New("native model stream ended unexpectedly")
		return harness.Result{Status: "failed", Text: unverifiedOutput, Error: err.Error(), Model: req.Model}, err
	}
	failed = f.process(t, f.message(t, "cbs-question", question, "text", "owner-dm"))
	if failed.RunID == "" {
		t.Fatal("fixture did not dispatch original question", failed)
	}
	prior := f.run(t, failed)
	if failed.Status != "failed" || prior.ExecutionStatus != "failed" || prior.MemoryStatus != "skipped" || prior.MemoryAttribution != "" {
		t.Fatal("fixture did not preserve separate native failure and skipped memory", failed, prior)
	}
	f.runner.run = nil
	input := inputCallback(t, f.serviceFixture, "retry", "text", followup, nil, map[string]any{
		"msgId": "bot-reply", "msgType": "text", "content": map[string]string{"text": "Agent-Host 回复"},
	})
	recovered := f.process(t, input)
	if recovered.RunID == "" {
		t.Fatal("follow-up did not dispatch native work", recovered)
	}
	current := f.run(t, recovered)
	requests := f.runner.snapshot()
	if len(requests) != 2 || requests[1].Resume || recovered.SessionID == failed.SessionID || recovered.Status != "completed" {
		t.Fatal("follow-up did not enter one fresh native session", recovered, requests)
	}
	if current.Input != followup || current.Source == nil || current.Source.MessageID != input.ID || current.Source.Input == nil || current.Source.Input.Quote == nil || current.Source.Input.Quote.Body != "Agent-Host 回复" || current.MemoryAttribution != "" || current.MemoryStatus == "skipped" || current.ExperienceStatus == "skipped" {
		t.Fatal("recovery changed the current task or paused default learning", current)
	}
	if len(requests[1].MCPServers) != 0 {
		t.Fatal("test no longer covers recovery without on-demand history", requests[1].MCPServers)
	}
	for _, want := range []string{question, followup, "Agent-Host 回复", prior.ID, failed.SessionID, failed.MessageID, "failed", "skipped"} {
		if !strings.Contains(requests[1].Prompt, want) {
			t.Errorf("fresh follow-up prompt lost recovery context %q", want)
		}
	}
	if strings.Contains(requests[1].Prompt, unverifiedOutput) {
		t.Fatal("failed partial output became successful recovery evidence", requests[1].Prompt)
	}
	if got := f.run(t, failed); !reflect.DeepEqual(got, prior) {
		t.Fatal("recovery rewrote previous execution evidence", got, prior)
	}
	f.receive(t, input)
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) || len(f.runner.snapshot()) != 2 {
		t.Fatal("duplicate follow-up replayed native work", err)
	}
	next := f.process(t, f.message(t, "next", "先解释命令用途。", "text", "owner-dm"))
	requests = f.runner.snapshot()
	if next.SessionID != recovered.SessionID || len(requests) != 3 || !requests[2].Resume || strings.Contains(requests[2].Prompt, question) {
		t.Fatal("recovery context was reinjected after native continuation resumed", next, requests)
	}
}

func TestRecoveredIndependentRequestKeepsCurrentInputAndMemoryAttribution(t *testing.T) {
	f := newServiceFixture(t)
	const priorQuestion = "Inspect the old object-storage deployment."
	const independent = "Write a short welcome message for a new colleague."
	var memoryInputs []string
	f.host.Decider = serviceDecider(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		memoryInputs = append(memoryInputs, input)
		return memory.Decision{Kind: "none", Model: "fixture-decision"}, nil
	})
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		err := errors.New("native connection closed")
		return harness.Result{Status: "failed", Error: err.Error(), Model: req.Model}, err
	}
	failed := f.process(t, f.message(t, "failed", priorQuestion, "text", "owner-dm"))
	f.runner.run = nil
	fresh := f.process(t, f.message(t, "independent", independent, "text", "owner-dm"))
	run := f.run(t, fresh)
	requests := f.runner.snapshot()
	if len(requests) != 2 || requests[1].Resume || fresh.SessionID == failed.SessionID || run.Input != independent || run.Source == nil || run.Source.MessageID != "independent" || !reflect.DeepEqual(memoryInputs, []string{independent}) {
		t.Fatal("recovery silently replaced the new task or attributed old context as memory", fresh, run, memoryInputs)
	}
	if !strings.Contains(requests[1].Prompt, independent) || !strings.Contains(requests[1].Prompt, priorQuestion) {
		t.Fatal("native recovery lost current input or the prior topic needed to judge continuity", requests[1].Prompt)
	}
	if f.run(t, failed).ExecutionStatus != "failed" {
		t.Fatal("independent request rewrote the failed task")
	}
}

func TestRecoveredSessionFromEarlierVersionReceivesContextOnResume(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	const question = "Explain how CBS CLI and s3-proxy operate object storage."
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		err := errors.New("native model stream ended unexpectedly")
		return harness.Result{Status: "failed", Error: err.Error(), Model: req.Model}, err
	}
	failed := f.process(t, f.message(t, "failed", question, "text", "owner-dm"))
	f.runner.run = nil
	// Emulate the old binary's completed recovery turn without assembling the
	// lost question. The native session is resumable but has no recovery event.
	legacyInput := f.message(t, "legacy-retry", "再试试", "text", "owner-dm")
	f.receive(t, legacyInput)
	claimed := f.next(t)
	session, _, err := f.service.sessionWithHost(ctx, f.host, claimed.Binding, false)
	if err != nil || session.PreviousSessionID != failed.SessionID || session.RecoveryRunID != failed.RunID {
		t.Fatal("legacy fixture lost persisted recovery lineage", session, err)
	}
	legacy := state.Run{
		ID: "legacy-recovered-run", SessionID: session.ID, Scope: session.Scope,
		AgentID: session.AgentID, AudienceID: session.AudienceID, NativeSessionID: session.NativeSessionID,
		Input: legacyInput.Text, Agent: "local-claude", RequestedModel: f.host.Config.Model,
		ProjectID: session.ProjectID, ProjectName: session.ProjectName, Workspace: session.Workspace,
		RuntimeFingerprint: session.RuntimeFingerprint, Projects: session.Projects, AdditionalDirectories: session.AdditionalDirectories,
	}
	if err := f.service.Store.CreateChannelRun(ctx, legacy, claimed.ID); err != nil {
		t.Fatal(err)
	}
	const oldAnswer = "I cannot see your earlier question. Please send it again."
	if err := f.service.Store.CompleteRun(ctx, legacy.ID, "completed", oldAnswer, f.host.Config.Model, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.FinalizeMemory(ctx, legacy.ID, nil, json.RawMessage(`{"kind":"none"}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.FinishChannelMessage(ctx, claimed.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.PrepareChannelReply(ctx, claimed.ID, oldAnswer); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.FinishChannelReply(ctx, claimed.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	const followup = "回顾上一个问题后继续。"
	resumed := f.process(t, f.message(t, "after-upgrade", followup, "text", "owner-dm"))
	requests := f.runner.snapshot()
	if len(requests) != 2 || !requests[1].Resume || resumed.SessionID != session.ID || f.run(t, resumed).Input != followup {
		t.Fatal("legacy recovery repair rotated or replayed the native session", resumed, requests)
	}
	for _, want := range []string{question, followup, failed.RunID, failed.SessionID, "failed"} {
		if !strings.Contains(requests[1].Prompt, want) {
			t.Errorf("resumed legacy recovery prompt lost %q", want)
		}
	}
	next := f.process(t, f.message(t, "after-repair", "继续说明。", "text", "owner-dm"))
	requests = f.runner.snapshot()
	if len(requests) != 3 || !requests[2].Resume || next.SessionID != resumed.SessionID || strings.Contains(requests[2].Prompt, question) {
		t.Fatal("legacy recovery repair reinjected context on every resumed turn", next, requests)
	}
}

func TestRepeatedRecoveryRetainsOriginalQuestionAcrossFailedRetry(t *testing.T) {
	f := newServiceFixture(t)
	const question = "你知道如何操作cbs cli +s3-proxy操作对象存储吗"
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		err := errors.New("native connection interrupted")
		return harness.Result{Status: "failed", Error: err.Error(), Model: req.Model}, err
	}
	first := f.process(t, f.message(t, "question", question, "text", "owner-dm"))
	second := f.process(t, f.message(t, "retry-one", "再试试", "text", "owner-dm"))
	f.runner.run = nil
	third := f.process(t, f.message(t, "retry-two", "继续", "text", "owner-dm"))
	requests := f.runner.snapshot()
	if len(requests) != 3 || requests[1].Resume || requests[2].Resume || third.Status != "completed" || first.SessionID == second.SessionID || second.SessionID == third.SessionID || f.run(t, third).Input != "继续" {
		t.Fatal("repeated recovery replayed old tasks or reused failed sessions", first, second, third, requests)
	}
	for _, want := range []string{question, "再试试", "继续", first.RunID, second.RunID, "failed"} {
		if !strings.Contains(requests[2].Prompt, want) {
			t.Errorf("second recovery prompt lost earlier source context %q", want)
		}
	}
	if f.run(t, first).ExecutionStatus != "failed" || f.run(t, second).ExecutionStatus != "failed" {
		t.Fatal("successful follow-up rewrote failed execution outcomes")
	}
}

func TestRuntimeChangeSuppliesPriorQuestionToSparseFreshFollowup(t *testing.T) {
	f := newRoutingFixture(t)
	const question = "你知道如何操作cbs cli +s3-proxy操作对象存储吗"
	const followup = "再试试"
	const unverifiedOutput = "Unverified old native output: the object upload succeeded."
	f.service.Router = serviceRouter(func(_ context.Context, req routing.Request) (routing.Decision, error) {
		if req.Text == question {
			return routing.Decision{Kind: "clarify", Reason: "The object-storage question has no established project."}, nil
		}
		if len(req.Recent) == 0 {
			t.Fatal("sparse follow-up lost its earlier routed request")
		}
		return continueLatestTask(req.Recent[0]), nil
	})
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		err := errors.New("native model stream ended unexpectedly")
		return harness.Result{Status: "failed", Text: unverifiedOutput, Error: err.Error(), Model: req.Model}, err
	}
	failed := f.process(t, f.message(t, "cbs-question", question, "text", "owner-dm"))
	if failed.Status != "failed" || failed.RunID == "" {
		t.Fatal("fixture did not preserve the original native failure", failed)
	}
	f.runner.run = nil
	// A changed runtime starts a new session without failure-recovery links.
	f.policy = "changed-runtime-policy"
	fresh := f.process(t, f.message(t, "retry-after-runtime-change", followup, "text", "owner-dm"))
	session, err := f.service.Store.Session(context.Background(), fresh.SessionID)
	requests := f.runner.snapshot()
	if err != nil || len(requests) != 2 || requests[1].Resume || fresh.Status != "completed" || fresh.SessionID == failed.SessionID || fresh.RuntimeFingerprint == failed.RuntimeFingerprint || session.PreviousSessionID != "" || session.RecoveryRunID != "" {
		t.Fatal("runtime change did not create an unlinked fresh native session", fresh, session, requests, err)
	}
	current := f.run(t, fresh)
	if current.Input != followup || current.Source == nil || current.Source.MessageID != fresh.MessageID || current.MemoryAttribution != "" || current.MemoryStatus == "skipped" || len(requests[1].MCPServers) != 0 {
		t.Fatal("request handoff changed the input, learning policy or history access", current, requests[1])
	}
	for _, want := range []string{question, followup, failed.MessageID} {
		if !strings.Contains(requests[1].Prompt, want) {
			t.Errorf("fresh runtime prompt lost earlier request context %q", want)
		}
	}
	for _, unwanted := range []string{unverifiedOutput, failed.ReplyText} {
		if strings.Contains(requests[1].Prompt, unwanted) {
			t.Fatal("fresh-session request handoff included a prior result or reply", unwanted)
		}
	}
	next := f.process(t, f.message(t, "next", "先解释命令用途。", "text", "owner-dm"))
	requests = f.runner.snapshot()
	if len(requests) != 3 || !requests[2].Resume || next.SessionID != fresh.SessionID || strings.Contains(requests[2].Prompt, question) {
		t.Fatal("recent request context was reinjected during native continuation", next, requests)
	}
}
