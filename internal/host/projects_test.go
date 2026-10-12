package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func TestMultiProjectRecallKeepsScopesAndPausesAutomaticWrites(t *testing.T) {
	h, runner := testHost(t)
	neutral := h.Config.Scope
	projects := []state.ProjectRef{{ID: "a", Name: "A", Scope: "project:a", Workspace: t.TempDir()}, {ID: "b", Name: "B", Scope: "project:b", Workspace: t.TempDir()}}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) { return saveStatement(), nil })
	for _, item := range []struct{ scope, text string }{{neutral, "我偏好法文回复。"}, {projects[0].Scope, "我偏好中文回复。"}, {projects[1].Scope, "我偏好英文回复。"}} {
		h.Config.Scope = item.scope
		runOK(t, h, newSession(t, h), item.text)
	}
	h.Config.Scope, h.Config.Workspace = neutral, neutral
	h.Config.Projects, h.Config.AdditionalDirectories = projects, []string{projects[0].Workspace, projects[1].Workspace}
	h.Config.RuntimeFingerprint = "joint-a-b"
	h.Config.Supplements = []state.UserSupplement{{MessageID: "clarification-1", Text: "Compare both selected projects."}}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		t.Fatal("joint run reached automatic memory decision")
		return memory.Decision{}, nil
	})
	s := newSession(t, h)
	r := runOK(t, h, s, "Explain the preferred response language.")
	if r.MemoryStatus != "skipped" || r.ExperienceStatus != "skipped" || len(r.Context) != 2 || len(r.Projects) != 2 || len(r.AdditionalDirectories) != 2 {
		t.Fatalf("joint receipt: %+v", r)
	}
	if r.Input != "Explain the preferred response language." || len(r.Supplements) != 1 || r.Supplements[0].MessageID != "clarification-1" {
		t.Fatalf("original input or source identity changed: %+v", r)
	}
	gotScopes := map[string]bool{}
	for _, m := range r.Context {
		gotScopes[m.Scope] = true
	}
	if !gotScopes[projects[0].Scope] || !gotScopes[projects[1].Scope] || gotScopes[neutral] {
		t.Fatalf("memory namespaces merged: %+v", r.Context)
	}
	request := runner.requests[len(runner.requests)-1]
	for _, text := range []string{"project:a", "project:b", "我偏好中文回复。", "我偏好英文回复。", "clarification-1", "Compare both selected projects."} {
		if !strings.Contains(request.Prompt, text) {
			t.Errorf("prompt missing provenance %q", text)
		}
	}
	if strings.Contains(request.Prompt, "我偏好法文回复。") || request.Workspace != neutral || len(request.AdditionalDirectories) != 2 {
		t.Fatalf("neutral workspace leaked knowledge or lost native dirs: %+v", request)
	}
	queries := map[string]bool{}
	for _, e := range r.Events {
		if e.Type == "memory.query.analyzed" {
			scope, _ := e.Data["scope"].(string)
			queries[scope] = true
		}
	}
	if len(queries) != 2 || !queries[projects[0].Scope] || !queries[projects[1].Scope] {
		t.Fatalf("missing per-scope retrieval audit: %+v", queries)
	}
	if _, err := h.Session(t.Context(), s.ID); err != nil {
		t.Fatalf("skipped automatic memory blocked compatible resume: %v", err)
	}
	if _, err := h.RetryMemory(t.Context(), r.ID); err == nil {
		t.Fatal("joint memory retry allowed")
	}
	if _, err := h.RetryExperience(t.Context(), r.ID); err == nil {
		t.Fatal("joint experience retry allowed")
	}
	for _, scope := range []string{neutral, projects[0].Scope, projects[1].Scope} {
		memories, err := h.Store.Memories(t.Context(), scope)
		if err != nil || len(memories) != 1 {
			t.Fatalf("joint task wrote into %s: %+v %v", scope, memories, err)
		}
	}
	// A caller cannot reuse native history by forgetting to update a fingerprint.
	h.Config.Projects = projects[:1]
	h.Config.AdditionalDirectories = h.Config.AdditionalDirectories[:1]
	if _, err := h.Session(t.Context(), s.ID); err == nil {
		t.Fatal("changed project membership resumed old history")
	}
	if _, err := h.Run(t.Context(), s, "continue"); err == nil {
		t.Fatal("changed membership dispatched old session")
	}
}

func TestMultiProjectExecutionEvidenceDoesNotReachExtractor(t *testing.T) {
	h, runner, judge := experienceHost(t)
	h.Config.Projects = []state.ProjectRef{{ID: "a", Scope: "project:a", Workspace: t.TempDir()}, {ID: "b", Scope: "project:b", Workspace: t.TempDir()}}
	h.Extractor = experienceExtractorFunc(func(context.Context, state.Run, []state.ToolEvidence, func(harness.Observation) error) (experience.Extraction, error) {
		t.Fatal("joint task reached extractor")
		return experience.Extraction{}, nil
	})
	r := runOK(t, h, newSession(t, h), "Compare the two fixture implementations.")
	if r.ExperienceStatus != "skipped" || judge.assessCalls != 0 || len(runner.requests) != 1 {
		t.Fatalf("joint evidence extracted: %+v", r)
	}
	evidence, err := h.Store.ToolEvidence(t.Context(), r.ID)
	if err != nil || len(evidence) == 0 {
		t.Fatalf("raw execution evidence was lost: %+v %v", evidence, err)
	}
}

func TestMultiProjectRecallUsesOneSharedTextBudget(t *testing.T) {
	h, _ := testHost(t)
	neutral := h.Config.Scope
	projects := []state.ProjectRef{{ID: "a", Scope: "project:a", Workspace: t.TempDir()}, {ID: "b", Scope: "project:b", Workspace: t.TempDir()}}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) { return saveStatement(), nil })
	for _, project := range projects {
		h.Config.Scope = project.Scope
		runOK(t, h, newSession(t, h), "我偏好中文回复。"+strings.Repeat("简洁", 1200))
	}
	h.Config.Scope, h.Config.Workspace, h.Config.Projects = neutral, neutral, projects
	r := runOK(t, h, newSession(t, h), "如何回复？")
	bytes := 0
	for _, m := range r.Context {
		bytes += len(m.Text)
	}
	if len(r.Context) != 1 || bytes > 12*1024 {
		t.Fatalf("per-scope budgets exceeded shared bound: records=%d bytes=%d", len(r.Context), bytes)
	}
}

func TestCompletedJointExecutionKeepsMemoryPausedAfterOutputFailure(t *testing.T) {
	h, _ := testHost(t)
	h.Config.Projects = []state.ProjectRef{{ID: "a", Scope: "project:a", Workspace: t.TempDir()}, {ID: "b", Scope: "project:b", Workspace: t.TempDir()}}
	want := errors.New("output unavailable")
	h.Sink = func(e event.Event) error {
		if e.Type == "phase.finished" && e.Phase == "execute" {
			return want
		}
		return nil
	}
	s := newSession(t, h)
	r, err := h.Run(t.Context(), s, "Compare both projects.")
	if !errors.Is(err, want) || r.ExecutionStatus != "completed" || r.MemoryStatus != "skipped" || r.ExperienceStatus != "skipped" {
		t.Fatalf("output failure made joint run eligible for writes: %+v %v", r, err)
	}
	if _, err := h.Session(t.Context(), s.ID); err != nil {
		t.Fatalf("compatible native history was stranded: %v", err)
	}
}

func TestChannelClarificationUsesPersistedSupplementWithoutChangingOriginalInput(t *testing.T) {
	h, runner := testHost(t)
	message := state.ChannelMessage{ID: "request", Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "native-request", ConversationID: "dm", SenderID: "owner", Profile: "profile", Binding: "binding", Scope: h.Config.Scope, MessageType: "text", Text: "Compare the projects.", ReplyExpiresAt: time.Now().Add(time.Hour)}
	enqueue := func(m state.ChannelMessage) {
		t.Helper()
		if inserted, err := h.Store.EnqueueChannelMessage(t.Context(), m, "https://example.invalid/reply"); err != nil || !inserted {
			t.Fatalf("enqueue: %t %v", inserted, err)
		}
		if _, err := h.Store.ClaimNextChannelMessage(t.Context(), m.Profile); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(message)
	if err := h.Store.WaitChannelChoice(t.Context(), message.ID, state.ChannelChoice{ID: "choice", Question: "Which projects?"}); err != nil {
		t.Fatal(err)
	}
	confirmation := message
	confirmation.ID, confirmation.MessageID, confirmation.Text = "confirmation", "native-confirmation", "/confirm choice Compare runtime behavior only."
	enqueue(confirmation)
	if _, err := h.Store.ClaimChannelChoice(t.Context(), "choice", confirmation.ID, "Compare runtime behavior only."); err != nil {
		t.Fatal(err)
	}
	h.Config.Supplements = []state.UserSupplement{{MessageID: "forged", Text: "Never inject this configuration override."}}
	h.Decider = deciderFunc(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		if input != message.Text {
			t.Fatalf("clarification rewrote the original memory evidence: %q", input)
		}
		return memory.Decision{Kind: "none"}, nil
	})
	session := newSession(t, h)
	if err := h.Store.BindChannelSession(t.Context(), message.Binding, session.ID); err != nil {
		t.Fatal(err)
	}
	run, err := h.RunChannel(t.Context(), session, message.Text, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Input != message.Text || len(run.Supplements) != 1 || run.Supplements[0].MessageID != confirmation.MessageID || run.Source == nil || run.Source.MessageID != message.MessageID {
		t.Fatalf("channel evidence changed: %+v", run)
	}
	prompt := runner.requests[0].Prompt
	if !strings.Contains(prompt, "Compare runtime behavior only.") || !strings.Contains(prompt, confirmation.MessageID) || strings.Contains(prompt, "Never inject") {
		t.Fatalf("prompt ignored persisted clarification: %s", prompt)
	}
}
