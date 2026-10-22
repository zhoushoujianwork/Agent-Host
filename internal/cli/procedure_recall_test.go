package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func TestProcedureHintsParticipateInSkillPreselectionWithinConversation(t *testing.T) {
	f := seedReviewFixture(t)
	s, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	m := state.ChannelMessage{ID: "hint-message", Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "hint-native", ConversationID: "owner-dm", SenderID: "owner", Profile: "fixture", Binding: "owner-conversation", Scope: f.workspace, MessageType: "text", Text: "Check fixture tests", ReplyExpiresAt: time.Now().Add(time.Hour)}
	if _, err := s.EnqueueChannelMessage(ctx, m, "https://example.invalid/reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextChannelMessage(ctx, m.Profile); err != nil {
		t.Fatal(err)
	}
	session := state.Session{ID: "hint-session", Scope: f.workspace, Workspace: f.workspace, RuntimeFingerprint: "fixture", NativeSessionID: "hint-native", Model: "fixture"}
	if err := s.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := s.BindChannelSession(ctx, "hint-route", session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveChannelRoute(ctx, m.ID, "hint-route", "", f.workspace, f.workspace, "fixture"); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "hint-run", SessionID: session.ID, Scope: f.workspace, Workspace: f.workspace, RuntimeFingerprint: "fixture", NativeSessionID: session.NativeSessionID, Input: m.Text, ExecutionStatus: "running", MemoryStatus: "blocked"}
	if err := s.CreateChannelRun(ctx, run, m.ID); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []harness.ToolObservation{
		{ID: "test-call", Name: "Bash", Stage: "call", Input: json.RawMessage(`{"command":"go test ./..."}`)},
		{ID: "test-call", Name: "Bash", Stage: "result", Result: json.RawMessage(`"ok fixture"`)},
	} {
		e := event.Event{RunID: run.ID, Phase: "execute", Type: "tool.activity", Data: map[string]any{"tool": tool}}
		if err := s.AppendEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CompleteRun(ctx, run.ID, "completed", "Test report passed", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeMemory(ctx, run.ID, nil, json.RawMessage(`{"kind":"none"}`)); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCandidate(ctx, "accepted-candidate")
	if err != nil {
		t.Fatal(err)
	}
	c.ID, c.RunID, c.MemoryID, c.Status = "hint-candidate", run.ID, "", "pending"
	c.ReviewedAt, c.ReviewNote = nil, ""
	if err := s.SaveCandidates(ctx, run.ID, []state.Candidate{c}, json.RawMessage(`{"reason":"test"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptCandidate(ctx, c.ID, state.CandidateAcceptance{MemoryID: "unused-duplicate", Note: "Same observed procedure", DuplicateIDs: []string{f.memoryID}, ExpectedRevisions: map[string]int{f.memoryID: 1}, Decision: json.RawMessage(`{"reason":"duplicate"}`)}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateFileMemory(ctx, filepath.Join(root, "memory"), filepath.Join(root, "cache", "memory"), "hint-test"); err != nil {
		t.Fatal(err)
	}
	o := options{home: f.home, scope: f.workspace, agentID: "default", audienceID: "owner", retrievalQuery: "操作吧", recallBinding: m.Binding}
	if err := refreshFileRuntime(ctx, s, &o); err != nil {
		t.Fatal(err)
	}
	if len(o.procedureRecallHints) != 1 || len(o.managedMemories) != 1 || o.managedFingerprint == "" || o.skillsPreselected {
		t.Fatal("same-conversation procedure absent from preselected native Skill set", o.procedureRecallHints, o.managedFingerprint)
	}
	o.recallBinding = "another-conversation"
	if err := refreshFileRuntime(ctx, s, &o); err != nil || len(o.procedureRecallHints) != 0 || len(o.managedMemories) != 1 {
		t.Fatal("recall hints crossed conversations or full catalog disappeared", err)
	}
	o.recallBinding = m.Binding
	if err := s.InvalidateMemory(ctx, f.memoryID, "withdraw"); err != nil {
		t.Fatal(err)
	}
	if err := refreshFileRuntime(ctx, s, &o); err != nil || len(o.procedureRecallHints) != 0 || len(o.managedMemories) != 0 {
		t.Fatal("inactive procedure affected native session selection", err)
	}
}
