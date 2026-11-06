package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (s *Store) RecordDreamSkillDraftBlocked(ctx context.Context, p DreamPartition, id, key, reason string, now time.Time) (DreamCandidate, error) {
	var out DreamCandidate
	if s.readOnly || !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || id == "" || key == "" || strings.TrimSpace(reason) == "" {
		return out, errors.New("invalid Dream Skill draft result")
	}
	if len(reason) > 4096 {
		reason = reason[:4096]
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM dream_candidates WHERE id=? AND person_id=? AND agent_id=? AND audience_id=? AND scope=? AND status='pending'`, id, p.PersonID, p.AgentID, p.AudienceID, p.Scope).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		if out.Kind != "procedure" {
			return errors.New("Dream Skill draft result requires pending procedure")
		}
		if out.SkillDraftStatus == "blocked" && out.SkillDraftKey == key {
			return nil
		}
		now = now.UTC()
		out.SkillDraftStatus, out.SkillDraftKey, out.SkillDraftReason = "blocked", key, reason
		out.SkillDraftAttempts++
		out.SkillDraftUpdatedAt = &now
		payload, err := json.Marshal(out)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE dream_candidates SET payload=? WHERE id=? AND status='pending' AND payload=?`, payload, id, raw)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("Dream Skill candidate changed during draft result")
		}
		return nil
	})
	return out, err
}
