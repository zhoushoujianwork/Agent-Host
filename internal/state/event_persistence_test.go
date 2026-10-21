package state

import (
	"context"
	"sync"
	"testing"
	"time"

	"agent-host/internal/event"
)

func TestFileMemoryEventsDoNotWaitForPublicationOrIndex(t *testing.T) {
	for _, busy := range []string{"publication", "index connection"} {
		t.Run(busy, func(t *testing.T) {
			s, _, _, _ := fileStore(t)
			run := addRun(t, s, "event", "scope", "Inspect service metadata.", false)
			var release func()
			if busy == "publication" {
				peer := &fileMemory{sourceDir: s.files.sourceDir}
				unlock, err := peer.lock(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = unlock() }
			} else {
				conn, err := s.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = conn.Close() }
			}
			release = sync.OnceFunc(release)
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			e := event.Event{RunID: run.ID, Type: "native.evidence", Phase: "execute"}
			err := s.AppendEvent(ctx, &e)
			release()
			if err != nil {
				t.Fatalf("unrelated memory work blocked execution evidence: %v", err)
			}
			saved, err := s.Run(t.Context(), run.ID)
			if err != nil || len(saved.Events) != 1 || saved.Events[0].Seq != e.Seq || e.Seq != 1 {
				t.Fatalf("execution evidence was not durable: %+v %v", saved.Events, err)
			}
		})
	}
}
