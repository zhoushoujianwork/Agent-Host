package host

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type roomRunner struct {
	started chan harness.Request
	finish  map[string]chan struct{}
}

func (r *roomRunner) Run(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.started <- req
	select {
	case <-ctx.Done():
		return harness.Result{Status: "canceled"}, ctx.Err()
	case <-r.finish[req.NativeSessionID]:
	}
	return harness.Result{Status: "completed", Text: "reply:" + req.NativeSessionID, Model: "fixture", NativeSessionID: req.NativeSessionID}, nil
}

func TestRoomConcurrentMessagesFreezeContextAndPreserveBothReplies(t *testing.T) {
	for _, sender := range []string{"alice", "bob"} {
		t.Run(sender, func(t *testing.T) {
			h, _ := testHost(t)
			seed, err := h.RoomSession(t.Context(), state.RoomInput{Key: "group", RequestID: "seed", SenderID: "alice", Text: "seed request"})
			if err != nil {
				t.Fatal(err)
			}
			runOK(t, h, seed, "seed request")
			if _, err = h.Session(t.Context(), seed.ID); err == nil {
				t.Fatal("sealed native parent was resumable")
			}
			a, err := h.RoomSession(t.Context(), state.RoomInput{Key: "group", RequestID: "a", SenderID: "alice", SenderName: "Alice", Text: "A request"})
			if err != nil {
				t.Fatal(err)
			}
			b, err := h.RoomSession(t.Context(), state.RoomInput{Key: "group", RequestID: "b", SenderID: sender, Text: "B request", ReplyToID: "seed"})
			if err != nil {
				t.Fatal(err)
			}
			if a.NativeSessionID == b.NativeSessionID || a.Room.ForkFromNativeSessionID != seed.NativeSessionID || b.Room.ForkFromNativeSessionID != seed.NativeSessionID {
				t.Fatal("children did not fork the same sealed parent")
			}
			runner := &roomRunner{started: make(chan harness.Request, 2), finish: map[string]chan struct{}{a.NativeSessionID: make(chan struct{}), b.NativeSessionID: make(chan struct{})}}
			h.Runner = runner
			errors := make(chan error, 2)
			for _, s := range []state.Session{a, b} {
				go func() { _, err := h.Run(t.Context(), s, s.Room.Text); errors <- err }()
			}
			for range 2 {
				select {
				case req := <-runner.started:
					if req.Resume || req.ForkFromNativeSessionID != seed.NativeSessionID || !strings.Contains(req.Prompt, `"sender_id":"`+sender+`"`) && strings.Contains(req.Prompt, "B request") {
						t.Errorf("invalid fork request: %+v", req)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("same-room messages were serialized")
				}
			}
			close(runner.finish[b.NativeSessionID])
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
			close(runner.finish[a.NativeSessionID])
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
			later, err := h.RoomSession(t.Context(), state.RoomInput{Key: "group", RequestID: "later", SenderID: "alice", Text: "what did both say?"})
			if err != nil {
				t.Fatal(err)
			}
			if len(later.Room.History) != 3 {
				t.Fatalf("history=%+v", later.Room.History)
			}
			if later.Room.ParentSessionID != b.ID {
				t.Fatal("last finisher replaced the later receipt's checkpoint")
			}
			prompt := roomPrompt(later.Room)
			if !strings.Contains(prompt, "reply:"+a.NativeSessionID) || strings.Contains(prompt, "reply:"+b.NativeSessionID) {
				t.Fatal("missing sibling context or unnecessarily repeated inherited reply", prompt)
			}
			for _, entry := range later.Room.History {
				if entry.Status != "completed" || entry.Reply == "" {
					t.Fatalf("lost sibling reply: %+v", entry)
				}
			}
			frozen, err := h.Store.Session(t.Context(), b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(frozen.Room.History) != 2 || frozen.Room.History[1].Reply != "" {
				t.Fatal("an existing snapshot was mutated by completion")
			}
			missing := b.Room.RoomInput
			missing.Key = ""
			duplicate, err := h.RoomSession(t.Context(), missing)
			_ = duplicate
			// The routing key is intentionally omitted from durable JSON; retry via
			// the authenticated entry rather than trusting a saved routing claim.
			if err == nil {
				t.Fatal("missing room entry accepted")
			}
			dup, err := h.RoomSession(t.Context(), state.RoomInput{Key: "group", RequestID: "b", SenderID: sender, Text: "B request", ReplyToID: "seed"})
			if err != nil {
				t.Fatal(err)
			}
			if dup.ID != b.ID {
				t.Fatal("duplicate created another execution")
			}
			if _, err = h.Run(t.Context(), dup, "B request"); err == nil {
				t.Fatal("duplicate replayed a completed native turn")
			}
			for _, key := range []string{"other-group", "private-alice"} {
				s, err := h.RoomSession(t.Context(), state.RoomInput{Key: key, RequestID: "later", SenderID: "alice", Text: "new room"})
				if err != nil || len(s.Room.History) != 0 || s.Room.ForkFromNativeSessionID != "" {
					t.Fatalf("room leaked: %s %+v %v", key, s, err)
				}
			}
			if err = h.Store.ResetRoom(t.Context(), "group"); err != nil {
				t.Fatal(err)
			}
			fresh, err := h.RoomSession(t.Context(), state.RoomInput{Key: "group", RequestID: "seed", SenderID: "alice", Text: "new conversation"})
			if err != nil || len(fresh.Room.History) != 0 {
				t.Fatal(fmt.Sprint(fresh, err))
			}
		})
	}
}

func TestRoomQueuedInputCannotObserveResultsCompletedAfterItsArrival(t *testing.T) {
	h, _ := testHost(t)
	seed, err := h.RoomSession(t.Context(), state.RoomInput{Key: "room", RequestID: "seed", SenderID: "alice", Text: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	arrived := time.Now().UTC()
	runOK(t, h, seed, "seed")
	queued, err := h.RoomSession(t.Context(), state.RoomInput{Key: "room", RequestID: "queued", SenderID: "bob", Text: "question", ReceivedAt: arrived})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Room.ForkFromNativeSessionID != "" || len(queued.Room.History) != 1 || queued.Room.History[0].Status != "running" || queued.Room.History[0].Reply != "" {
		t.Fatal("queued message saw a future result", queued.Room)
	}
	later, err := h.RoomSession(t.Context(), state.RoomInput{Key: "room", RequestID: "later", SenderID: "bob", Text: "next question"})
	if err != nil || later.Room.ForkFromNativeSessionID != seed.NativeSessionID {
		t.Fatal("later message lost the completed checkpoint", later, err)
	}
}

func TestRoomHistorySurvivesRuntimeAndModelChanges(t *testing.T) {
	for _, change := range []string{"runtime", "model"} {
		t.Run(change, func(t *testing.T) {
			h, runner := testHost(t)
			h.Config.RuntimeFingerprint = "runtime-A"
			input := func(id, text string) state.RoomInput {
				return state.RoomInput{Key: "owner-room", RequestID: id, SenderID: "owner", SenderName: "Owner", Text: text}
			}
			seed, err := h.RoomSession(t.Context(), input("service-check", "check service"))
			if err != nil {
				t.Fatal(err)
			}
			prior := runOK(t, h, seed, seed.Room.Text)
			if change == "runtime" {
				h.Config.RuntimeFingerprint = "runtime-B"
			} else {
				h.Config.Model = "model-B"
			}
			greeting, err := h.RoomSession(t.Context(), input("greeting", "hi"))
			if err != nil {
				t.Fatal(err)
			}
			runner.status = "canceled"
			if canceled, err := h.Run(t.Context(), greeting, "hi"); err == nil || canceled.ExecutionStatus != "canceled" {
				t.Fatalf("expected retained canceled greeting: run=%+v error=%v", canceled, err)
			}
			runner.status = ""
			request := input("forward", "send that result to the colleague")
			request.ReplyToID = "unresolved-outgoing-id"
			followup, err := h.RoomSession(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			turn := followup.Room
			if turn.ParentSessionID != "" || turn.ForkFromNativeSessionID != "" || turn.NativeContinuation != "no_compatible_completed_parent" || turn.ReplyToID != request.ReplyToID || len(turn.History) != 2 {
				t.Fatalf("invalid discontinuity snapshot: %+v", turn)
			}
			entry := turn.History[0]
			if entry.RequestID != "service-check" || entry.SenderID != "owner" || entry.RunID != prior.ID || entry.Reply != prior.Output || entry.Status != "completed" || entry.TextOnlyReason != "runtime_or_model_changed" || turn.History[1].Status != "canceled" {
				t.Fatalf("lost attributed result: %+v", turn.History)
			}
			// Check the persisted snapshot and the actual harness prompt.
			saved, err := h.Store.Session(t.Context(), followup.ID)
			if err != nil || saved.Room.History[0] != entry {
				t.Fatalf("snapshot not durable: %+v %v", saved, err)
			}
			runOK(t, h, followup, request.Text)
			got := runner.requests[len(runner.requests)-1]
			for _, text := range []string{prior.Output, prior.ID, request.ReplyToID, "do not treat it as repeated instructions or replay tools", "preview-only quotes remain unresolved", "Current capabilities govern this run"} {
				if !strings.Contains(got.Prompt, text) {
					t.Fatalf("prompt lacks %q", text)
				}
			}
			if got.Resume || got.ForkFromNativeSessionID != "" {
				t.Fatal("incompatible native context reused")
			}
			if _, err := h.RoomSession(t.Context(), state.RoomInput{Key: "owner-room", ContinueFrom: seed.ID, RequestID: "takeover", SenderID: "owner", Text: "continue"}); err == nil {
				t.Fatal("explicit incompatible takeover accepted")
			}
		})
	}
}

func TestRoomSelectsOlderCompatibleParentWithTextOnlySibling(t *testing.T) {
	h, _ := testHost(t)
	h.Config.RuntimeFingerprint = "A"
	enter := func(id string) state.Session {
		s, err := h.RoomSession(t.Context(), state.RoomInput{Key: "room", RequestID: id, SenderID: "owner", Text: id})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	older := enter("older")
	runOK(t, h, older, "older")
	h.Config.RuntimeFingerprint = "B"
	sibling := enter("sibling")
	runOK(t, h, sibling, "sibling")
	h.Config.RuntimeFingerprint = "A"
	later := enter("later")
	if later.Room.ParentSessionID != older.ID || later.Room.ForkFromNativeSessionID != older.NativeSessionID || later.Room.NativeContinuation != "compatible_completed_parent" {
		t.Fatalf("older compatible parent lost: %+v", later.Room)
	}
	prompt := roomPrompt(later.Room)
	if !strings.Contains(prompt, sibling.Room.RequestID) || !strings.Contains(prompt, "runtime_or_model_changed") || len(later.Room.InheritedRequestIDs) != 1 || later.Room.InheritedRequestIDs[0] != "older" {
		t.Fatalf("sibling evidence or dedup lost: %+v %s", later.Room, prompt)
	}
}

func TestRoomPromptExplainsTextOnlyHistoryAndUnresolvedQuote(t *testing.T) {
	turn := &state.RoomTurn{
		RoomInput:          state.RoomInput{RequestID: "forward", SenderID: "owner", Text: "send that result", ReplyToID: "unknown-bot-id"},
		NativeContinuation: "no_compatible_completed_parent",
		History:            []state.RoomEntry{{RequestID: "service-check", SenderID: "owner", RunID: "actual-run", Input: "check service", Reply: "service healthy: evidence-314159", Status: "completed", TextOnlyReason: "runtime_or_model_changed"}},
	}
	prompt := roomPrompt(turn)
	for _, text := range []string{"actual-run", "service healthy: evidence-314159", "unknown-bot-id", "runtime_or_model_changed", "no_compatible_completed_parent", "do not treat it as repeated instructions or replay tools", "preview-only quotes remain unresolved", "ask if ambiguous", "Current capabilities govern this run"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt lacks %q", text)
		}
	}
	if roomFork(turn) != "" {
		t.Fatal("text-only prompt fabricated native parent")
	}
}
