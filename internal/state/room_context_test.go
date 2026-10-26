package state

import (
	"agent-host/internal/event"
	"testing"
	"time"
)

func TestRoomTextScopeBoundaries(t *testing.T) {
	for _, boundary := range []string{"agent", "audience", "scope", "project", "workspace", "project-set", "project-scope", "project-workspace", "directories", "room", "reset"} {
		t.Run(boundary, func(t *testing.T) {
			store, _ := testStore(t)
			base := Session{ID: "first", NativeSessionID: "native-first", AgentID: "agent", AudienceID: "owner", Scope: "scope", ProjectID: "project", Workspace: "/workspace", Model: "A", RuntimeFingerprint: "A", Projects: []ProjectRef{{ID: "project", Scope: "scope", Workspace: "/workspace"}}, AdditionalDirectories: []string{"/extra"}}
			store = store.WithPartition(base.AgentID, base.AudienceID)
			input := RoomInput{Key: "room", RequestID: "first", SenderID: "owner", Text: "private evidence"}
			if _, err := store.EnterRoom(t.Context(), base, input); err != nil {
				t.Fatal(err)
			}
			next := base
			next.ID, next.NativeSessionID, next.Model, next.RuntimeFingerprint = "next", "native-next", "B", "B"
			next.Projects = append([]ProjectRef(nil), base.Projects...)
			switch boundary {
			case "agent":
				next.AgentID = "other"
			case "audience":
				next.AudienceID = "other"
			case "scope":
				next.Scope = "other"
			case "project":
				next.ProjectID = "other"
			case "workspace":
				next.Workspace = "/other"
			case "project-set":
				next.Projects[0].ID = "other"
			case "project-scope":
				next.Projects[0].Scope = "other"
			case "project-workspace":
				next.Projects[0].Workspace = "/other"
			case "directories":
				next.AdditionalDirectories = []string{"/other"}
			case "room":
				input.Key = "other"
			case "reset":
				if err := store.ResetRoom(t.Context(), input.Key); err != nil {
					t.Fatal(err)
				}
			}
			store = store.WithPartition(next.AgentID, next.AudienceID)
			input.RequestID = "next"
			got, err := store.EnterRoom(t.Context(), next, input)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Room.History) != 0 || got.Room.ParentSessionID != "" || got.Room.ForkFromNativeSessionID != "" {
				t.Fatalf("cross-boundary evidence: %+v", got.Room)
			}
		})
	}
}

func TestRoomResultRequiresSealAvailableAtArrival(t *testing.T) {
	store, _ := testStore(t)
	run := addRun(t, store, "seal-source", "/project", "check", true)
	arrived := time.Now().UTC()
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	available, err := roomResultAvailable(t.Context(), tx, run.ID, arrived)
	tx.Rollback()
	if err != nil || available {
		t.Fatalf("unsealed result available: %v %v", available, err)
	}
	seal := event.Event{RunID: run.ID, SessionID: run.SessionID, Type: "run.finished", At: arrived.Add(time.Second)}
	if err := store.AppendEvent(t.Context(), &seal); err != nil {
		t.Fatal(err)
	}
	tx, err = store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	available, err = roomResultAvailable(t.Context(), tx, run.ID, arrived)
	if err != nil || available {
		t.Fatalf("future seal available: %v %v", available, err)
	}
	available, err = roomResultAvailable(t.Context(), tx, run.ID, arrived.Add(2*time.Second))
	if err != nil || !available {
		t.Fatalf("past seal unavailable: %v %v", available, err)
	}
}

func TestRoomContextAfterRuntimeDiscontinuity(t *testing.T) {
	for _, change := range []string{"runtime", "model"} {
		t.Run(change, func(t *testing.T) {
			store, _ := testStore(t)
			base := Session{ID: "task", Scope: "/workspace", Workspace: "/workspace", Model: "A", RuntimeFingerprint: "A", NativeSessionID: "native-task"}
			enter := func(session Session, id, text, replyTo string) Session {
				got, err := store.EnterRoom(t.Context(), session, RoomInput{Key: "room", RequestID: id, SenderID: "owner", Text: text, ReplyToID: replyTo})
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			finish := func(session Session, id, status string) {
				run := Run{ID: id, SessionID: session.ID, Scope: session.Scope, Workspace: session.Workspace, RequestedModel: session.Model, RuntimeFingerprint: session.RuntimeFingerprint, NativeSessionID: session.NativeSessionID, Input: session.Room.Text}
				if err := store.CreateRun(t.Context(), run); err != nil {
					t.Fatal(err)
				}
				if err := store.CompleteRun(t.Context(), id, status, "actual service result", session.Model, ""); err != nil {
					t.Fatal(err)
				}
				seal := event.Event{RunID: id, SessionID: session.ID, Type: "run.finished", At: time.Now().UTC()}
				if err := store.AppendEvent(t.Context(), &seal); err != nil {
					t.Fatal(err)
				}
			}
			first := enter(base, "task", "check service", "")
			finish(first, "result-run", "completed")
			base.ID, base.NativeSessionID = "hi", "native-hi"
			if change == "runtime" {
				base.RuntimeFingerprint = "B"
			} else {
				base.Model = "B"
			}
			hi := enter(base, "hi", "hi", "")
			finish(hi, "hi-run", "canceled")
			base.ID, base.NativeSessionID = "followup", "native-followup"
			got := enter(base, "followup", "send that result", "unresolved-outgoing-id")
			if got.Room.ParentSessionID != "" || got.Room.ForkFromNativeSessionID != "" || got.Room.ReplyToID != "unresolved-outgoing-id" || got.Room.NativeContinuation != "no_compatible_completed_parent" || len(got.Room.History) != 2 {
				t.Fatalf("invalid context: %+v", got.Room)
			}
			entry := got.Room.History[0]
			if entry.SenderID != "owner" || entry.RunID != "result-run" || entry.Input != "check service" || entry.Reply != "actual service result" || entry.Status != "completed" || entry.TextOnlyReason != "runtime_or_model_changed" || got.Room.History[1].Status != "canceled" {
				t.Fatalf("lost attribution: %+v", got.Room.History)
			}
			saved, err := store.Session(t.Context(), got.ID)
			if err != nil || saved.Room.History[0] != entry {
				t.Fatalf("not durable: %+v %v", saved, err)
			}

			if _, err := store.EnterRoom(t.Context(), Session{ID: "takeover", NativeSessionID: "native-takeover", Scope: base.Scope, Workspace: base.Workspace, Model: base.Model, RuntimeFingerprint: base.RuntimeFingerprint}, RoomInput{Key: "room", RequestID: "takeover", SenderID: "owner", Text: "continue", ContinueFrom: first.ID}); err == nil {
				t.Fatal("incompatible explicit takeover accepted")
			}
			finish(got, "sibling-run", "completed")
			base.ID, base.NativeSessionID, base.Model, base.RuntimeFingerprint = "restored", "native-restored", "A", "A"
			restored := enter(base, "restored", "continue earlier work", "")
			if restored.Room.ParentSessionID != first.ID || restored.Room.ForkFromNativeSessionID != first.NativeSessionID || restored.Room.NativeContinuation != "compatible_completed_parent" || len(restored.Room.History) != 3 || restored.Room.History[2].RunID != "sibling-run" || restored.Room.History[2].Reply == "" {
				t.Fatalf("compatible older parent or sibling lost: %+v", restored.Room)
			}
			if len(restored.Room.InheritedRequestIDs) != 1 || restored.Room.InheritedRequestIDs[0] != "task" {
				t.Fatalf("inherited dedup lost: %+v", restored.Room)
			}
		})
	}
}
