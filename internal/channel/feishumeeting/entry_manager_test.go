package feishumeeting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type entryClientFunc func(context.Context, ...string) (Output, error)

func (f entryClientFunc) Run(ctx context.Context, args ...string) (Output, error) {
	return f(ctx, args...)
}

func entryConfig() Config { c := fixtureConfig(); c.AutoJoin = true; c.MeetingID = ""; return c }
func entryInvitation(id string) EntryEvent {
	return EntryEvent{ID: id, Kind: EntryInvite, MeetingNumber: "123456789", MeetingID: fixtureConfig().MeetingID, CallID: "call-" + id, SenderOpenID: "ou_alice", At: time.Now().UTC()}
}
func entryManagerJSON(data any) Output {
	raw, _ := json.Marshal(map[string]any{"ok": true, "data": data})
	return Output{Stdout: raw, Started: true}
}
func entryJoinResult() Output {
	return entryManagerJSON(map[string]any{"meeting": map[string]any{"id": fixtureConfig().MeetingID, "meeting_no": "123456789"}})
}
func entryMembershipResult(status any) Output {
	participants := []any{}
	if status != nil {
		participants = append(participants, map[string]any{"id": fixtureConfig().BotOpenID, "status": status, "user_type": 0})
	}
	return entryManagerJSON(map[string]any{"meeting": map[string]any{"id": fixtureConfig().MeetingID, "meeting_no": "123456789", "status": 2, "participants": participants}})
}
func entryHooks() EntryHooks {
	return EntryHooks{StartMeeting: func(context.Context, Config) error { return nil }, StopMeeting: func(string) error { return nil }, Observe: func(string, string, map[string]any) error { return nil }}
}
func entryManager(t *testing.T, client Client, hooks EntryHooks) *EntryManager {
	t.Helper()
	m, err := OpenEntryManager(entryConfig(), filepath.Join(t.TempDir(), "entry.json"), client, hooks)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func entryStatus(t *testing.T, m *EntryManager, key, status string) EntryMeeting {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.state.Meetings[key]
	if !ok || entry.Status != status {
		t.Fatalf("entry %s: %+v, want %s", key, entry, status)
	}
	return entry
}
func receiveEntry(t *testing.T, m *EntryManager, e EntryEvent) {
	t.Helper()
	if err := m.Receive(t.Context(), e); err != nil {
		t.Fatal(err)
	}
}
func processEntry(t *testing.T, m *EntryManager, key string) {
	t.Helper()
	if err := m.process(t.Context(), key); err != nil {
		t.Fatal(err)
	}
}

func TestEntryPersistsBeforeJoiningAndIgnoresUnauthorizedStaleAndRepeatedSources(t *testing.T) {
	var m *EntryManager
	calls, starts := 0, 0
	client := entryClientFunc(func(_ context.Context, args ...string) (Output, error) {
		calls++
		if calls == 1 {
			raw, err := os.ReadFile(m.path)
			if err != nil {
				t.Fatal(err)
			}
			var saved entryState
			if json.Unmarshal(raw, &saved) != nil || saved.Meetings["first"].Status != "joining" {
				t.Fatal("mutation preceded durable joining state")
			}
			want := []string{"--profile", "meeting-lab", "vc", "+meeting-join", "--as", "bot", "--meeting-number", "123456789", "--format", "json", "--call-id", "call-first"}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("join arguments: %v", args)
			}
			return entryJoinResult(), nil
		}
		if !strings.Contains(strings.Join(args, " "), "vc meeting get --as bot") {
			t.Fatal("join was repeated")
		}
		return entryMembershipResult(2), nil
	})
	hooks := entryHooks()
	hooks.StartMeeting = func(ctx context.Context, c Config) error {
		starts++
		if c.MeetingID != fixtureConfig().MeetingID || ctx.Err() != nil {
			t.Fatal("invalid start")
		}
		return nil
	}
	m = entryManager(t, client, hooks)
	e := entryInvitation("first")
	receiveEntry(t, m, e)
	receiveEntry(t, m, e)
	unauthorized := entryInvitation("unauthorized")
	unauthorized.SenderOpenID = "ou_unknown"
	receiveEntry(t, m, unauthorized)
	stale := entryInvitation("stale")
	stale.At = m.opened.Add(-time.Minute)
	receiveEntry(t, m, stale)
	if calls != 0 || len(m.Snapshot().Meetings) != 1 {
		t.Fatal("receipt executed work or admitted an unauthorized entry")
	}
	entryStatus(t, m, "first", "pending")
	processEntry(t, m, "first")
	entryStatus(t, m, "first", "active")
	if calls != 2 || starts != 1 {
		t.Fatalf("calls=%d starts=%d", calls, starts)
	}
	info, err := os.Stat(m.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("entry state not private")
	}
}

func TestEntryActiveFreshInvitationChecksMembershipAndRemovedBotRejoinsOnce(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{
		{Output: entryJoinResult()}, {Output: entryMembershipResult(2)},
		{Output: entryMembershipResult(2)},
		{Output: entryMembershipResult(4)}, {Output: entryJoinResult()}, {Output: entryMembershipResult(2)},
	}}
	starts, stops := 0, 0
	hooks := entryHooks()
	hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
	hooks.StopMeeting = func(string) error { stops++; return nil }
	m := entryManager(t, client, hooks)
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	receiveEntry(t, m, entryInvitation("another"))
	processEntry(t, m, "first")
	if starts != 1 || len(client.calls) != 3 || stops != 0 {
		t.Fatal("present duplicate restarted or rejoined")
	}
	receiveEntry(t, m, entryInvitation("removed"))
	processEntry(t, m, "first")
	if starts != 2 || len(client.calls) != 6 || stops != 1 {
		t.Fatalf("removed bot recovery: starts%d calls%d stops%d", starts, len(client.calls), stops)
	}
	if !strings.Contains(strings.Join(client.calls[4], " "), "--call-id call-removed") {
		t.Fatal("new invitation correlation lost")
	}
}

func TestEntryUnknownJoinDoesNotReplayAndFreshIntentRequiresMembershipEvidence(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{
		{Output: Output{Started: true}, Err: errors.New("private diagnostic")},
		{Output: entryMembershipResult(4)}, {Output: entryJoinResult()}, {Output: entryMembershipResult(2)},
	}}
	m := entryManager(t, client, entryHooks())
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	entryStatus(t, m, "first", Unknown)
	restored, err := OpenEntryManager(entryConfig(), m.path, client, entryHooks())
	if err != nil {
		t.Fatal(err)
	}
	entryStatus(t, restored, "first", Unknown)
	processEntry(t, restored, "first")
	receiveEntry(t, restored, entryInvitation("first"))
	if len(client.calls) != 1 {
		t.Fatal("unknown mutation was implicitly replayed")
	}
	receiveEntry(t, restored, entryInvitation("explicit-new"))
	processEntry(t, restored, "first")
	entryStatus(t, restored, "first", "active")
	if len(client.calls) != 4 || !strings.Contains(strings.Join(client.calls[1], " "), "vc meeting get") {
		t.Fatal("unknown retry skipped membership check")
	}
}

func TestEntryUnknownWithoutLongIDCannotAuthorizeAnotherJoin(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{{Output: Output{Started: true}, Err: errors.New("lost response")}}}
	m := entryManager(t, client, entryHooks())
	e := entryInvitation("first")
	e.MeetingID = ""
	receiveEntry(t, m, e)
	processEntry(t, m, "first")
	e.ID = "fresh"
	receiveEntry(t, m, e)
	processEntry(t, m, "first")
	entryStatus(t, m, "first", Unknown)
	if len(client.calls) != 1 {
		t.Fatal("unknown destination was mutated again")
	}
}

func TestEntryRestartSkipsPendingAndMarksInterruptedMutationUnknown(t *testing.T) {
	m := entryManager(t, &scriptedClient{}, entryHooks())
	receiveEntry(t, m, entryInvitation("first"))
	restored, err := OpenEntryManager(entryConfig(), m.path, &scriptedClient{}, entryHooks())
	if err != nil {
		t.Fatal(err)
	}
	entryStatus(t, restored, "first", "skipped")
	restored.mu.Lock()
	e := restored.state.Meetings["first"]
	e.Status = "joining"
	e.Held = true
	restored.state.Meetings["first"] = e
	err = restored.saveLocked()
	restored.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	again, err := OpenEntryManager(entryConfig(), m.path, &scriptedClient{}, entryHooks())
	if err != nil {
		t.Fatal(err)
	}
	entryStatus(t, again, "first", Unknown)
}

func TestEntryRestoreVerifiesMembershipBeforeStartAndReady(t *testing.T) {
	c := entryConfig()
	c.MeetingID = fixtureConfig().MeetingID
	client := &scriptedClient{results: []scriptedResult{{Output: entryMembershipResult(2)}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var observed []string
	hooks := entryHooks()
	hooks.StartMeeting = func(context.Context, Config) error { observed = append(observed, "baseline-ready"); return nil }
	hooks.Observe = func(kind, status string, _ map[string]any) error {
		observed = append(observed, kind)
		if kind == "meeting.entry.ready" {
			cancel()
		}
		return nil
	}
	m, err := OpenEntryManager(c, filepath.Join(t.TempDir(), "entry.json"), client, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if m.CanReply(c.MeetingID) {
		t.Fatal("unverified restored meeting became ready")
	}
	err = m.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || strings.Contains(strings.Join(client.calls[0], " "), "+meeting-join") {
		t.Fatal("startup mutated membership")
	}
	if !reflect.DeepEqual(observed, []string{"baseline-ready", "meeting.entry.active", "meeting.entry.ready"}) {
		t.Fatalf("readiness order: %v", observed)
	}
	if m.CanReply(c.MeetingID) || m.Snapshot().Ready {
		t.Fatal("stopped manager remains ready")
	}
}

func TestEntryEndCancelsRuntimeImmediatelyAndBlocksLateInvitesAcrossRestart(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}}}
	var meetingCtx context.Context
	stops := 0
	hooks := entryHooks()
	hooks.StartMeeting = func(ctx context.Context, _ Config) error { meetingCtx = ctx; return nil }
	hooks.StopMeeting = func(string) error { stops++; return nil }
	m := entryManager(t, client, hooks)
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	m.running = true
	if !m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("ready meeting cannot reply")
	}
	end := EntryEvent{ID: "ended", Kind: EntryEnd, MeetingID: fixtureConfig().MeetingID, At: time.Now()}
	receiveEntry(t, m, end)
	if m.CanReply(end.MeetingID) || meetingCtx.Err() == nil {
		t.Fatal("end failed to stop replies or cancel execution context")
	}
	if err := m.stop("first"); err != nil {
		t.Fatal(err)
	}
	receiveEntry(t, m, entryInvitation("late"))
	processEntry(t, m, "first")
	entryStatus(t, m, "first", "ended")
	restored, err := OpenEntryManager(entryConfig(), m.path, client, hooks)
	if err != nil {
		t.Fatal(err)
	}
	receiveEntry(t, restored, entryInvitation("later"))
	processEntry(t, restored, "first")
	if len(client.calls) != 2 || stops != 1 {
		t.Fatal("late invitation revived a terminal meeting")
	}
}

func TestEntryEndDuringJoinNeverStartsReceiver(t *testing.T) {
	var m *EntryManager
	starts := 0
	client := entryClientFunc(func(ctx context.Context, args ...string) (Output, error) {
		receiveEntry(t, m, EntryEvent{ID: "end", Kind: EntryEnd, MeetingID: fixtureConfig().MeetingID, At: time.Now()})
		if ctx.Err() == nil {
			t.Fatal("inflight join was not canceled")
		}
		return entryJoinResult(), nil
	})
	hooks := entryHooks()
	hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
	m = entryManager(t, client, hooks)
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	entryStatus(t, m, "first", "ended")
	if starts != 0 {
		t.Fatal("ended meeting started a receiver")
	}
}

func TestEntryPollFailureStopsOnlyMeetingAndFreshInviteCanRecover(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}, {Output: entryMembershipResult(2)}}}
	starts, stops := 0, 0
	var meetingCtx context.Context
	hooks := entryHooks()
	hooks.StartMeeting = func(ctx context.Context, _ Config) error { meetingCtx = ctx; starts++; return nil }
	hooks.StopMeeting = func(string) error { stops++; return nil }
	m := entryManager(t, client, hooks)
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	if err := m.ReportPollFailure(t.Context(), fixtureConfig().MeetingID); err != nil {
		t.Fatal(err)
	}
	entryStatus(t, m, "first", Unknown)
	if meetingCtx.Err() == nil || stops != 0 {
		t.Fatal("failure callback waited for itself or did not cancel")
	}
	if err := m.stop("first"); err != nil {
		t.Fatal(err)
	}
	receiveEntry(t, m, entryInvitation("recover"))
	processEntry(t, m, "first")
	entryStatus(t, m, "first", "active")
	if starts != 2 || len(client.calls) != 3 {
		t.Fatal("present bot recovery performed another join")
	}
}

func TestEntryPollFailureIgnoresSupersededRuntimeLease(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{
		{Output: entryJoinResult()}, {Output: entryMembershipResult(2)},
		{Output: entryMembershipResult(4)}, {Output: entryJoinResult()}, {Output: entryMembershipResult(2)},
	}}
	var leases []context.Context
	hooks := entryHooks()
	hooks.StartMeeting = func(ctx context.Context, _ Config) error { leases = append(leases, ctx); return nil }
	m := entryManager(t, client, hooks)
	m.running = true
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	receiveEntry(t, m, entryInvitation("reinvite"))
	processEntry(t, m, "first")
	if len(leases) != 2 || leases[0] == leases[1] || leases[0].Err() == nil {
		t.Fatal("replacement did not rotate the runtime lease")
	}
	if err := m.ReportPollFailure(t.Context(), fixtureConfig().MeetingID, leases[0]); err != nil {
		t.Fatal(err)
	}
	entryStatus(t, m, "first", "active")
	if leases[1].Err() != nil || !m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("old receiver failure canceled its replacement")
	}
	if err := m.ReportPollFailure(t.Context(), fixtureConfig().MeetingID, leases[1]); err != nil {
		t.Fatal(err)
	}
	entryStatus(t, m, "first", Unknown)
	if leases[1].Err() == nil || m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("current receiver failure did not stop replies")
	}
}

func TestEntryReadyRuntimeKeepsRepliesDuringMembershipRecheck(t *testing.T) {
	var m *EntryManager
	calls, starts := 0, 0
	client := entryClientFunc(func(_ context.Context, _ ...string) (Output, error) {
		calls++
		if calls == 1 {
			return entryJoinResult(), nil
		}
		if calls >= 3 && !m.CanReply(fixtureConfig().MeetingID) {
			t.Fatal("ready runtime lost reply authority during membership read")
		}
		if calls == 4 {
			return Output{Stdout: []byte(`{"ok":false,"error":{"type":"permission"}}`), Started: true}, nil
		}
		return entryMembershipResult(2), nil
	})
	hooks := entryHooks()
	hooks.StartMeeting = func(context.Context, Config) error {
		starts++
		if m.CanReply(fixtureConfig().MeetingID) {
			t.Fatal("first baseline-incomplete runtime can reply")
		}
		return nil
	}
	m = entryManager(t, client, hooks)
	m.running = true
	receiveEntry(t, m, entryInvitation("first"))
	if m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("first pending entry can reply")
	}
	processEntry(t, m, "first")
	receiveEntry(t, m, entryInvitation("present"))
	if !m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("new invitation temporarily disabled an existing ready receiver")
	}
	processEntry(t, m, "first")
	if starts != 1 || !m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("present recheck disturbed the receiver")
	}
	receiveEntry(t, m, entryInvitation("unconfirmed"))
	processEntry(t, m, "first")
	if m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("unconfirmed membership retained reply authority")
	}
}

func TestEntryLifecycleObservationsFollowPersistenceAndPrecedeJoin(t *testing.T) {
	var m *EntryManager
	client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}}}
	stops := 0
	var observations []string
	hooks := entryHooks()
	hooks.StopMeeting = func(string) error { stops++; return nil }
	hooks.Observe = func(kind, status string, data map[string]any) error {
		observations = append(observations, kind+":"+status)
		// Hooks may inspect state; the manager must not hold its mutex here.
		_ = m.Snapshot()
		raw, err := os.ReadFile(m.path)
		if err != nil {
			t.Fatal(err)
		}
		var saved entryState
		if json.Unmarshal(raw, &saved) != nil {
			t.Fatal("observation preceded valid persistence")
		}
		if kind == "meeting.entry.received" && !saved.Seen[data["event_id"].(string)] {
			t.Fatal("receipt observation preceded durable source dedup")
		}
		if kind == "meeting.entry.joining" {
			if saved.Meetings["first"].Status != "joining" || len(client.calls) != 0 || data["event_id"] != "first" || data["meeting_number"] != "123456789" {
				t.Fatal("join observation was not between persistence and mutation")
			}
		}
		if kind == "meeting.entry.ended" && (saved.Meetings["first"].Status != "ended" || saved.Meetings["first"].StopPending || stops != 1 || data["event_id"] != "end") {
			t.Fatal("end observation preceded cleanup or lost its source")
		}
		return nil
	}
	m = entryManager(t, client, hooks)
	e := entryInvitation("first")
	receiveEntry(t, m, e)
	receiveEntry(t, m, e)
	ignored := entryInvitation("unauthorized")
	ignored.SenderOpenID = "ou_unknown"
	receiveEntry(t, m, ignored)
	processEntry(t, m, "first")
	receiveEntry(t, m, EntryEvent{ID: "end", Kind: EntryEnd, MeetingID: fixtureConfig().MeetingID, At: time.Now()})
	if err := m.stop("first"); err != nil {
		t.Fatal(err)
	}
	want := []string{"meeting.entry.received:pending", "meeting.entry.received:duplicate", "meeting.entry.received:ignored", "meeting.entry.joining:joining", "meeting.entry.active:ready", "meeting.entry.received:ended", "meeting.entry.ended:ended"}
	if !reflect.DeepEqual(observations, want) {
		t.Fatalf("lifecycle observations: %v", observations)
	}
}

func TestEntryFreshBareNumberResolvesNewMeetingAfterPreviousMeetingEnded(t *testing.T) {
	newID := "700000000000000002"
	newJoin := entryManagerJSON(map[string]any{"meeting": map[string]any{"id": newID, "meeting_no": "123456789"}, "join_user": map[string]any{"id": "777777777777777777", "user_type": 10}})
	newMembership := entryManagerJSON(map[string]any{"meeting": map[string]any{"id": newID, "meeting_no": "123456789", "status": 2, "participants": []any{map[string]any{"id": fixtureConfig().BotOpenID, "status": 2}}}})
	client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}, {Output: newJoin}, {Output: newMembership}, {Output: newMembership}}}
	m := entryManager(t, client, entryHooks())
	receiveEntry(t, m, entryInvitation("first"))
	processEntry(t, m, "first")
	receiveEntry(t, m, EntryEvent{ID: "end", Kind: EntryEnd, MeetingID: fixtureConfig().MeetingID, At: time.Now()})
	if err := m.stop("first"); err != nil {
		t.Fatal(err)
	}
	receiveEntry(t, m, entryInvitation("late-invite"))
	if len(m.Snapshot().Meetings) != 1 {
		t.Fatal("known ended invitation created a new entry")
	}
	fresh := entryInvitation("new-occurrence")
	fresh.Kind = EntryMessage
	fresh.MeetingID = ""
	fresh.CallID = ""
	receiveEntry(t, m, fresh)
	processEntry(t, m, "new-occurrence")
	entryStatus(t, m, "first", "ended")
	current := entryStatus(t, m, "new-occurrence", "active")
	if current.MeetingID != newID || len(client.calls) != 4 {
		t.Fatal("fresh explicit number failed to resolve a new meeting")
	}
	fresh.ID = "current-occurrence"
	receiveEntry(t, m, fresh)
	processEntry(t, m, "new-occurrence")
	if len(m.Snapshot().Meetings) != 2 || len(client.calls) != 5 {
		t.Fatal("old ended number shadowed its active replacement")
	}
}

func TestEntryAbsentSeedCannotShadowFreshNumberRuntime(t *testing.T) {
	c := entryConfig()
	c.MeetingID = fixtureConfig().MeetingID
	client := &scriptedClient{results: []scriptedResult{
		{Output: entryMembershipResult(4)}, {Output: entryJoinResult()}, {Output: entryMembershipResult(2)}, {Output: entryMembershipResult(2)},
	}}
	starts := 0
	hooks := entryHooks()
	hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
	m, err := OpenEntryManager(c, filepath.Join(t.TempDir(), "entry.json"), client, hooks)
	if err != nil {
		t.Fatal(err)
	}
	m.running = true
	seed := "seed:" + c.MeetingID
	processEntry(t, m, seed)
	entryStatus(t, m, seed, Failed)
	fresh := entryInvitation("fresh-number")
	fresh.Kind = EntryMessage
	fresh.MeetingID = ""
	fresh.CallID = ""
	receiveEntry(t, m, fresh)
	// Before its long ID is learned, a new invitation must find this pending
	// occurrence rather than turn the failed seed into a second queued join.
	receiveEntry(t, m, entryInvitation("invite-while-pending"))
	entryStatus(t, m, seed, Failed)
	entryStatus(t, m, fresh.ID, "pending")
	processEntry(t, m, fresh.ID)
	for i := 0; i < 100; i++ {
		if !m.CanReply(c.MeetingID) {
			t.Fatal("failed seed shadowed the ready runtime")
		}
	}
	receiveEntry(t, m, entryInvitation("duplicate-present"))
	processEntry(t, m, fresh.ID)
	if starts != 1 || len(client.calls) != 4 || !strings.Contains(strings.Join(client.calls[3], " "), "vc meeting get") {
		t.Fatal("duplicate invitation rejoined instead of probing current membership")
	}
	entryStatus(t, m, seed, Failed)
	entryStatus(t, m, fresh.ID, "active")
	if len(m.Snapshot().Meetings) != 2 {
		t.Fatal("old evidence was removed or an extra occurrence was created")
	}
}

func TestEntryReleasedFailedAndSkippedNumbersDoNotCarryOldLongID(t *testing.T) {
	for _, status := range []string{Failed, "skipped"} {
		t.Run(status, func(t *testing.T) {
			m := entryManager(t, &scriptedClient{}, entryHooks())
			receiveEntry(t, m, entryInvitation("old"))
			m.mu.Lock()
			old := m.state.Meetings["old"]
			old.Status, old.Held = status, false
			m.state.Meetings["old"] = old
			err := m.saveLocked()
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			fresh := entryInvitation("new-number")
			fresh.Kind = EntryMessage
			fresh.MeetingID = ""
			fresh.CallID = ""
			receiveEntry(t, m, fresh)
			entry := entryStatus(t, m, fresh.ID, "pending")
			if entry.MeetingID != "" || entry.NeedsCheck {
				t.Fatal("new occurrence inherited the released meeting identity")
			}
			entryStatus(t, m, "old", status)
		})
	}
}

func TestEntryLookupIsDeterministicAndPrefersHeldOverReleasedHistory(t *testing.T) {
	m := entryManager(t, &scriptedClient{}, entryHooks())
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, status := range map[string]string{"z-held": Unknown, "a-held": Unknown, "failed": Failed, "ended": "ended"} {
		m.state.Meetings[key] = EntryMeeting{Key: key, MeetingID: fixtureConfig().MeetingID, MeetingNumber: "123456789", Status: status, Held: status == Unknown}
	}
	for i := 0; i < 100; i++ {
		if got := m.findLocked("123456789", fixtureConfig().MeetingID); got != "a-held" {
			t.Fatalf("unstable or historical lookup: %s", got)
		}
	}
}

func TestEntryPendingIMCoalescesInvitationCorrelationBeforeRun(t *testing.T) {
	for _, inviteFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("invite-first-%t", inviteFirst), func(t *testing.T) {
			client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hooks := entryHooks()
			hooks.Observe = func(kind, status string, _ map[string]any) error {
				if kind == "meeting.entry.active" {
					cancel()
				}
				return nil
			}
			m := entryManager(t, client, hooks)
			im := entryInvitation("im-first")
			im.Kind = EntryMessage
			im.MeetingID = ""
			im.CallID = ""
			invite := entryInvitation("vc-invite")
			invite.SenderOpenID = "ou_bob"
			key := im.ID
			if inviteFirst {
				receiveEntry(t, m, invite)
				receiveEntry(t, m, im)
				key = invite.ID
			} else {
				receiveEntry(t, m, im)
				receiveEntry(t, m, invite)
			}
			entry := entryStatus(t, m, key, "pending")
			if len(m.Snapshot().Meetings) != 1 || entry.MeetingID != invite.MeetingID || entry.CallID != invite.CallID || entry.EventID != invite.ID || entry.SenderOpenID != invite.SenderOpenID {
				t.Fatalf("invitation identity or correlation lost: %+v", entry)
			}
			if err := m.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if len(client.calls) != 2 || !strings.Contains(strings.Join(client.calls[0], " "), "+meeting-join") || !strings.Contains(strings.Join(client.calls[0], " "), "--call-id call-vc-invite") || !strings.Contains(strings.Join(client.calls[1], " "), "--meeting-id "+invite.MeetingID) {
				t.Fatalf("coalesced join/probe: %v", client.calls)
			}
		})
	}
}

func TestEntryPendingInvitationRejectsConflictsWithoutChangingSnapshot(t *testing.T) {
	m := entryManager(t, &scriptedClient{}, entryHooks())
	first := entryInvitation("first")
	receiveEntry(t, m, first)
	before := entryStatus(t, m, first.ID, "pending")
	conflict := first
	conflict.ID = "conflict"
	conflict.MeetingID = "700000000000000099"
	if err := m.Receive(t.Context(), conflict); err == nil {
		t.Fatal("conflicting invitation was coalesced")
	}
	after := entryStatus(t, m, first.ID, "pending")
	if before != after || len(m.Snapshot().Meetings) != 1 || m.state.Seen[conflict.ID] {
		t.Fatal("conflict changed durable pending identity")
	}
}

func TestEntryPendingSameTargetDifferentCallIDKeepsFirstJoinAndAcknowledgesDuplicate(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	duplicateObserved := false
	hooks := entryHooks()
	hooks.Observe = func(kind, status string, data map[string]any) error {
		if kind == "meeting.entry.received" && status == "duplicate" && data["event_id"] == "second" {
			duplicateObserved = true
		}
		if kind == "meeting.entry.active" {
			cancel()
		}
		return nil
	}
	m := entryManager(t, client, hooks)
	first := entryInvitation("first")
	receiveEntry(t, m, first)
	before := entryStatus(t, m, first.ID, "pending")
	second := entryInvitation("second")
	second.SenderOpenID = "ou_bob"
	if err := m.Receive(t.Context(), second); err != nil {
		t.Fatalf("normal repeated invitation would stop the listener: %v", err)
	}
	if before != entryStatus(t, m, first.ID, "pending") || !m.state.Seen[second.ID] || !duplicateObserved {
		t.Fatal("duplicate replaced pending correlation or lost source evidence")
	}
	if err := m.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(client.calls) != 2 || !strings.Contains(strings.Join(client.calls[0], " "), "--call-id call-first") || strings.Contains(strings.Join(client.calls[0], " "), "call-second") {
		t.Fatalf("pending invitation duplicated or replaced join: %v", client.calls)
	}
}

func TestEntryInvitationCannotRewriteJoiningMutationSnapshot(t *testing.T) {
	var m *EntryManager
	im := entryInvitation("im-first")
	im.Kind = EntryMessage
	im.MeetingID = ""
	im.CallID = ""
	calls := 0
	client := entryClientFunc(func(_ context.Context, args ...string) (Output, error) {
		calls++
		if calls == 1 {
			receiveEntry(t, m, entryInvitation("arrived-after-joining"))
			entry := entryStatus(t, m, im.ID, "joining")
			if entry.CallID != "" || entry.MeetingID != "" || entry.EventID != im.ID || strings.Contains(strings.Join(args, " "), "--call-id") {
				t.Fatal("late invitation rewrote an attempted mutation")
			}
			return entryJoinResult(), nil
		}
		return entryMembershipResult(2), nil
	})
	m = entryManager(t, client, entryHooks())
	receiveEntry(t, m, im)
	processEntry(t, m, im.ID)
	if calls != 2 {
		t.Fatal("late invitation duplicated join")
	}
}

func TestEntryJoiningObservationFailurePreventsMutation(t *testing.T) {
	client := &scriptedClient{}
	hooks := entryHooks()
	hooks.Observe = func(kind, status string, _ map[string]any) error {
		if kind == "meeting.entry.joining" {
			return errors.New("observation unavailable")
		}
		return nil
	}
	m := entryManager(t, client, hooks)
	receiveEntry(t, m, entryInvitation("first"))
	if err := m.process(t.Context(), "first"); err == nil {
		t.Fatal("observation failure was hidden")
	}
	if len(client.calls) != 0 {
		t.Fatal("join mutation ran after its observation failed")
	}
}

func TestEntryCapacityCountsPendingAndUnknownAndPreventsPreviouslyRejectedRetry(t *testing.T) {
	m := entryManager(t, &scriptedClient{}, entryHooks())
	for i := 0; i < 5; i++ {
		e := entryInvitation(fmt.Sprint(i))
		e.MeetingID = fmt.Sprintf("70000000000000000%d", i)
		e.MeetingNumber = fmt.Sprintf("12345678%d", i)
		receiveEntry(t, m, e)
	}
	entryStatus(t, m, "4", Failed)
	e := entryInvitation("retry")
	e.MeetingID = "700000000000000004"
	e.MeetingNumber = "123456784"
	receiveEntry(t, m, e)
	entryStatus(t, m, "4", Failed)
	m.mu.Lock()
	entry := m.state.Meetings["0"]
	entry.Status = Unknown
	entry.Held = true
	m.state.Meetings["0"] = entry
	m.mu.Unlock()
	e.ID = "retry-again"
	receiveEntry(t, m, e)
	entryStatus(t, m, "4", Failed)
}

func TestEntryStateIdentityAndPersistenceFailureFailClosed(t *testing.T) {
	m := entryManager(t, &scriptedClient{}, entryHooks())
	receiveEntry(t, m, entryInvitation("first"))
	c := entryConfig()
	c.BotOpenID = "ou_otherbot"
	if _, err := OpenEntryManager(c, m.path, &scriptedClient{}, entryHooks()); err == nil {
		t.Fatal("cross identity state reused")
	}
	bad := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m.path = filepath.Join(bad, "entry.json")
	writeFailure := entryInvitation("write-failure")
	writeFailure.MeetingNumber, writeFailure.MeetingID = "987654321", "700000000000000099"
	if err := m.Receive(t.Context(), writeFailure); err == nil {
		t.Fatal("failed persistence was acknowledged")
	}
	if m.CanReply(fixtureConfig().MeetingID) || m.failure == nil {
		t.Fatal("persistence failure did not close manager")
	}
}

func TestEntryRetentionRejectionCannotBecomeAnUndurableDuplicate(t *testing.T) {
	for _, kind := range []string{"meetings", "ended"} {
		t.Run(kind, func(t *testing.T) {
			m := entryManager(t, &scriptedClient{}, entryHooks())
			for i := 0; i < 1000; i++ {
				key := fmt.Sprint(i)
				if kind == "meetings" {
					m.state.Meetings[key] = EntryMeeting{Key: key, Status: "ended"}
				} else {
					m.state.Ended[key] = true
				}
			}
			e := entryInvitation("not-saved")
			if kind == "ended" {
				e.Kind = EntryEnd
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := m.Receive(t.Context(), e); err == nil {
					t.Fatal("retention rejection was acknowledged on redelivery")
				}
				if m.state.Seen[e.ID] {
					t.Fatal("rejected source was marked durably received")
				}
			}
		})
	}
}

func TestEntryConcurrentEndCancelsBaselineAndDoesNotPublishActive(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{{Output: entryJoinResult()}, {Output: entryMembershipResult(2)}}}
	starting := make(chan struct{})
	hooks := entryHooks()
	hooks.StartMeeting = func(ctx context.Context, _ Config) error {
		close(starting)
		<-ctx.Done()
		return ctx.Err()
	}
	m := entryManager(t, client, hooks)
	receiveEntry(t, m, entryInvitation("first"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	select {
	case <-starting:
	case <-time.After(3 * time.Second):
		t.Fatal("baseline did not start")
	}
	if m.CanReply(fixtureConfig().MeetingID) {
		t.Fatal("baseline-incomplete runtime can reply")
	}
	receiveEntry(t, m, EntryEvent{ID: "end", Kind: EntryEnd, MeetingID: fixtureConfig().MeetingID, At: time.Now()})
	entryStatus(t, m, "first", "ended")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("baseline cancellation was not joined")
	}
	if m.CanReply(fixtureConfig().MeetingID) || len(client.calls) != 2 {
		t.Fatal("ended baseline became active or repeated mutation")
	}
}

func TestEntryRestoreAbsenceAndUnknownDoNotRejoin(t *testing.T) {
	for name, output := range map[string]Output{
		"absent":  entryMembershipResult(4),
		"unknown": {Stdout: []byte(`{"ok":false,"error":{"type":"permission"}}`), Started: true},
	} {
		t.Run(name, func(t *testing.T) {
			c := entryConfig()
			c.MeetingID = fixtureConfig().MeetingID
			client := &scriptedClient{results: []scriptedResult{{Output: output}}}
			starts := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hooks := entryHooks()
			hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
			hooks.Observe = func(kind, _ string, _ map[string]any) error {
				if kind == "meeting.entry.ready" {
					cancel()
				}
				return nil
			}
			m, err := OpenEntryManager(c, filepath.Join(t.TempDir(), "entry.json"), client, hooks)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if starts != 0 || len(client.calls) != 1 || m.CanReply(c.MeetingID) {
				t.Fatal("unverified restore joined or published readiness")
			}
			want := Failed
			if name == "unknown" {
				want = Unknown
			}
			entryStatus(t, m, "seed:"+c.MeetingID, want)
		})
	}
}

func TestEntryConfiguredSeedRechecksPriorAbsenceWithoutJoining(t *testing.T) {
	for _, status := range []int{2, 4} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			config := entryConfig()
			config.MeetingID = fixtureConfig().MeetingID
			path := filepath.Join(t.TempDir(), "entry.json")
			firstClient := &scriptedClient{results: []scriptedResult{{Output: entryMembershipResult(4)}}}
			first, err := OpenEntryManager(config, path, firstClient, entryHooks())
			if err != nil {
				t.Fatal(err)
			}
			key := "seed:" + config.MeetingID
			processEntry(t, first, key)
			entryStatus(t, first, key, Failed)
			starts := 0
			hooks := entryHooks()
			hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
			client := &scriptedClient{results: []scriptedResult{{Output: entryMembershipResult(status)}}}
			restored, err := OpenEntryManager(config, path, client, hooks)
			if err != nil {
				t.Fatal(err)
			}
			entryStatus(t, restored, key, "verifying")
			processEntry(t, restored, key)
			wantStatus, wantStarts := Failed, 0
			if status == 2 {
				wantStatus, wantStarts = "active", 1
			}
			entryStatus(t, restored, key, wantStatus)
			if starts != wantStarts || len(client.calls) != 1 || !strings.Contains(strings.Join(client.calls[0], " "), "vc meeting get --as bot") {
				t.Fatal("seed restoration repeated a mutation or skipped membership proof")
			}
		})
	}
}

func TestEntryRestoredSeedCanRejoinAfterExitDespiteHistoricalUnknownNumber(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		initialStatus int
		pollFailed    bool
	}{
		{"present then exit", 2, false},
		{"present then poll failure", 2, true},
		{"absent at restart", 4, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			config := entryConfig()
			config.MeetingID = fixtureConfig().MeetingID
			path := filepath.Join(t.TempDir(), "entry.json")
			firstClient := &scriptedClient{results: []scriptedResult{
				{Output: entryMembershipResult(4)},
				{Output: Output{Started: true}, Err: errors.New("lost join acknowledgement")},
			}}
			first, err := OpenEntryManager(config, path, firstClient, entryHooks())
			if err != nil {
				t.Fatal(err)
			}
			seed := "seed:" + config.MeetingID
			processEntry(t, first, seed)
			request := EntryEvent{ID: "first-number", Kind: EntryMessage, MeetingNumber: "123456789", SenderOpenID: "ou_alice", At: time.Now()}
			receiveEntry(t, first, request)
			processEntry(t, first, request.ID)
			entryStatus(t, first, request.ID, Unknown)
			client := &scriptedClient{results: []scriptedResult{
				{Output: entryMembershipResult(scenario.initialStatus)},
				{Output: entryMembershipResult(4)},
				{Output: entryJoinResult()},
				{Output: entryMembershipResult(2)},
				{Output: entryMembershipResult(2)},
			}}
			starts, stops := 0, 0
			hooks := entryHooks()
			hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
			hooks.StopMeeting = func(string) error { stops++; return nil }
			restored, err := OpenEntryManager(config, path, client, hooks)
			if err != nil {
				t.Fatal(err)
			}
			processEntry(t, restored, seed)
			initialState, initialStarts := Failed, 0
			if scenario.initialStatus == 2 {
				initialState, initialStarts = "active", 1
			}
			current := entryStatus(t, restored, seed, initialState)
			if current.MeetingNumber != request.MeetingNumber || len(client.calls) != 1 || starts != initialStarts {
				t.Fatal("read-only restoration failed to bind the verified meeting number")
			}
			if scenario.pollFailed {
				if err := restored.ReportPollFailure(t.Context(), config.MeetingID); err != nil {
					t.Fatal(err)
				}
				if err := restored.stop(seed); err != nil {
					t.Fatal(err)
				}
			}
			wantStops := stops + 1
			request.ID, request.At = "invited-after-exit", time.Now()
			receiveEntry(t, restored, request)
			processEntry(t, restored, seed)
			entryStatus(t, restored, seed, "active")
			if len(client.calls) != 4 || starts != initialStarts+1 || stops != wantStops || !strings.Contains(strings.Join(client.calls[2], " "), "vc +meeting-join --as bot") {
				t.Fatalf("fresh invitation after exit did not rejoin once: calls=%v starts=%d stops=%d", client.calls, starts, stops)
			}
			request.ID, request.At = "invited-while-present", time.Now()
			receiveEntry(t, restored, request)
			processEntry(t, restored, seed)
			if len(client.calls) != 5 || starts != initialStarts+1 || stops != wantStops {
				t.Fatal("fresh invitation while present repeated the join or receiver")
			}
			entryStatus(t, restored, "first-number", Unknown)
		})
	}
}

func TestMembershipNeverTreatsMissingOrAmbiguousResponseAsAbsence(t *testing.T) {
	for name, output := range map[string]Output{
		"missing participants": entryManagerJSON(map[string]any{"meeting": map[string]any{"id": fixtureConfig().MeetingID, "status": 2}}),
		"wrong meeting":        entryManagerJSON(map[string]any{"meeting": map[string]any{"id": "700000000000000099", "status": 2, "participants": []any{}}}),
		"calling":              entryMembershipResult(1),
		"ringing":              entryMembershipResult(3),
		"permission":           {Stdout: []byte(`{"ok":false,"error":{"type":"permission"}}`), Started: true},
	} {
		t.Run(name, func(t *testing.T) {
			client := &scriptedClient{results: []scriptedResult{{Output: output}}}
			status, _ := meetingMembership(t.Context(), entryConfig(), client, fixtureConfig().MeetingID)
			if status != Unknown {
				t.Fatalf("unproven absence: %s", status)
			}
		})
	}
}

func TestJoinRejectsMismatchedOrMissingDestination(t *testing.T) {
	for name, data := range map[string]any{
		"wrong number": map[string]any{"meeting": map[string]any{"id": fixtureConfig().MeetingID, "meeting_no": "987654321"}},
		"wrong id":     map[string]any{"meeting": map[string]any{"id": "700000000000000099"}},
		"missing id":   map[string]any{"meeting": map[string]any{"meeting_no": "123456789"}},
	} {
		t.Run(name, func(t *testing.T) {
			client := &scriptedClient{results: []scriptedResult{{Output: entryManagerJSON(data)}}}
			id, status := joinMeeting(t.Context(), entryConfig(), client, EntryMeeting{MeetingID: fixtureConfig().MeetingID, MeetingNumber: "123456789"})
			if id != "" || status != Unknown {
				t.Fatal("invalid join acknowledged")
			}
		})
	}
}

func TestEntryJoinAcknowledgementRequiresIndependentBotMembership(t *testing.T) {
	for userName, joinUser := range map[string]any{
		"omitted":   nil,
		"type zero": map[string]any{"id": "777777777777777777", "user_type": 0},
		"open id":   map[string]any{"id": fixtureConfig().BotOpenID},
	} {
		for proofName, proof := range map[string]Output{
			"present":   entryMembershipResult(2),
			"absent":    entryMembershipResult(4),
			"other bot": entryManagerJSON(map[string]any{"meeting": map[string]any{"id": fixtureConfig().MeetingID, "status": 2, "participants": []any{map[string]any{"id": "ou_other_bot", "status": 2}}}}),
		} {
			t.Run(userName+"/"+proofName, func(t *testing.T) {
				data := map[string]any{"meeting": map[string]any{"id": fixtureConfig().MeetingID}}
				if joinUser != nil {
					data["join_user"] = joinUser
				}
				client := &scriptedClient{results: []scriptedResult{{Output: entryManagerJSON(data)}, {Output: proof}}}
				starts := 0
				hooks := entryHooks()
				hooks.StartMeeting = func(context.Context, Config) error { starts++; return nil }
				m := entryManager(t, client, hooks)
				receiveEntry(t, m, entryInvitation("first"))
				processEntry(t, m, "first")
				wantStatus, wantStarts := Unknown, 0
				if proofName == "present" {
					wantStatus, wantStarts = "active", 1
				}
				entryStatus(t, m, "first", wantStatus)
				if starts != wantStarts || len(client.calls) != 2 || !strings.Contains(strings.Join(client.calls[1], " "), "vc meeting get --as bot") {
					t.Fatalf("join-user fields bypassed membership proof: starts=%d calls=%v", starts, client.calls)
				}
			})
		}
	}
}

func TestJoinCLIAuthorizationRejectionIsFailedWithoutAnotherAttempt(t *testing.T) {
	client := &scriptedClient{results: []scriptedResult{{Output: Output{Stdout: []byte(`{"ok":false,"error":{"type":"authorization","subtype":"missing_scope"}}`), Started: true}}}}
	id, status := joinMeeting(t.Context(), entryConfig(), client, EntryMeeting{MeetingNumber: "123456789"})
	if id != "" || status != Failed || len(client.calls) != 1 {
		t.Fatal("an explicit CLI authorization rejection was uncertain or retried")
	}
}
