package state

import (
	"context"
	"encoding/json"
	"errors"
)

// PendingDreamProcedurePage rotates independently of run/handoff inspection.
// A blocked proposal remains inspectable without monopolizing each idle batch.
func (s *Store) PendingDreamProcedurePage(ctx context.Context, p DreamPartition, limit int) ([]DreamCandidate, error) {
	if s.readOnly || !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid bounded Dream procedure page")
	}
	cursor, err := s.DreamScanCursor(ctx, p)
	if err != nil {
		return nil, err
	}
	read := func(after string) ([]DreamCandidate, error) {
		rows, err := s.db.QueryContext(ctx, `SELECT payload FROM dream_candidates WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? AND status='pending' AND COALESCE(json_extract(payload,'$.Kind'),json_extract(payload,'$.kind'))='procedure' AND id>? ORDER BY id LIMIT ?`, p.PersonID, p.AgentID, p.AudienceID, p.Scope, after, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []DreamCandidate
		for rows.Next() {
			var raw []byte
			var c DreamCandidate
			if err = rows.Scan(&raw); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}
	items, err := read(cursor.SkillAfterID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 && cursor.SkillAfterID != "" {
		items, err = read("")
		if err != nil {
			return nil, err
		}
	}
	cursor.SkillAfterID = ""
	if len(items) > 0 {
		cursor.SkillAfterID = items[len(items)-1].ID
	}
	return items, s.SaveDreamScanCursor(ctx, cursor)
}
