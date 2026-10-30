//go:build unix

package offloadchannel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func roomInput(id, text string) state.RoomInput {
	return state.RoomInput{Key: "room", RequestID: id, SenderID: "local-owner", Text: text}
}

func TestOffloadRoomsOverlapAndUnknownNeverResumeOrReplay(t *testing.T) {
	f := newFixture(t)
	f.host.Sink = nil
	first, err := f.host.RunRoom(t.Context(), roomInput("first", "first"))
	if err != nil {
		t.Fatal(err)
	}
	queued := roomInput("queued", "queued")
	queued.ReceivedAt = first.CreatedAt
	second, err := f.host.RunRoom(t.Context(), queued)
	if err != nil {
		t.Fatal(err)
	}
	if second.NativeSessionID == first.NativeSessionID || strings.Contains("\n"+f.read("argv.2"), "\n-r\n") {
		t.Fatal("overlapping intake inherited later native history")
	}
	seed := f.host.newSession()
	_, created, err := f.host.store().EnterOffloadRoom(t.Context(), seed, roomInput("interrupted", "never dispatched"))
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, err = f.host.RunRoom(t.Context(), roomInput("interrupted", "never dispatched")); err == nil || f.calls() != 2 {
		t.Fatal("accepted interrupted request was executed again")
	}
}

func TestOffloadRoomLeaseAndRecoveryProtectActiveRun(t *testing.T) {
	f := newFixture(t)
	f.host.Sink = nil
	f.mode("hang")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.host.RunRoom(ctx, roomInput("one", "slow")); done <- err }()
	for deadline := time.Now().Add(5 * time.Second); f.calls() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if f.calls() != 1 {
		t.Fatal("room execution did not start")
	}
	if _, err := f.host.RunRoom(t.Context(), roomInput("two", "busy")); !errors.Is(err, state.ErrSessionBusy) {
		t.Fatalf("busy room: %v", err)
	}
	if err := f.store.RecoverIdleChatRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	session, _, err := f.host.store().EnterOffloadRoom(t.Context(), f.host.newSession(), roomInput("one", "slow"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.host.store().LatestSessionRun(t.Context(), session.ID)
	if err != nil || active.ExecutionStatus != "running" {
		t.Fatal("recovery marked an owned room run unknown")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation hidden")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned process did not stop")
	}
	if f.calls() != 1 {
		t.Fatal("busy room launched another native execution")
	}
}

func TestOffloadIndependentRoomsRunConcurrently(t *testing.T) {
	f := newFixture(t)
	f.host.Sink = nil
	script := strings.Replace(fakeScript, "mode=$(cat", `touch "$d/start.$$"
mode=$(cat`, 1)
	if err := os.WriteFile(filepath.Join(f.dir, "offload"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f.mode("hang")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		go func(key string) {
			input := roomInput("one", key)
			input.Key = key
			_, err := f.host.RunRoom(ctx, input)
			done <- err
		}(key)
	}
	var starts []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		starts, _ = filepath.Glob(filepath.Join(f.dir, "start.*"))
		if len(starts) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("room did not stop")
		}
	}
	if len(starts) != 2 {
		t.Fatal("independent rooms serialized globally")
	}
}

func TestOffloadRoomMemoryFailureIsRecordedWithoutReplay(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		calls++
		return memory.Decision{}, errors.New("fixture classifier unavailable")
	})}
	first, err := f.host.RunRoom(t.Context(), roomInput("first", "remember"))
	if err == nil || first.ExecutionStatus != "completed" || first.MemoryStatus != "failed" {
		t.Fatalf("separate outcomes: %+v %v", first, err)
	}
	again, err := f.host.RunRoom(t.Context(), roomInput("first", "remember"))
	if err == nil || again.ID != first.ID || f.calls() != 1 || calls != 1 {
		t.Fatal("recorded memory failure retried native or classifier work")
	}
	second, err := f.host.RunRoom(t.Context(), roomInput("second", "new request"))
	if err == nil || second.NativeSessionID != first.NativeSessionID || f.calls() != 2 || calls != 2 {
		t.Fatal("memory failure blocked native continuity")
	}
}

func TestOffloadRoomReplayReflectsSeparateMemoryRetry(t *testing.T) {
	f := newFixture(t)
	f.host.Memory = &MemoryPlugin{Decider: decisionFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{}, errors.New("fixture failure")
	})}
	input := roomInput("first", "The release code is ACORN-482.")
	first, err := f.host.RunRoom(t.Context(), input)
	if err == nil || first.MemoryStatus != "failed" {
		t.Fatal("fixture did not fail closeout")
	}
	f.host.Memory.Decider = decisionFunc(statementDecider)
	if _, err = f.host.RetryMemory(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	var projected string
	f.host.Sink = func(e event.Event) error {
		if e.Type == "run.finished" {
			projected, _ = e.Data["memory_status"].(string)
		}
		return nil
	}
	replay, err := f.host.RunRoom(t.Context(), input)
	if err != nil || replay.MemoryStatus != "saved" || projected != "saved" || f.calls() != 1 {
		t.Fatalf("stale replay after retry: %s %+v %v", projected, replay, err)
	}
	original, err := f.host.store().Run(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range original.Events {
		if e.Type == "run.finished" && e.Data["memory_status"] != "failed" {
			t.Fatal("replay rewrote original closeout evidence")
		}
	}

}
