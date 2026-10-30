//go:build unix

package offloadchannel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/memory"
	"agent-host/internal/runcompletion"
	"agent-host/internal/state"
)

type decisionFunc func(context.Context, string, []state.Memory) (memory.Decision, error)

func (f decisionFunc) Decide(ctx context.Context, input string, existing []state.Memory) (memory.Decision, error) {
	return f(ctx, input, existing)
}

func statementDecider(_ context.Context, input string, existing []state.Memory) (memory.Decision, error) {
	d := memory.Decision{Kind: "project_fact", Store: strings.HasPrefix(input, "The release code is ")}
	if !d.Store {
		d.Kind = "none"
		return d, nil
	}
	for _, m := range existing {
		if m.Text == input {
			d.Store = false
			d.DuplicateIDs = append(d.DuplicateIDs, m.ID)
		} else if strings.HasPrefix(m.Text, "The release code is ") {
			d.ConflictIDs = append(d.ConflictIDs, m.ID)
		}
	}
	return d, nil
}

func freshMemorySession(t *testing.T, f *fixture) {
	t.Helper()
	s, err := f.host.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.BindChannelSession(t.Context(), "binding-"+f.host.Config.AudienceID, s.ID); err != nil {
		t.Fatal(err)
	}
}

func memoryItems(t *testing.T, f *fixture) []state.Memory {
	t.Helper()
	items, err := f.host.store().Memories(t.Context(), f.host.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestMemoryPluginCrossSessionRecallRetainsSourceAndInvalidation(t *testing.T) {
	f := newFixture(t)
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(statementDecider)}
	first, err := f.turn(t.Context(), "remember", "The release code is ACORN-482.")
	if err != nil || first.MemoryStatus != "saved" || first.ExperienceStatus != "skipped" || first.AcceptanceStatus != "unverified" {
		t.Fatalf("%+v %v", first, err)
	}
	items := memoryItems(t, f)
	if len(items) != 1 || items[0].SourceRunID != first.ID || items[0].SenderID != "owner" || items[0].SourceType != "user_statement" {
		t.Fatalf("provenance: %+v", items)
	}
	freshMemorySession(t, f)
	second, err := f.turn(t.Context(), "recall", "What is the release code?")
	if err != nil || second.SessionID == first.SessionID || second.NativeSessionID == first.NativeSessionID {
		t.Fatalf("not a fresh native session: %+v %v", second, err)
	}
	if len(second.Context) != 1 || second.Context[0].ID != items[0].ID {
		t.Fatalf("missing sourced recall: %+v", second.Context)
	}
	stdin := f.read("stdin.2")
	if !strings.Contains(stdin, "ACORN-482") || !strings.Contains(stdin, first.ID) || !strings.Contains(stdin, "source data") || strings.Contains("\n"+f.read("argv.2"), "\n-r\n") {
		t.Fatalf("recall did not reach fresh prompt: %s", stdin)
	}
	if err := f.store.InvalidateMemory(t.Context(), items[0].ID, "user withdrew the code"); err != nil {
		t.Fatal(err)
	}
	freshMemorySession(t, f)
	third, err := f.turn(t.Context(), "after-delete", "What is the release code?")
	if err != nil || len(third.Context) != 0 || strings.Contains(f.read("stdin.3"), "ACORN-482") {
		t.Fatalf("invalidated memory recalled: %+v %v", third, err)
	}
}

func TestMemoryPluginDeliversBeforeProcessingAndFailureDoesNotReplay(t *testing.T) {
	f := newFixture(t)
	delivered := false
	broken := true
	decisions := 0
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(func(ctx context.Context, input string, existing []state.Memory) (memory.Decision, error) {
		decisions++
		if !delivered {
			t.Error("memory processing delayed answer delivery")
		}
		if broken {
			return memory.Decision{}, errors.New("fixture memory failure")
		}
		return statementDecider(ctx, input, existing)
	})}
	ctx := runcompletion.With(t.Context(), func(_ context.Context, r state.Run) error {
		if r.ExecutionStatus != "completed" || r.MemoryStatus != "pending" || r.Output != "answer 1" {
			t.Errorf("bad ready snapshot: %+v", r)
		}
		delivered = true
		return nil
	})
	run, err := f.turn(ctx, "failed-memory", "The release code is ACORN-482.")
	if err == nil || run.ExecutionStatus != "completed" || run.Output != "answer 1" || run.MemoryStatus != "failed" || !delivered {
		t.Fatalf("lost execution outcome: %+v %v", run, err)
	}
	if _, err = f.host.Session(t.Context(), run.SessionID); err != nil {
		t.Fatalf("memory failure blocked native continuation: %v", err)
	}
	broken = false
	retried, err := f.host.RetryMemory(t.Context(), run.ID)
	if err != nil || retried.MemoryStatus != "saved" || f.calls() != 1 || decisions != 2 {
		t.Fatalf("retry replayed native execution: %+v %v calls=%d decisions=%d", retried, err, f.calls(), decisions)
	}
	if _, err = f.host.RetryMemory(t.Context(), run.ID); err != nil || decisions != 2 || len(memoryItems(t, f)) != 1 {
		t.Fatalf("retry duplicated publication: %v", err)
	}
}

func TestMemoryPluginDuplicateConflictAndDisabledMemoryAreNotPromoted(t *testing.T) {
	f := newFixture(t)
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(statementDecider)}
	for i, id := range []string{"first", "same"} {
		r, err := f.turn(t.Context(), id, "The release code is ACORN-482.")
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && r.MemoryStatus != "unchanged" {
			t.Fatalf("duplicate status: %+v", r)
		}
	}
	if len(memoryItems(t, f)) != 1 {
		t.Fatal("duplicate memory created")
	}
	_, err := f.turn(t.Context(), "conflict", "The release code is BIRCH-911.")
	if err != nil {
		t.Fatal(err)
	}
	items := memoryItems(t, f)
	if len(items) != 2 || items[0].Status != "conflicted" || items[1].Status != "conflicted" {
		t.Fatalf("conflicts not quarantined: %+v", items)
	}
	freshMemorySession(t, f)
	r, err := f.turn(t.Context(), "ask", "What is the release code?")
	if err != nil || len(r.Context) != 0 {
		t.Fatalf("conflict recalled: %+v %v", r.Context, err)
	}
	if err = f.store.InvalidateMemory(t.Context(), items[0].ID, "withdrawn"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.turn(t.Context(), "reworded", "The release code is ACORN-482."); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := f.store.Memory(t.Context(), items[0].ID)
	if err != nil || withdrawn.Status != "invalidated" {
		t.Fatalf("withdrawn memory resurrected: %+v %v", withdrawn, err)
	}
}

func TestMemoryPluginAudienceIsolationReadOnlyAndQuotedSource(t *testing.T) {
	f := newFixture(t)
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(statementDecider)}
	if _, err := f.turn(t.Context(), "owner-fact", "The release code is PRIVATE-482."); err != nil {
		t.Fatal(err)
	}
	f.host.Config.AudienceID = "group-a"
	f.host.Memory = &MemoryPlugin{} // retrieval alone never invokes a classifier.
	m := f.enqueueAs("group-ask", "What is the release code?", nil, nil, "2", "member")
	s, err := f.session(m)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.host.RunChannel(t.Context(), s, m.Text, m.ID)
	if err != nil || len(r.Context) != 0 || r.MemoryStatus != "skipped" || r.OffloadMemoryMode != state.OffloadMemoryRecall || strings.Contains(f.read("stdin.2"), "PRIVATE-482") {
		t.Fatalf("audience leak: %+v %v", r, err)
	}
	if _, err = f.host.Session(t.Context(), r.SessionID); err != nil {
		t.Fatal(err)
	}
	f.host.Config.AudienceID = "owner"
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		t.Error("quoted input was promoted to a user declaration")
		return memory.Decision{}, nil
	})}
	quote := &state.ChannelInput{Quote: &state.ChannelQuote{Body: "The release code is SECRET-991."}}
	m = f.enqueue("quote", "The release code is QUOTED-991.", quote, nil)
	s, err = f.session(m)
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.host.RunChannel(t.Context(), s, m.Text, m.ID)
	if err != nil || r.MemoryStatus != "unchanged" || len(memoryItems(t, f)) != 1 {
		t.Fatalf("quoted source saved: %+v %v", r, err)
	}
}

func TestMemoryPluginCancellationAndStaleSnapshotPreserveAnswer(t *testing.T) {
	for _, kind := range []string{"cancel", "snapshot"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.host.Memory = &MemoryPlugin{Timeout: time.Second, Decider: decisionFunc(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
				if kind == "cancel" {
					cancel()
					return memory.Decision{}, context.Canceled
				}
				// A separate completed source publishes while this comparison is running.
				other := state.Run{ID: "other", SessionID: "other-session", Scope: f.host.Config.Scope, Input: "The release code is OTHER-123.", NativeSessionID: "other-native"}
				if err := f.store.CreateSession(t.Context(), state.Session{ID: other.SessionID, Scope: other.Scope, NativeSessionID: other.NativeSessionID}); err != nil {
					t.Fatal(err)
				}
				if err := f.store.CreateRun(t.Context(), other); err != nil {
					t.Fatal(err)
				}
				if err := f.store.CompleteRun(t.Context(), other.ID, "completed", "ok", "m", ""); err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinalizeMemory(t.Context(), other.ID, []state.MemoryUpdate{{Memory: state.Memory{ID: "other-memory", Scope: other.Scope, Kind: "project_fact", Text: other.Input, SourceRunID: other.ID}}}, []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
				return memory.Decision{Store: true, Kind: "project_fact"}, nil
			})}
			r, err := f.turn(ctx, "source", "The release code is ACORN-482.")
			if err == nil || r.ExecutionStatus != "completed" || r.Output == "" || r.MemoryStatus != "failed" || f.calls() != 1 {
				t.Fatalf("bad isolated failure: %+v %v", r, err)
			}
			for _, m := range memoryItems(t, f) {
				if m.SourceRunID == r.ID {
					t.Fatal("canceled/stale decision published")
				}
			}
		})
	}
}

func TestMemoryPluginFileBackendPublishesAndRecalls(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.MigrateFileMemory(t.Context(), t.TempDir(), t.TempDir(), "offload-memory-fixture"); err != nil {
		t.Fatal(err)
	}
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(statementDecider)}
	first, err := f.turn(t.Context(), "file-source", "The release code is ACORN-482.")
	if err != nil || first.MemoryStatus != "saved" {
		t.Fatalf("file publication: %+v %v", first, err)
	}
	freshMemorySession(t, f)
	second, err := f.turn(t.Context(), "file-recall", "What is the release code?")
	if err != nil || len(second.Context) != 1 || second.Context[0].SourceRunID != first.ID {
		t.Fatalf("file recall: %+v %v", second, err)
	}
	if _, err = f.store.VerifyFileMemory(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryPluginGroupPreferencesKeepTheirAuthorAndAgent(t *testing.T) {
	f := newFixture(t)
	f.host.Config.AudienceID = "group-a"
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Store: true, Kind: "preference"}, nil
	})}
	m := f.enqueueAs("alice", "I prefer concise replies.", nil, nil, "2", "alice")
	s, err := f.session(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.RunChannel(t.Context(), s, m.Text, m.ID); err != nil {
		t.Fatal(err)
	}
	f.host.Memory = &MemoryPlugin{}
	for _, tc := range []struct {
		id, sender, agent string
		want              int
	}{
		{"bob", "bob", "default", 0}, {"alice-again", "alice", "default", 1}, {"another-agent", "alice", "another", 0},
	} {
		f.host.Config.AgentID = tc.agent
		freshMemorySession(t, f)
		m = f.enqueueAs(tc.id, "How should I format replies?", nil, nil, "2", tc.sender)
		s, err = f.session(m)
		if err != nil {
			t.Fatal(err)
		}
		r, err := f.host.RunChannel(t.Context(), s, m.Text, m.ID)
		if err != nil || len(r.Context) != tc.want {
			t.Fatalf("%s: %+v %v", tc.id, r.Context, err)
		}
	}
}

func TestMemoryPluginDirectOwnerChatRetainsLocalSourceAndRecall(t *testing.T) {
	f := newFixture(t)
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(statementDecider)}
	s, err := f.host.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.host.Run(t.Context(), s, "The release code is ACORN-482.")
	if err != nil || first.MemoryStatus != "saved" || first.Source != nil {
		t.Fatalf("local publication: %+v %v", first, err)
	}
	items := memoryItems(t, f)
	if len(items) != 1 || items[0].SourceRunID != first.ID || items[0].SenderID != "" {
		t.Fatal("local source attribution changed")
	}
	fresh, err := f.host.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.host.Run(t.Context(), fresh, "What is the release code?")
	if err != nil || len(second.Context) != 1 || second.Context[0].SourceRunID != first.ID || !strings.Contains(f.read("stdin.2"), "ACORN-482") {
		t.Fatalf("local recall: %+v %v", second, err)
	}
}

func TestDirectOwnerChatCannotAdoptChannelSession(t *testing.T) {
	f := newFixture(t)
	first, err := f.turn(t.Context(), "channel", "hello")
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.host.Session(t.Context(), first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Run(t.Context(), s, "take over"); err == nil || f.calls() != 1 {
		t.Fatal("channel session adopted or replayed")
	}
}

func TestChannelCannotAdoptDirectOwnerChatSession(t *testing.T) {
	f := newFixture(t)
	s, err := f.host.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Run(t.Context(), s, "local input"); err != nil {
		t.Fatal(err)
	}
	m := f.enqueue("channel", "channel input", nil, nil)
	if _, err = f.host.RunChannel(t.Context(), s, m.Text, m.ID); err == nil || f.calls() != 1 {
		t.Fatal("local session adopted by channel")
	}
}
