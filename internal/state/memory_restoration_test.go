package state

import (
	"encoding/json"
	"testing"
)

func TestUndoMemoryRestoration(t *testing.T) {
	for _, status := range []string{"invalidated", "conflicted"} {
		t.Run(status, func(t *testing.T) {
			s, _, _, _ := fileStore(t)
			r := addRun(t, s, "source", "scope", "Use concise output.", true)
			if err := s.FinalizeMemory(testContext, r.ID, []MemoryUpdate{updateFor(r, "memory")}, decision); err != nil {
				t.Fatal(err)
			}
			if err := s.UndoMemoryRestoration(testContext, "memory", "not a restoration"); err == nil {
				t.Fatal("undo accepted an ordinary active revision")
			}
			if status == "invalidated" {
				if err := s.InvalidateMemory(testContext, "memory", "obsolete"); err != nil {
					t.Fatal(err)
				}
			} else {
				other := addRun(t, s, "other", "scope", "Use detailed output.", true)
				u := updateFor(other, "other-memory")
				u.ConflictIDs = []string{"memory"}
				if err := s.FinalizeMemory(testContext, other.ID, []MemoryUpdate{u}, decision); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Memory(testContext, "memory")
			if err != nil || before.Status != status {
				t.Fatalf("initial status: %+v %v", before, err)
			}
			if err := s.RestoreMemory(testContext, "memory", "mistaken restoration"); err != nil {
				t.Fatal(err)
			}
			if err := s.UndoMemoryRestoration(testContext, "memory", " "); err == nil {
				t.Fatal("undo accepted an empty reason")
			}
			if err := s.UndoMemoryRestoration(testContext, "memory", "restore prior review state"); err != nil {
				t.Fatal(err)
			}
			after, err := s.Memory(testContext, "memory")
			if err != nil || after.Status != status || after.Revision != before.Revision+2 || after.Text != before.Text || after.SourceRunID != before.SourceRunID || after.SourceType != before.SourceType {
				t.Fatalf("undo changed source or lost history: %+v %v", after, err)
			}
			var audit map[string]any
			if err := json.Unmarshal(after.RevisionDecision, &audit); err != nil || audit["policy"] != "undo-local-restoration-v1" || audit["restored_status"] != status {
				t.Fatalf("missing undo audit: %s %v", after.RevisionDecision, err)
			}
			if err := s.UndoMemoryRestoration(testContext, "memory", "repeat"); err == nil {
				t.Fatal("undo accepted a second reversal")
			}
			matches, err := s.Search(testContext, "scope", "concise", 10)
			if err != nil || len(matches) != 0 {
				t.Fatalf("disabled memory recalled: %+v %v", matches, err)
			}
			if _, err := s.VerifyFileMemory(testContext); err != nil {
				t.Fatal(err)
			}
		})
	}
}
