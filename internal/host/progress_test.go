package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/state"
)

func progressJournal(t *testing.T) (*journal, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	release, err := state.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	h := &Host{Store: store, Config: Config{Scope: t.TempDir(), Model: "fixture"}}
	s := newSession(t, h)
	r := state.Run{ID: ID(), SessionID: s.ID, Scope: s.Scope, Workspace: s.Workspace, Input: "Inspect the fixture", Agent: "local-claude", NativeSessionID: s.NativeSessionID}
	if err := store.CreateRun(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return &journal{h: h, run: r, start: time.Now()}, db
}

func sendProgressTick(ctx context.Context, ticks chan<- time.Time) error {
	select {
	case ticks <- time.Now():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func completedProgressEvidence(t *testing.T, j *journal, wantError string, wantProgress bool) {
	t.Helper()
	stored, err := j.h.Store.Run(t.Context(), j.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var progress, tool, finished event.Event
	for _, e := range stored.Events {
		switch e.Type {
		case "phase.progress":
			progress = e
		case "tool.activity":
			tool = e
		case "phase.finished":
			finished = e
		}
	}
	if (progress.Seq != 0) != wantProgress {
		t.Fatalf("failed progress write was confused with its committed evidence: %+v", stored.Events)
	}
	if tool.Seq == 0 || tool.Status != "completed" {
		t.Fatalf("later tool evidence was lost: %+v", tool)
	}
	raw, ok := tool.Data["raw"].(map[string]any)
	if !ok || raw["output"] != "observed fixture result" {
		t.Fatalf("original tool evidence changed: %+v", tool)
	}
	if finished.Status != "completed" || !strings.Contains(fmt.Sprint(finished.Data["progress_error"]), wantError) {
		t.Fatalf("progress failure changed the phase outcome or lost its diagnostic: %+v", finished)
	}
	if err := j.h.Store.CompleteRun(t.Context(), j.run.ID, "completed", "fixture answer", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if receipt, err := j.h.receipt(j, nil); err != nil || receipt.ExecutionStatus != "completed" || receipt.Output != "fixture answer" {
		t.Fatalf("progress failure changed the completed native receipt: %+v %v", receipt, err)
	}
	stored, err = j.h.Store.Run(t.Context(), j.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := stored.Events[len(stored.Events)-1]
	warnings, ok := last.Data["progress_errors"].([]any)
	if last.Type != "run.finished" || !ok || len(warnings) != 1 {
		t.Fatalf("receipt lost the visible progress failure: %+v", last)
	}
	warning, ok := warnings[0].(map[string]any)
	if !ok || warning["phase"] != "execute" || warning["source_event_seq"] != float64(finished.Seq) || !strings.Contains(fmt.Sprint(warning["error"]), wantError) {
		t.Fatalf("receipt warning lost its original error or durable source: %+v", warning)
	}
}

func TestProgressSinkFailureDoesNotCancelExecution(t *testing.T) {
	j, _ := progressJournal(t)
	want := errors.Join(errors.New("fixture progress sink unavailable"), context.DeadlineExceeded)
	j.h.Sink = func(e event.Event) error {
		if e.Type == "phase.progress" {
			return want
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticks := make(chan time.Time)
	err := j.phaseWithTicks(ctx, "execute", "harness.claude", "fixture", func(ctx context.Context) error {
		if err := sendProgressTick(ctx, ticks); err != nil {
			return err
		}
		// The next tick is a barrier: the first optional write has finished.
		if err := sendProgressTick(ctx, ticks); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("optional progress canceled native execution: %w", err)
		}
		return j.emit(event.Event{Type: "tool.activity", Phase: "execute", Status: "completed", Data: map[string]any{"raw": json.RawMessage(`{"output":"observed fixture result"}`)}})
	}, ticks)
	if err != nil {
		t.Fatalf("progress sink failure became a business failure: %v", err)
	}
	completedProgressEvidence(t, j, want.Error(), true)
}

func TestProgressPersistenceFailureDoesNotCancelExecution(t *testing.T) {
	j, db := progressJournal(t)
	const want = "fixture progress persistence failed"
	if _, err := db.Exec("CREATE TRIGGER reject_progress BEFORE INSERT ON events WHEN json_extract(NEW.payload, '$.type') = 'phase.progress' BEGIN SELECT RAISE(ABORT, '" + want + "'); END;"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticks := make(chan time.Time)
	err := j.phaseWithTicks(ctx, "execute", "harness.claude", "fixture", func(ctx context.Context) error {
		if err := sendProgressTick(ctx, ticks); err != nil {
			return err
		}
		if err := sendProgressTick(ctx, ticks); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("optional journal write canceled native execution: %w", err)
		}
		return j.emit(event.Event{Type: "tool.activity", Phase: "execute", Status: "completed", Data: map[string]any{"raw": json.RawMessage(`{"output":"observed fixture result"}`)}})
	}, ticks)
	if err != nil {
		t.Fatalf("progress journal failure became a business failure: %v", err)
	}
	completedProgressEvidence(t, j, want, false)
}

func TestProgressPolicyPreservesCriticalEventFailures(t *testing.T) {
	for _, kind := range []string{"tool.activity", "phase.finished"} {
		t.Run(kind, func(t *testing.T) {
			j, _ := progressJournal(t)
			want := errors.New("fixture critical event unavailable")
			j.h.Sink = func(e event.Event) error {
				if e.Type == kind {
					return want
				}
				return nil
			}
			err := j.phaseWithTicks(t.Context(), "execute", "harness.claude", "fixture", func(context.Context) error {
				if kind == "tool.activity" {
					return j.emit(event.Event{Type: kind, Phase: "execute"})
				}
				return nil
			}, nil)
			if !errors.Is(err, want) {
				t.Fatalf("critical event failure was weakened: %v", err)
			}
		})
	}
}

func TestProgressPolicyPreservesParentCancellation(t *testing.T) {
	j, _ := progressJournal(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := j.phaseWithTicks(ctx, "execute", "harness.claude", "fixture", func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation was lost: %v", err)
	}
	stored, err := j.h.Store.Run(t.Context(), j.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	finished := stored.Events[len(stored.Events)-1]
	if finished.Type != "phase.finished" || finished.Status != "failed" {
		t.Fatalf("canceled work appeared completed: %+v", finished)
	}
}
