package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// DreamCleanup retains the verified manifest so interrupted partial deletion can
// validate every remaining file without trusting the remaining directory itself.
type DreamCleanup struct {
	ID          string          `json:"id"`
	Partition   DreamPartition  `json:"partition"`
	Path        string          `json:"path"`
	Digest      string          `json:"digest"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason"`
	Manifest    json.RawMessage `json:"manifest"`
	Inode       uint64          `json:"inode"`
	Device      uint64          `json:"device"`
	PlannedAt   time.Time       `json:"planned_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

func (s *Store) SaveDreamCleanup(ctx context.Context, c DreamCleanup) error {
	if s.readOnly || c.ID == "" || !validDreamPartition(c.Partition) || !s.matchesPartition(c.Partition.AgentID, c.Partition.AudienceID) || (c.Status != "planned" && c.Status != "completed") {
		return errors.New("invalid dream cleanup record")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO dream_cleanup(id,person_id,agent_id,audience_id,scope,status,payload) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload WHERE dream_cleanup.person_id=excluded.person_id AND dream_cleanup.agent_id=excluded.agent_id AND dream_cleanup.audience_id=excluded.audience_id AND dream_cleanup.scope=excluded.scope`, c.ID, c.Partition.PersonID, c.Partition.AgentID, c.Partition.AudienceID, c.Partition.Scope, c.Status, raw)
	return err
}

func (s *Store) DreamCleanupRecords(ctx context.Context, p DreamPartition, status string, limit int) ([]DreamCleanup, error) {
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid dream cleanup query")
	}
	query := `SELECT payload FROM dream_cleanup WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=?`
	args := []any{p.PersonID, p.AgentID, p.AudienceID, p.Scope}
	if status != "" && status != "all" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DreamCleanup
	for rows.Next() {
		var raw []byte
		var c DreamCleanup
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

// RetireExpiredDreamCandidates keeps evidence and checkpoints, preventing an
// unchanged source from regenerating a retired proposal on the next wake.
func (s *Store) RetireExpiredDreamCandidates(ctx context.Context, p DreamPartition, now time.Time, limit int) (int, error) {
	if s.readOnly || !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 {
		return 0, errors.New("invalid candidate retirement")
	}
	count := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT payload FROM dream_candidates WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? AND status='pending' AND julianday(json_extract(payload,'$.ExpiresAt')) <= julianday(?) ORDER BY id LIMIT ?`, p.PersonID, p.AgentID, p.AudienceID, p.Scope, timestamp(now), limit)
		if err != nil {
			return err
		}
		var candidates []DreamCandidate
		for rows.Next() {
			var raw []byte
			var c DreamCandidate
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, c := range candidates {
			if c.ExpiresAt.After(now) {
				continue
			}
			c.Status = "expired"
			raw, _ := json.Marshal(c)
			result, e := tx.ExecContext(ctx, `UPDATE dream_candidates SET status='expired',payload=? WHERE id=? AND status='pending'`, raw, c.ID)
			if e != nil {
				return e
			}
			n, _ := result.RowsAffected()
			count += int(n)
		}
		return nil
	})
	return count, err
}
