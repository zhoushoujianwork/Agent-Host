package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Room records belong to the operational database. Old linear sessions are
// retained unchanged; no native transcript is migrated or synthesized.
const schemaRoomsV16 = `
CREATE TABLE IF NOT EXISTS rooms(id TEXT PRIMARY KEY, epoch INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS room_messages(room_id TEXT NOT NULL, request_id TEXT NOT NULL,
 session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id), run_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(room_id,request_id));
PRAGMA user_version=16;
`

type RoomInput struct {
	ContinueFrom string    `json:"-"`
	ReceivedAt   time.Time `json:"received_at"`
	Key          string    `json:"-"`
	RequestID    string    `json:"request_id"`
	SenderID     string    `json:"sender_id"`
	SenderName   string    `json:"sender_name,omitempty"`
	Text         string    `json:"text"`
	ReplyToID    string    `json:"reply_to_id,omitempty"`
}

// RoomTurn is immutable intake evidence, persisted with both session and run.
// It is a bounded public context snapshot, never a native transcript merge.
type RoomTurn struct {
	RoomID string `json:"room_id"`
	RoomInput
	ForkFromNativeSessionID string      `json:"fork_from_native_session_id,omitempty"`
	ParentSessionID         string      `json:"parent_session_id,omitempty"`
	History                 []RoomEntry `json:"history,omitempty"`
	InheritedRequestIDs     []string    `json:"inherited_request_ids,omitempty"`
	NativeContinuation      string      `json:"native_continuation,omitempty"`
}

type RoomEntry struct {
	// TextOnlyReason explains native incompatibility without changing text authority.
	TextOnlyReason string `json:"text_only_reason,omitempty"`
	RequestID      string `json:"request_id"`
	SenderID       string `json:"sender_id"`
	SenderName     string `json:"sender_name,omitempty"`
	Input          string `json:"input"`
	ReplyToID      string `json:"reply_to_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Status         string `json:"status"`
	Reply          string `json:"reply,omitempty"`
	InputTruncated bool   `json:"input_truncated,omitempty"`
	ReplyTruncated bool   `json:"reply_truncated,omitempty"`
}

func (s *Store) roomKey(key string) string {
	a, u := normalizedPartition(s.agentID, s.audienceID)
	b, _ := json.Marshal([]string{a, u, key})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func (s *Store) ResetRoom(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("room key is required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO rooms(id,epoch) VALUES(?,1) ON CONFLICT(id) DO UPDATE SET epoch=epoch+1`, s.roomKey(key))
		return err
	})
}

// EnterRoom claims a unique request and seals its context in a short write
// transaction. The caller's fresh session is never used by another message.
// A duplicate returns the original receipt; it cannot launch a second run.
// Offload rooms are serialized by their owning host. Only the latest receipt
// may supply a native parent; an overlapping/unfinished input starts fresh.
const OffloadRoomFresh = "offload_fresh"
const OffloadRoomResume = "offload_serial_continuation"

func (s *Store) EnterRoom(ctx context.Context, session Session, input RoomInput) (Session, error) {
	result, _, err := s.enterRoom(ctx, session, input, false)
	return result, err
}

func (s *Store) EnterOffloadRoom(ctx context.Context, session Session, input RoomInput) (Session, bool, error) {
	return s.enterRoom(ctx, session, input, true)
}

func (s *Store) enterRoom(ctx context.Context, session Session, input RoomInput, offload bool) (Session, bool, error) {
	created := false
	var result Session
	if strings.TrimSpace(input.Key) == "" || input.RequestID == "" || input.SenderID == "" || strings.TrimSpace(input.Text) == "" {
		return result, false, errors.New("room, message, authenticated sender and text are required")
	}
	for _, value := range []string{input.Key, input.RequestID, input.SenderID, input.ReplyToID} {
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return result, false, errors.New("invalid room message identity")
		}
	}
	if len(input.Text) > 16*1024 || len(input.SenderName) > 200 || strings.ContainsAny(input.SenderName, "\x00\r\n") {
		return result, false, errors.New("room message exceeds input limits")
	}
	if input.ReceivedAt.IsZero() {
		input.ReceivedAt = time.Now().UTC()
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.assignPartition(&session.AgentID, &session.AudienceID); err != nil {
			return err
		}
		if offload && (session.AudienceID != OwnerAudienceID || input.SenderID != "local-owner" || input.ContinueFrom != "" || !strings.HasPrefix(session.RuntimeFingerprint, OffloadFingerprintPrefix) || !IsOffloadPending(session.NativeSessionID)) {
			return errors.New("invalid local Offload room intake")
		}
		var err error
		key := s.roomKey(input.Key)
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO rooms(id) VALUES(?)", key); err != nil {
			return err
		}
		var epoch int
		if err = tx.QueryRowContext(ctx, "SELECT epoch FROM rooms WHERE id=?", key).Scan(&epoch); err != nil {
			return err
		}
		roomID := key + ":" + strconv.Itoa(epoch)
		if input.ContinueFrom != "" {
			parent, err := s.readSession(ctx, tx, input.ContinueFrom)
			if err != nil {
				return err
			}
			if parent.Room != nil {
				roomID = parent.Room.RoomID
			}
		}
		var duplicate string
		err = tx.QueryRowContext(ctx, "SELECT session_id FROM room_messages WHERE room_id=? AND request_id=?", roomID, input.RequestID).Scan(&duplicate)
		if err == nil {
			result, err = s.readSession(ctx, tx, duplicate)
			if err != nil {
				return err
			}
			if result.Room == nil || result.Room.Text != input.Text || result.Room.SenderID != input.SenderID || result.Room.ReplyToID != input.ReplyToID || input.ContinueFrom != "" && result.Room.ParentSessionID != input.ContinueFrom {
				return errors.New("room request ID already has different content or sender")
			}
			if offload && (!sameRecoveryRuntime(result, session) || result.Model != session.Model || !sameRoomTextScope(result, session)) {
				return errors.New("room request belongs to a different runtime")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if session.Room != nil {
			return errors.New("session already belongs to a room request")
		}
		turn := &RoomTurn{RoomID: roomID, RoomInput: input, NativeContinuation: "no_compatible_completed_parent"}
		if offload {
			turn.NativeContinuation = OffloadRoomFresh
		}
		if input.ContinueFrom != "" {
			parent, err := s.readSession(ctx, tx, input.ContinueFrom)
			if err != nil {
				return err
			}
			if parent.Model != session.Model || parent.Scope != session.Scope || !sameRecoveryRuntime(parent, session) {
				return errors.New("takeover source does not match the selected runtime")
			}
			if err := sessionCanResume(ctx, tx, parent.ID); err != nil {
				return err
			}
			var payload []byte
			if err := tx.QueryRowContext(ctx, "SELECT payload FROM runs WHERE session_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1", parent.ID).Scan(&payload); err != nil {
				return err
			}
			var run Run
			if err := json.Unmarshal(payload, &run); err != nil {
				return err
			}
			var sealed bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM events WHERE run_id=? AND json_extract(payload,'$.type')='run.finished')", run.ID).Scan(&sealed); err != nil {
				return err
			}
			if !sealed || run.CompletedAt == nil || parent.NativeSessionID == "" {
				return errors.New("takeover source is still finalizing or has no native context")
			}
		}

		rows, err := tx.QueryContext(ctx, `SELECT session_id,run_id FROM room_messages WHERE room_id=? ORDER BY rowid DESC LIMIT 20`, roomID)
		if err != nil {
			return err
		}
		type prior struct{ session, run string }
		var ids []prior
		for rows.Next() {
			var p prior
			if err = rows.Scan(&p.session, &p.run); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, p)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for index, p := range ids {
			previous, err := s.readSession(ctx, tx, p.session)
			if err != nil {
				return err
			}
			if previous.Room == nil {
				return errors.New("room receipt has no provenance")
			}
			if previous.Room.ReceivedAt.After(input.ReceivedAt) {
				continue
			}
			// Text stays within the exact conversation and project/directory scope.
			// Native execution compatibility is a separate, stricter decision.
			if !sameRoomTextScope(previous, session) {
				continue
			}
			entry := RoomEntry{RequestID: previous.Room.RequestID, SenderID: previous.Room.SenderID, SenderName: previous.Room.SenderName, Input: previous.Room.Text, ReplyToID: previous.Room.ReplyToID, RunID: p.run, Status: "starting"}
			nativeCompatible := sameRecoveryRuntime(previous, session)
			if !nativeCompatible {
				entry.TextOnlyReason = "runtime_or_model_changed"
			}
			if p.run != "" {
				r, err := s.readRun(ctx, tx, p.run)
				if err != nil {
					return err
				}
				entry.Status = r.ExecutionStatus
				if r.CompletedAt != nil && r.CompletedAt.After(input.ReceivedAt) {
					entry.Status = "running"
				} else if r.ExecutionStatus == "completed" && r.CompletedAt == nil {
					entry.Status = "unknown"
				} else if r.ExecutionStatus == "completed" {
					available, err := roomResultAvailable(ctx, tx, r.ID, input.ReceivedAt)
					if err != nil {
						return err
					}
					if !available {
						entry.Status = "unknown"
					} else {
						entry.Reply, entry.ReplyTruncated = recoveryText(r.Output, 8000)
						if turn.ParentSessionID == "" && nativeCompatible && previous.NativeSessionID != "" && (!offload || index == 0 && previous.Room.SenderID == "local-owner" && (previous.Room.NativeContinuation == OffloadRoomFresh || previous.Room.NativeContinuation == OffloadRoomResume)) {
							turn.NativeContinuation = "compatible_completed_parent"
							if offload {
								turn.NativeContinuation = OffloadRoomResume
							}
							turn.ParentSessionID = previous.ID
							turn.ForkFromNativeSessionID = previous.NativeSessionID
						}
					}
				}
			}
			entry.Input, entry.InputTruncated = recoveryText(entry.Input, 4000)
			turn.History = append(turn.History, entry)
		}
		if input.ContinueFrom != "" {
			parent, err := s.readSession(ctx, tx, input.ContinueFrom)
			if err != nil {
				return err
			}
			turn.NativeContinuation = "explicit_takeover"
			turn.ParentSessionID, turn.ForkFromNativeSessionID = parent.ID, parent.NativeSessionID
		}
		if turn.ParentSessionID != "" {
			parent, err := s.readSession(ctx, tx, turn.ParentSessionID)
			if err != nil {
				return err
			}
			included := map[string]RoomEntry{}
			if parent.Room != nil {
				for _, entry := range parent.Room.History {
					included[entry.RequestID] = entry
				}
				for _, entry := range turn.History {
					if entry.RequestID == parent.Room.RequestID {
						included[entry.RequestID] = entry
					}
				}
			}
			for _, entry := range turn.History {
				old, ok := included[entry.RequestID]
				if ok && old == entry {
					turn.InheritedRequestIDs = append(turn.InheritedRequestIDs, entry.RequestID)
				}
			}
		}
		for i, j := 0, len(turn.History)-1; i < j; i, j = i+1, j-1 {
			turn.History[i], turn.History[j] = turn.History[j], turn.History[i]
		}
		session.Room = turn
		if err = insertSession(ctx, tx, session); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO room_messages(room_id,request_id,session_id) VALUES(?,?,?)", roomID, input.RequestID, session.ID); err != nil {
			return err
		}
		result = session
		created = true
		return nil
	})
	return result, created, err
}

func linkRoomRun(ctx context.Context, tx *sql.Tx, session Session, run *Run) error {
	if session.Room == nil {
		if run.Room != nil {
			return errors.New("run has unexpected room provenance")
		}
		return nil
	}
	if session.Room.Text != run.Input {
		return errors.New("room run text differs from its receipt")
	}
	res, err := tx.ExecContext(ctx, "UPDATE room_messages SET run_id=? WHERE session_id=? AND run_id=''", run.ID, session.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("room message already has an execution; replay is forbidden")
	}
	run.Room = session.Room
	return nil
}

// Execution permissions and native settings do not erase readable conversation
// evidence. This predicate grants no tool authority and cannot select a native parent.
func sameRoomTextScope(a, b Session) bool {
	return samePartition(a.AgentID, a.AudienceID, b.AgentID, b.AudienceID) &&
		a.Scope == b.Scope && a.ProjectID == b.ProjectID && a.WorkingDirectory() == b.WorkingDirectory() &&
		sameProjectRefs(a.Projects, b.Projects) && sameDirectories(a.AdditionalDirectories, b.AdditionalDirectories)
}

// A completed payload may precede its final journal seal. Do not expose a
// result that was still finalizing when this input arrived.
func roomResultAvailable(ctx context.Context, tx *sql.Tx, runID string, receivedAt time.Time) (bool, error) {
	var payload []byte
	err := tx.QueryRowContext(ctx, "SELECT payload FROM events WHERE run_id=? AND json_extract(payload,'$.type')='run.finished' ORDER BY seq LIMIT 1", runID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var seal struct {
		At time.Time `json:"at"`
	}
	if err := json.Unmarshal(payload, &seal); err != nil {
		return false, err
	}
	return !seal.At.IsZero() && !seal.At.After(receivedAt), nil
}
