package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// DecisionMemories selects a comparison neighborhood rather than limiting the
// size of a retained scope. It does not claim that absent candidates are absent
// from the corpus; exact duplicate checks still happen at publication.
func (s *Store) DecisionMemories(ctx context.Context, scope, query string, limit int) ([]Memory, error) {
	if !s.FileMemoryEnabled() {
		return s.Memories(ctx, scope)
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("comparison limit must be 1–100")
	}
	r, err := s.Retrieve(ctx, scope, query, limit)
	if err != nil {
		return nil, err
	}
	var exact, conflicts []Memory
	err = s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		a, b := s.Partition()
		rows, err := tx.QueryContext(ctx, `SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND text=? AND status IN ('active','conflicted') AND (expires_at IS NULL OR expires_at>?) ORDER BY rowid LIMIT ?`, a, b, scope, query, timestamp(time.Now()), limit)
		if err != nil {
			return err
		}
		exact, err = scanMemories(rows)
		if err = errors.Join(err, rows.Close()); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND status='conflicted' AND (expires_at IS NULL OR expires_at>?) ORDER BY rowid DESC LIMIT ?`, a, b, scope, timestamp(time.Now()), limit)
		if err != nil {
			return err
		}
		conflicts, err = scanMemories(rows)
		return errors.Join(err, rows.Close())
	})
	if err != nil {
		return nil, err
	}
	items := []Memory{}
	seen := map[string]bool{}
	for _, group := range [][]Memory{exact, r.Matches, conflicts, r.Persistent} {
		for _, m := range group {
			if len(items) < limit && !seen[m.ID] {
				items = append(items, m)
				seen[m.ID] = true
			}
		}
	}
	return items, nil
}

// RestoreMemory is an explicit local decision. It does not erase the disable
// history or elevate the original source's evidence strength.
func (s *Store) RestoreMemory(ctx context.Context, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("restoration requires a reason")
	}
	return s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		m, err := s.readMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Status != "invalidated" && m.Status != "conflicted" {
			return errors.New("memory is not disabled or conflicted")
		}
		if expired(m, time.Now()) {
			return errors.New("expired memory requires an updated source before restoration")
		}
		m.Status = "active"
		m.Revision++
		m.ReviewNote = reason
		m.RevisionDecision, _ = json.Marshal(map[string]any{"policy": "explicit-local-restoration-v1", "reason": reason})
		return persistMemory(ctx, tx, m, false)
	})
}

// DisabledMemories supplies the complete bounded blacklist for admission, not
// recall. Too many disabled records excludes publication explicitly.
func (s *Store) DisabledMemories(ctx context.Context, scope string, limit int) ([]Memory, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("disabled comparison limit must be 1–100")
	}
	var items []Memory
	err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		a, b := s.Partition()
		rows, err := tx.QueryContext(ctx, `SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND status='invalidated' ORDER BY rowid LIMIT ?`, a, b, scope, limit+1)
		if err != nil {
			return err
		}
		items, err = scanMemories(rows)
		if err = errors.Join(err, rows.Close()); err != nil {
			return err
		}
		if len(items) > limit {
			return errors.New("disabled-memory comparison budget exceeded; publication excluded")
		}
		return nil
	})
	return items, err
}

func (s *Store) ExperiencePublicationFailure(ctx context.Context, runID, reason string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		r, err := s.readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if !s.FileMemoryEnabled() || r.ExecutionStatus != "completed" || r.ExperienceStatus != "review_pending" || reason == "" {
			return errors.New("run is not eligible for publication failure")
		}
		r.ExperienceError = reason
		return writeRun(ctx, tx, r)
	})
}
