package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

// Drain only in these synchronous tests so every actual host phase can be
// inspected. Production uses the independent worker exercised below.
func observeAndDrainHostPhases(t *testing.T, f *serviceFixture, messageID string) *[]string {
	t.Helper()
	phases := []string{}
	f.host.Sink = func(e event.Event) error {
		if e.Type != "phase.started" {
			return nil
		}
		run, err := f.service.Store.Run(context.Background(), e.RunID)
		if err != nil {
			return err
		}
		if run.Source == nil || run.Source.ChannelMessageID != messageID {
			return fmt.Errorf("phase %s lost the original message source", e.Phase)
		}
		persisted := false
		for _, saved := range run.Events {
			if saved.Seq == e.Seq && saved.Type == e.Type && saved.Phase == e.Phase {
				persisted = true
			}
		}
		if !persisted || e.Seq <= 0 {
			return fmt.Errorf("phase %s reached the sink before durable event storage", e.Phase)
		}
		reactions, err := f.service.Store.ChannelReactions(context.Background(), messageID)
		if err != nil {
			return err
		}
		for _, reaction := range reactions {
			// The answer is delivered before closeout, so its reply and terminal
			// markers may already exist when memory phases start.
			if run.ExecutionStatus == "completed" && (e.Phase == "remember" || e.Phase == "persist" || e.Phase == "evidence" || e.Phase == "extract" || e.Phase == "assess" || e.Phase == "candidates") {
				continue
			}
			if reaction.Stage != "received" && reaction.Stage != "processing" {
				return fmt.Errorf("internal phase leaked into public outbox: %+v", reaction)
			}
		}
		if e.Phase == "remember" && (run.ExecutionStatus != "completed" || run.MemoryStatus != "pending") {
			return fmt.Errorf("native completion and pending memory were conflated: execution=%s memory=%s", run.ExecutionStatus, run.MemoryStatus)
		}
		phases = append(phases, e.Phase)
		drainReactions(t, f.service)
		return nil
	}
	return &phases
}

func addedReactionLabels(t *testing.T, f *serviceFixture, conversationID, messageID string) []string {
	t.Helper()
	var labels []string
	for _, call := range f.service.Reactor.(*serviceReactor).snapshot() {
		if call.conversation != conversationID || call.message != messageID {
			t.Fatalf("reaction targeted a different message: %+v", call)
		}
		if call.action == "add" {
			labels = append(labels, call.label)
		}
	}
	return labels
}

func TestHostPhasesRemainLocalWhilePublicProgressStaysGeneric(t *testing.T) {
	f := newServiceFixture(t)
	input := f.message(t, "phase-source", "我偏好中文回复。", "text", "phase-dm")
	f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "preference", Store: true, Model: "fixture-decision"}, nil
	})
	phases := observeAndDrainHostPhases(t, f, Key("dingtalk", f.service.ClientID, input.ID))
	f.replier.reply = func(context.Context, replyCall) (string, error) {
		drainReactions(t, f.service)
		m := f.receipt(t, input.ID)
		for _, reaction := range m.Reactions {
			if reaction.Stage == "completed" || reaction.Stage == "failed" {
				t.Fatalf("terminal marker preceded reply acceptance: %+v", reaction)
			}
		}
		last := m.Reactions[len(m.Reactions)-1]
		if last.Stage != "processing" || last.Status != "accepted" || !last.Active {
			t.Fatalf("reply operation did not expose its own stage: %+v", last)
		}
		return "accepted", nil
	}
	m := f.process(t, input)
	drainReactions(t, f.service)
	if want := []string{"recall", "execute", "remember", "persist", "evidence", "candidates"}; !reflect.DeepEqual(*phases, want) {
		t.Fatalf("actual host phases: got=%v want=%v", *phases, want)
	}
	wantLabels := []string{"已收到", "处理中", "已完成"}
	if got := addedReactionLabels(t, f, input.ConversationID, input.ID); !reflect.DeepEqual(got, wantLabels) {
		t.Fatalf("phase labels: got=%v want=%v", got, wantLabels)
	}
	run := f.run(t, m)
	if run.ExecutionStatus != "completed" || run.MemoryStatus != "saved" || run.AcceptanceStatus != "unverified" || m.ReplyStatus != "accepted" {
		t.Fatalf("independent outcomes changed: run=%+v message=%+v", run, m)
	}
}

type phaseToolRunner struct{ paired bool }

func (r phaseToolRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	observations := []harness.Observation{
		{Type: "init", Model: "fixture-native", NativeSessionID: req.NativeSessionID},
		{Type: "tool_start", Tool: &harness.ToolObservation{ID: "test-1", Name: "Bash", Stage: "call", Input: json.RawMessage(`{"command":"go test ./..."}`)}},
	}
	if r.paired {
		observations = append(observations, harness.Observation{Type: "tool_result", Tool: &harness.ToolObservation{ID: "test-1", Name: "Bash", Stage: "result", Result: json.RawMessage(`{"stdout":"ok fixture","exit_code":0}`)}})
	}
	observations = append(observations, harness.Observation{Type: "result", Model: "fixture-native", Message: "completed"})
	for _, observation := range observations {
		if err := emit(observation); err != nil {
			return harness.Result{Status: "unknown"}, err
		}
	}
	return harness.Result{Status: "completed", Text: "The test command reported passing.", Model: "fixture-native", NativeSessionID: req.NativeSessionID}, nil
}

type phaseExtractor func(context.Context, state.Run, []state.ToolEvidence, func(harness.Observation) error) (experience.Extraction, error)

func (f phaseExtractor) Extract(ctx context.Context, r state.Run, evidence []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
	return f(ctx, r, evidence, emit)
}

type phaseJudge struct{ calls int }

func (j *phaseJudge) Assess(_ context.Context, _ state.ExperienceDraft, evidence []state.ToolEvidence) (experience.Assessment, error) {
	j.calls++
	if len(evidence) != 1 || evidence[0].ID != "test-1" || evidence[0].CallSeq >= evidence[0].ResultSeq {
		return experience.Assessment{}, errors.New("Jev assessment lost paired tool evidence")
	}
	return experience.Assessment{Eligible: true, Kind: "procedure", Model: "fixture-jev", Audit: json.RawMessage(`{"reason":"eligible_for_review"}`)}, nil
}

func (*phaseJudge) Compare(context.Context, state.Candidate, []state.Memory) (experience.Relations, error) {
	return experience.Relations{}, errors.New("pending experience must not be compared for publication")
}

func TestHostExperienceMarkersRequireActualExtractionAndAssessment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		paired     bool
		draft      bool
		wantPhases []string
		wantLabels []string
	}{
		{"unpaired tool", false, false,
			[]string{"recall", "execute", "remember", "persist", "evidence"},
			[]string{"已收到", "处理中", "已完成"}},
		{"paired tool without drafts", true, false,
			[]string{"recall", "execute", "remember", "persist", "evidence", "extract", "candidates"},
			[]string{"已收到", "处理中", "已完成"}},
		{"paired tool with draft", true, true,
			[]string{"recall", "execute", "remember", "persist", "evidence", "extract", "assess", "candidates"},
			[]string{"已收到", "处理中", "已完成"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			f.host.Runner = phaseToolRunner{paired: tc.paired}
			f.host.Config.ToolsDisabled = false
			judge := &phaseJudge{}
			f.host.Judge = judge
			extractions := 0
			f.host.Extractor = phaseExtractor(func(_ context.Context, run state.Run, evidence []state.ToolEvidence, _ func(harness.Observation) error) (experience.Extraction, error) {
				extractions++
				if run.ExecutionStatus != "completed" || len(evidence) != 1 || evidence[0].ID != "test-1" || evidence[0].CallSeq >= evidence[0].ResultSeq {
					return experience.Extraction{}, errors.New("extractor did not receive durable paired evidence")
				}
				extraction := experience.Extraction{Model: "fixture-extractor"}
				if tc.draft {
					extraction.Drafts = []state.ExperienceDraft{{Title: "Check fixture tests", Summary: "The fixture Go test command reported success.", Conditions: "When checking the fixture Go package", Steps: []string{"Run go test ./... and inspect its reported result."}, EvidenceIDs: []string{"test-1"}}}
				}
				return extraction, nil
			})
			input := f.message(t, "experience-phases", "Check the fixture tests.", "text", "experience-dm")
			phases := observeAndDrainHostPhases(t, f, Key("dingtalk", f.service.ClientID, input.ID))
			m := f.process(t, input)
			drainReactions(t, f.service)
			if !reflect.DeepEqual(*phases, tc.wantPhases) {
				t.Fatalf("model work phases: got=%v want=%v", *phases, tc.wantPhases)
			}
			if got := addedReactionLabels(t, f, input.ConversationID, input.ID); !reflect.DeepEqual(got, tc.wantLabels) {
				t.Fatalf("model work labels: got=%v want=%v", got, tc.wantLabels)
			}
			wantExtractions, wantAssessments := 0, 0
			if tc.paired {
				wantExtractions = 1
			}
			if tc.draft {
				wantAssessments = 1
			}
			if extractions != wantExtractions || judge.calls != wantAssessments {
				t.Fatalf("unexpected model calls: extraction=%d assessment=%d", extractions, judge.calls)
			}
			candidates, err := f.service.Store.Candidates(context.Background(), f.service.Scope, m.RunID, "all")
			if err != nil || len(candidates) != wantAssessments {
				t.Fatalf("candidate persistence: candidates=%+v error=%v", candidates, err)
			}
			// The answer was delivered before closeout, so an unpaired-tool
			// extraction failure stays on the run and never rewrites the message.
			if m.Status != "completed" || m.ReplyStatus != "accepted" {
				t.Fatalf("unexpected experience outcome: %+v", m)
			}
			if run := f.run(t, m); !tc.paired && run.ExperienceStatus != "failed" || tc.paired && run.ExperienceStatus == "failed" {
				t.Fatalf("closeout outcome lost: %+v", run)
			}
		})
	}
}

func TestHostJevFailureMarksFailureWithoutMemoryPersistence(t *testing.T) {
	f := newServiceFixture(t)
	f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{}, errors.New("fixture Jev decision failed")
	})
	input := f.message(t, "jev-failure-phases", "Remember my preference.", "text", "failure-dm")
	phases := observeAndDrainHostPhases(t, f, Key("dingtalk", f.service.ClientID, input.ID))
	m := f.process(t, input)
	drainReactions(t, f.service)
	if want := []string{"recall", "execute", "remember"}; !reflect.DeepEqual(*phases, want) {
		t.Fatalf("failure progressed to persistence: got=%v want=%v", *phases, want)
	}
	if got, want := addedReactionLabels(t, f, input.ConversationID, input.ID), []string{"已收到", "处理中", "已完成"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("answer markers: got=%v want=%v", got, want)
	}
	run := f.run(t, m)
	if run.ExecutionStatus != "completed" || run.MemoryStatus != "failed" || m.Status != "completed" || m.ReplyStatus != "accepted" || len(f.replier.snapshot()) != 1 {
		t.Fatalf("memory failure overwrote native or reply truth: run=%+v message=%+v", run, m)
	}
	memories, err := f.service.Store.Memories(context.Background(), f.service.Scope)
	if err != nil || len(memories) != 0 {
		t.Fatalf("failed judgment saved memory: %+v error=%v", memories, err)
	}
}

func TestWorkDeliversReplyWhileDecisionIsBlocked(t *testing.T) {
	f := newServiceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var firstDecision sync.Once
	f.host.Decider = serviceDecider(func(ctx context.Context, _ string, _ []state.Memory) (memory.Decision, error) {
		firstDecision.Do(func() { close(entered) })
		select {
		case <-release:
			return memory.Decision{Kind: "none", Model: "fixture-jev"}, nil
		case <-ctx.Done():
			return memory.Decision{}, ctx.Err()
		}
	})
	completed := make(chan state.ChannelReaction, 1)
	f.service.ObserveReaction = func(kind string, reaction state.ChannelReaction, _ time.Duration) error {
		if kind == "channel.reaction.finished" && reaction.Status == "accepted" && reaction.Stage == "completed" {
			completed <- reaction
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	input := f.message(t, "blocked-jev", "Explain the project.", "text", "blocked-dm")
	f.receive(t, input)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("Jev decision did not start")
	}
	// The decider is still BLOCKED: the answer and terminal marker must already
	// be delivered, while memory remains pending.
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("reply was not delivered while the decision was blocked")
	}
	m := f.receipt(t, input.ID)
	run := f.run(t, m)
	if m.Status != "completed" || m.ReplyStatus != "accepted" || len(f.replier.snapshot()) != 1 || run.ExecutionStatus != "completed" || run.MemoryStatus != "pending" {
		t.Fatalf("reply did not precede blocked closeout: message=%+v run=%+v", m, run)
	}
	for _, e := range run.Events {
		if e.Type == "run.finished" {
			t.Fatal("run.finished was emitted before closeout ended")
		}
	}
	followup := f.message(t, "after-early-reply", "Continue this conversation.", "text", "blocked-dm")
	f.receive(t, followup)
	// Let the worker scan the queue while closeout still owns the binding.
	time.Sleep(350 * time.Millisecond)
	if len(f.runner.snapshot()) != 1 || f.receipt(t, followup.ID).Status != "queued" {
		t.Fatal("an early reply released native-session exclusion during closeout")
	}
	close(release)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if run = f.run(t, f.receipt(t, input.ID)); run.MemoryStatus != "pending" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m = f.receipt(t, input.ID)
	if run.MemoryStatus == "pending" || m.Status != "completed" || m.ReplyStatus != "accepted" {
		t.Fatalf("closeout after release changed the delivered answer: message=%+v run=%+v", m, run)
	}
	for time.Now().Before(deadline) && f.receipt(t, followup.ID).ReplyStatus != "accepted" {
		time.Sleep(20 * time.Millisecond)
	}
	requests := f.runner.snapshot()
	if len(requests) != 2 || !requests[1].Resume || requests[0].NativeSessionID != requests[1].NativeSessionID || f.receipt(t, followup.ID).SessionID != m.SessionID {
		t.Fatalf("follow-up after closeout lost native continuity: %+v", requests)
	}
}
