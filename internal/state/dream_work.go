package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const DreamWorkPolicyVersion = "dream-incremental-v1"

type DreamSkillEvidence struct {
	EventSeq        int64  `json:"event_seq"`
	Kind            string `json:"kind"` // offered, loaded, or reference_read
	SkillID         string `json:"skill_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Revision        int    `json:"revision,omitempty"`
	Digest          string `json:"digest,omitempty"`
	Reference       string `json:"reference,omitempty"`
	ReferenceDigest string `json:"reference_digest,omitempty"`
	Status          string `json:"status"`
}

type DreamWorkItem struct {
	ID                  string               `json:"id"`
	Partition           DreamPartition       `json:"partition"`
	SourceKey           string               `json:"source_key"`
	Source              string               `json:"source"`
	RunID               string               `json:"run_id,omitempty"`
	SessionID           string               `json:"session_id,omitempty"`
	EvidenceDigest      string               `json:"evidence_digest"`
	PriorEvidenceDigest string               `json:"prior_evidence_digest,omitempty"`
	PolicyVersion       string               `json:"policy_version"`
	State               string               `json:"state"` // pending, processed, missing_evidence, retryable, retired
	MissingReasons      []string             `json:"missing_reasons,omitempty"`
	SkillEvidence       []DreamSkillEvidence `json:"skill_evidence,omitempty"`
	ReceiptIDs          []string             `json:"receipt_ids,omitempty"`
	CandidateIDs        []string             `json:"candidate_ids,omitempty"`
	FirstSeenAt         time.Time            `json:"first_seen_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	ProcessedAt         *time.Time           `json:"processed_at,omitempty"`
	Attempt             int                  `json:"attempt"`
	NextAttemptAt       *time.Time           `json:"next_attempt_at,omitempty"`
	LastError           string               `json:"last_error,omitempty"`
}

type DreamScanCursor struct {
	HandoffAfter string         `json:"handoff_after,omitempty"`
	SkillAfterID string         `json:"skill_after_id,omitempty"`
	Partition    DreamPartition `json:"partition"`
	AfterRowID   int64          `json:"after_row_id"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type DreamCanonicalRun struct {
	RowID   int64
	Run     Run
	Session Session
}

func validDreamWorkState(v string) bool {
	switch v {
	case "pending", "processed", "missing_evidence", "retryable", "retired":
		return true
	}
	return false
}

// PrepareDreamWork atomically compares the observed evidence with its durable
// checkpoint. It returns false when an identical terminal observation needs no work.
func (s *Store) PrepareDreamWork(ctx context.Context, observed DreamWorkItem, now time.Time) (DreamWorkItem, bool, error) {
	if s.readOnly || observed.ID == "" || observed.SourceKey == "" || observed.EvidenceDigest == "" || observed.PolicyVersion == "" || !validDreamPartition(observed.Partition) || !s.matchesPartition(observed.Partition.AgentID, observed.Partition.AudienceID) {
		return observed, false, errors.New("invalid dream work observation")
	}
	now = now.UTC()
	ready := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT payload FROM dream_work_items WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? AND source_key=?`, observed.Partition.PersonID, observed.Partition.AgentID, observed.Partition.AudienceID, observed.Partition.Scope, observed.SourceKey).Scan(&raw)
		if err == nil {
			var prior DreamWorkItem
			if err = json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			if prior.EvidenceDigest == observed.EvidenceDigest && prior.PolicyVersion == observed.PolicyVersion {
				if prior.State == "retryable" && (prior.NextAttemptAt == nil || !now.Before(*prior.NextAttemptAt)) {
					prior.State, prior.UpdatedAt, prior.NextAttemptAt = "pending", now, nil
					raw, _ = json.Marshal(prior)
					if _, err = tx.ExecContext(ctx, `UPDATE dream_work_items SET state=?,updated_at=?,payload=? WHERE id=?`, prior.State, timestamp(now), raw, prior.ID); err != nil {
						return err
					}
					observed, ready = prior, true
					return nil
				}
				observed = prior
				ready = prior.State == "pending"
				return nil
			}
			observed.PriorEvidenceDigest = prior.EvidenceDigest
			observed.FirstSeenAt = prior.FirstSeenAt
			observed.ReceiptIDs = append([]string(nil), prior.ReceiptIDs...)
			observed.CandidateIDs = append([]string(nil), prior.CandidateIDs...)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if observed.FirstSeenAt.IsZero() {
			observed.FirstSeenAt = now
		}
		observed.UpdatedAt = now
		observed.ProcessedAt, observed.NextAttemptAt, observed.LastError = nil, nil, ""
		observed.Attempt = 0
		if len(observed.MissingReasons) > 0 {
			observed.State = "missing_evidence"
		} else {
			observed.State, ready = "pending", true
		}
		raw, _ = json.Marshal(observed)
		_, err = tx.ExecContext(ctx, `INSERT INTO dream_work_items(id,source_key,person_id,agent_id,audience_id,scope,state,updated_at,payload) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(person_id,agent_id,audience_id,scope,source_key) DO UPDATE SET id=excluded.id,state=excluded.state,updated_at=excluded.updated_at,payload=excluded.payload`, observed.ID, observed.SourceKey, observed.Partition.PersonID, observed.Partition.AgentID, observed.Partition.AudienceID, observed.Partition.Scope, observed.State, timestamp(now), raw)
		return err
	})
	return observed, ready, err
}

func (s *Store) FinishDreamWork(ctx context.Context, item DreamWorkItem, stateName, message string, receiptIDs, candidateIDs []string, now time.Time) (DreamWorkItem, error) {
	if s.readOnly || !validDreamWorkState(stateName) || !validDreamPartition(item.Partition) || !s.matchesPartition(item.Partition.AgentID, item.Partition.AudienceID) {
		return item, errors.New("invalid dream work transition")
	}
	now = now.UTC()
	if len(message) > 4096 {
		message = message[:4096]
	}
	updated := item
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var persistedRaw []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM dream_work_items WHERE id=? AND person_id=? AND agent_id=? AND audience_id=? AND scope=?`, item.ID, item.Partition.PersonID, item.Partition.AgentID, item.Partition.AudienceID, item.Partition.Scope).Scan(&persistedRaw); err != nil {
			return err
		}
		var persisted DreamWorkItem
		if err := json.Unmarshal(persistedRaw, &persisted); err != nil {
			return err
		}
		updated = persisted
		if persisted.EvidenceDigest != item.EvidenceDigest || persisted.PolicyVersion != item.PolicyVersion {
			return errors.New("dream work evidence changed during transition")
		}
		updated.State, updated.UpdatedAt, updated.LastError = stateName, now, message
		if receiptIDs != nil {
			updated.ReceiptIDs = appendDreamUnique(updated.ReceiptIDs, receiptIDs...)
		}
		if candidateIDs != nil {
			updated.CandidateIDs = appendDreamUnique(updated.CandidateIDs, candidateIDs...)
		}
		if stateName == "processed" {
			updated.ProcessedAt = &now
			updated.NextAttemptAt = nil
		} else if stateName == "retryable" {
			updated.Attempt++
			delay := time.Minute << min(updated.Attempt-1, 6)
			if delay > time.Hour {
				delay = time.Hour
			}
			next := now.Add(delay)
			updated.NextAttemptAt = &next
		}
		updatedRaw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE dream_work_items SET state=?,updated_at=?,payload=? WHERE id=? AND person_id=? AND agent_id=? AND audience_id=? AND scope=? AND payload=?`, updated.State, timestamp(now), updatedRaw, item.ID, item.Partition.PersonID, item.Partition.AgentID, item.Partition.AudienceID, item.Partition.Scope, persistedRaw)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("dream work item changed during transition")
		}
		return nil
	})
	return updated, err
}

func appendDreamUnique(existing []string, values ...string) []string {
	out := append([]string(nil), existing...)
	seen := map[string]bool{}
	for _, value := range out {
		seen[value] = true
	}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value], out = true, append(out, value)
		}
	}
	return out
}

func (s *Store) DreamWorkItems(ctx context.Context, p DreamPartition, stateName string, limit int) ([]DreamWorkItem, error) {
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 || stateName != "" && stateName != "all" && !validDreamWorkState(stateName) {
		return nil, errors.New("invalid dream work query")
	}
	query := `SELECT payload FROM dream_work_items WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=?`
	args := []any{p.PersonID, p.AgentID, p.AudienceID, p.Scope}
	if stateName != "" && stateName != "all" {
		query += " AND state=?"
		args = append(args, stateName)
	}
	query += " ORDER BY updated_at DESC,id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DreamWorkItem
	for rows.Next() {
		var raw []byte
		var item DreamWorkItem
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &item)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DreamWorkItem(ctx context.Context, p DreamPartition, id string) (DreamWorkItem, error) {
	var item DreamWorkItem
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || id == "" {
		return item, errors.New("invalid dream work lookup")
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM dream_work_items WHERE id=? AND person_id=? AND agent_id=? AND audience_id=? AND scope=?`, id, p.PersonID, p.AgentID, p.AudienceID, p.Scope).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &item)
	}
	return item, err
}

func (s *Store) DreamWorkItemBySource(ctx context.Context, p DreamPartition, sourceKey string) (DreamWorkItem, error) {
	var item DreamWorkItem
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || sourceKey == "" {
		return item, errors.New("invalid dream work source lookup")
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM dream_work_items WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? AND source_key=?`, p.PersonID, p.AgentID, p.AudienceID, p.Scope, sourceKey).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &item)
	}
	return item, err
}

func (s *Store) DreamScanCursor(ctx context.Context, p DreamPartition) (DreamScanCursor, error) {
	c := DreamScanCursor{Partition: p}
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) {
		return c, errors.New("invalid dream cursor partition")
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM dream_scan_cursors WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=?`, p.PersonID, p.AgentID, p.AudienceID, p.Scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}

func (s *Store) SaveDreamScanCursor(ctx context.Context, c DreamScanCursor) error {
	if s.readOnly || !validDreamPartition(c.Partition) || !s.matchesPartition(c.Partition.AgentID, c.Partition.AudienceID) || c.AfterRowID < 0 {
		return errors.New("invalid dream cursor")
	}
	c.UpdatedAt = time.Now().UTC()
	raw, _ := json.Marshal(c)
	_, err := s.db.ExecContext(ctx, `INSERT INTO dream_scan_cursors(person_id,agent_id,audience_id,scope,payload) VALUES(?,?,?,?,?) ON CONFLICT(person_id,agent_id,audience_id,scope) DO UPDATE SET payload=excluded.payload`, c.Partition.PersonID, c.Partition.AgentID, c.Partition.AudienceID, c.Partition.Scope, raw)
	return err
}

// DreamCanonicalRunPage rotates to row zero after reaching the end. The caller
// saves Next only after every returned item has been durably classified.
func (s *Store) DreamCanonicalRunPage(ctx context.Context, p DreamPartition, limit int) ([]DreamCanonicalRun, DreamScanCursor, error) {
	cursor, err := s.DreamScanCursor(ctx, p)
	if err != nil {
		return nil, cursor, err
	}
	if limit < 1 || limit > 200 {
		return nil, cursor, errors.New("invalid canonical run page limit")
	}
	read := func(after int64) ([]DreamCanonicalRun, int64, error) {
		rows, e := s.db.QueryContext(ctx, `SELECT rowid,payload FROM runs WHERE rowid>? AND scope=? ORDER BY rowid LIMIT ?`, after, p.Scope, limit)
		if e != nil {
			return nil, after, e
		}
		defer rows.Close()
		type candidate struct {
			rowID int64
			run   Run
		}
		var candidates []candidate
		last := after
		for rows.Next() {
			var rowID int64
			var raw []byte
			var run Run
			if e = rows.Scan(&rowID, &raw); e == nil {
				e = json.Unmarshal(raw, &run)
			}
			if e != nil {
				return nil, after, e
			}
			last = rowID
			a, audience := normalizedPartition(run.AgentID, run.AudienceID)
			trusted := run.Source != nil && run.Source.SenderID == p.PersonID || run.Source == nil && p.PersonID == "local-owner"
			if a != p.AgentID || audience != p.AudienceID || !trusted {
				continue
			}
			candidates = append(candidates, candidate{rowID: rowID, run: run})
		}
		if e = errors.Join(rows.Err(), rows.Close()); e != nil {
			return nil, after, e
		}
		out := make([]DreamCanonicalRun, 0, len(candidates))
		for _, candidate := range candidates {
			session, readErr := s.readSession(ctx, s.db, candidate.run.SessionID)
			if readErr != nil {
				return nil, after, readErr
			}
			full, readErr := s.Run(ctx, candidate.run.ID)
			if readErr != nil {
				return nil, after, readErr
			}
			out = append(out, DreamCanonicalRun{RowID: candidate.rowID, Run: full, Session: session})
		}
		return out, last, nil
	}
	items, last, err := read(cursor.AfterRowID)
	if err != nil {
		return nil, cursor, err
	}
	if last == cursor.AfterRowID && cursor.AfterRowID > 0 {
		items, last, err = read(0)
	}
	cursor.AfterRowID = last
	return items, cursor, err
}

func (s *Store) DreamCanonicalRunByID(ctx context.Context, p DreamPartition, id string) (DreamCanonicalRun, error) {
	var out DreamCanonicalRun
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || id == "" {
		return out, errors.New("invalid canonical run lookup")
	}
	run, err := s.Run(ctx, id)
	if err != nil {
		return out, err
	}
	a, audience := normalizedPartition(run.AgentID, run.AudienceID)
	trusted := run.Source != nil && run.Source.SenderID == p.PersonID || run.Source == nil && p.PersonID == "local-owner"
	if a != p.AgentID || audience != p.AudienceID || run.Scope != p.Scope || !trusted {
		return out, errors.New("canonical run does not match dream partition")
	}
	session, err := s.Session(ctx, run.SessionID)
	if err != nil {
		return out, err
	}
	out.Run, out.Session = run, session
	return out, nil
}

func (s *Store) DreamCandidatesByReceipt(ctx context.Context, receiptID string) ([]DreamCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM dream_candidates WHERE receipt_id=? ORDER BY id`, receiptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DreamCandidate
	for rows.Next() {
		var raw []byte
		var c DreamCandidate
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &c)
		}
		if err != nil {
			return nil, err
		}
		if !s.matchesPartition(c.Partition.AgentID, c.Partition.AudienceID) {
			return nil, errors.New("dream candidate belongs to another partition")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
