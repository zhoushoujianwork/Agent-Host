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
