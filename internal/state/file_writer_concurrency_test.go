package state

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"agent-host/internal/event"
)

func TestFileOperationalUpdateAndEventWriterCannotInvalidateEachOther(t *testing.T) {
	s, _, _, _ := fileStore(t)
	run := addRun(t, s, "overlap", "scope", "Inspect the room.", false)
	done := make(chan error, 1)
	err := s.transaction(t.Context(), func(tx *sql.Tx) error {
		current, err := s.readRun(t.Context(), tx, run.ID)
		if err != nil {
			return err
		}
		go func() {
			done <- s.AppendEvent(t.Context(), &event.Event{RunID: run.ID, Type: "native.evidence"})
		}()
		select {
		case err := <-done:
			return fmt.Errorf("event writer bypassed the operational transaction: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		current.Output = "durable progress"
		return writeRun(t.Context(), tx, current)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	saved, err := s.Run(t.Context(), run.ID)
	if err != nil || saved.Output != "durable progress" || len(saved.Events) != 1 {
		t.Fatal("lost run update or event", saved, err)
	}
}
