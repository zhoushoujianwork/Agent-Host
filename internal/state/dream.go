package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const schemaDream = `
CREATE TABLE IF NOT EXISTS dream_receipts(id TEXT PRIMARY KEY,idempotency_key TEXT NOT NULL UNIQUE,person_id TEXT NOT NULL,agent_id TEXT NOT NULL,audience_id TEXT NOT NULL,scope TEXT NOT NULL,status TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS dream_receipts_partition ON dream_receipts(person_id,agent_id,audience_id,scope,status);
CREATE TABLE IF NOT EXISTS dream_candidates(id TEXT PRIMARY KEY,receipt_id TEXT NOT NULL REFERENCES dream_receipts(id),person_id TEXT NOT NULL,agent_id TEXT NOT NULL,audience_id TEXT NOT NULL,scope TEXT NOT NULL,status TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS dream_candidates_partition ON dream_candidates(person_id,agent_id,audience_id,scope,status);
CREATE TABLE IF NOT EXISTS dream_cycles(id TEXT PRIMARY KEY,config_id TEXT NOT NULL,status TEXT NOT NULL,started_at TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS dream_cycles_config ON dream_cycles(config_id,started_at);
`

type DreamPartition struct {
	PersonID, AgentID, AudienceID, Scope string
}

type DreamReceipt struct {
	ID, IdempotencyKey, Source, SourceDigest                       string
	Partition                                                      DreamPartition
	Status, CanonicalRunID, CanonicalSessionID, Attribution, Error string
	CreatedAt                                                      time.Time
}

type DreamCandidate struct {
	ID, ReceiptID                              string
	Partition                                  DreamPartition
	Kind, Text, Status                         string
	SourceRefs                                 []string
	ExpiresAt                                  time.Time
	Inferred                                   bool
	CreatedAt                                  time.Time
	ReviewedAt                                 *time.Time
	ReviewActor, ReviewNote, MemoryID, SkillID string
}

type DreamCycle struct {
	ID         string     `json:"id"`
	ConfigID   string     `json:"config_id"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Receipts   []string   `json:"receipt_ids,omitempty"`
}

func (s *Store) SaveDreamCycle(ctx context.Context, c DreamCycle) error {
	if s.readOnly || c.ID == "" || c.ConfigID == "" || (c.Status != "running" && c.Status != "completed" && c.Status != "failed" && c.Status != "canceled") {
		return errors.New("invalid dream cycle")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO dream_cycles(id,config_id,status,started_at,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload`, c.ID, c.ConfigID, c.Status, timestamp(c.StartedAt), raw)
	return err
}

func (s *Store) DreamCycles(ctx context.Context, configID string, limit int) ([]DreamCycle, error) {
	return s.dreamCycles(ctx, configID, limit, false)
}

func (s *Store) LatestDreamCycles(ctx context.Context, configID string, limit int) ([]DreamCycle, error) {
	return s.dreamCycles(ctx, configID, limit, true)
}

func (s *Store) dreamCycles(ctx context.Context, configID string, limit int, latest bool) ([]DreamCycle, error) {
	if configID == "" || limit < 1 || limit > 200 {
		return nil, errors.New("invalid dream cycle query")
	}
	order := "started_at,id"
	if latest {
		order = "started_at DESC,id DESC"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM dream_cycles WHERE config_id=? ORDER BY "+order+" LIMIT ?", configID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DreamCycle
	for rows.Next() {
		var raw []byte
		var c DreamCycle
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &c)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func validDreamPartition(p DreamPartition) bool {
	for _, v := range []string{p.PersonID, p.AgentID, p.AudienceID, p.Scope} {
		if strings.TrimSpace(v) == "" || strings.TrimSpace(v) != v || len(v) > 512 || strings.ContainsAny(v, "\x00\r\n") {
			return false
		}
	}
	return true
}

func (s *Store) SaveDreamExtraction(ctx context.Context, receipt DreamReceipt, candidates []DreamCandidate) (DreamReceipt, error) {
	if s.readOnly || receipt.ID == "" || receipt.IdempotencyKey == "" || receipt.SourceDigest == "" || !validDreamPartition(receipt.Partition) {
		return receipt, errors.New("invalid writable dream receipt")
	}
	if !s.matchesPartition(receipt.Partition.AgentID, receipt.Partition.AudienceID) {
		return receipt, errors.New("dream receipt belongs to another agent or audience")
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	if receipt.Status == "" {
		receipt.Status = "scanned"
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT payload FROM dream_receipts WHERE idempotency_key=?", receipt.IdempotencyKey).Scan(&raw)
		if err == nil {
			var existing DreamReceipt
			if json.Unmarshal(raw, &existing) != nil || existing.SourceDigest != receipt.SourceDigest || existing.Partition != receipt.Partition {
				return errors.New("dream idempotency conflict")
			}
			receipt = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		body, _ := json.Marshal(receipt)
		if _, err = tx.ExecContext(ctx, "INSERT INTO dream_receipts(id,idempotency_key,person_id,agent_id,audience_id,scope,status,payload) VALUES(?,?,?,?,?,?,?,?)", receipt.ID, receipt.IdempotencyKey, receipt.Partition.PersonID, receipt.Partition.AgentID, receipt.Partition.AudienceID, receipt.Partition.Scope, receipt.Status, body); err != nil {
			return err
		}
		seen := map[string]bool{}
		for i := range candidates {
			c := &candidates[i]
			if c.ID == "" || seen[c.ID] || c.Partition != receipt.Partition || c.ReceiptID != receipt.ID || len(c.SourceRefs) == 0 || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(receipt.CreatedAt) || (c.Kind != "knowledge" && c.Kind != "working_preference" && c.Kind != "task" && c.Kind != "procedure") || strings.TrimSpace(c.Text) == "" {
				return errors.New("invalid dream candidate")
			}
			if c.Kind == "working_preference" && c.Inferred { /* inferred habits remain pending by construction */
			}
			seen[c.ID], c.Status = true, "pending"
			if c.CreatedAt.IsZero() {
				c.CreatedAt = receipt.CreatedAt
			}
			payload, _ := json.Marshal(c)
			if _, err = tx.ExecContext(ctx, "INSERT INTO dream_candidates(id,receipt_id,person_id,agent_id,audience_id,scope,status,payload) VALUES(?,?,?,?,?,?,?,?)", c.ID, c.ReceiptID, c.Partition.PersonID, c.Partition.AgentID, c.Partition.AudienceID, c.Partition.Scope, c.Status, payload); err != nil {
				return err
			}
		}
		return nil
	})
	return receipt, err
}

func (s *Store) CompleteDreamExtraction(ctx context.Context, receiptID string, candidates []DreamCandidate) (DreamReceipt, error) {
	var receipt DreamReceipt
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM dream_receipts WHERE id=?", receiptID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return err
		}
		if !s.matchesPartition(receipt.Partition.AgentID, receipt.Partition.AudienceID) {
			return errors.New("dream receipt belongs to another partition")
		}
		if receipt.Status == "extracted" {
			return nil
		}
		if receipt.Status != "scanned" {
			return errors.New("dream receipt cannot be extracted")
		}
		seen := map[string]bool{}
		for i := range candidates {
			c := &candidates[i]
			if c.ID == "" || seen[c.ID] || c.Partition != receipt.Partition || c.ReceiptID != receipt.ID || len(c.SourceRefs) == 0 || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(receipt.CreatedAt) || (c.Kind != "knowledge" && c.Kind != "working_preference" && c.Kind != "task" && c.Kind != "procedure") || strings.TrimSpace(c.Text) == "" {
				return errors.New("invalid dream candidate")
			}
			seen[c.ID] = true
			c.Status = "pending"
			if c.CreatedAt.IsZero() {
				c.CreatedAt = receipt.CreatedAt
			}
			payload, _ := json.Marshal(c)
			if _, err := tx.ExecContext(ctx, "INSERT INTO dream_candidates(id,receipt_id,person_id,agent_id,audience_id,scope,status,payload) VALUES(?,?,?,?,?,?,?,?)", c.ID, c.ReceiptID, c.Partition.PersonID, c.Partition.AgentID, c.Partition.AudienceID, c.Partition.Scope, c.Status, payload); err != nil {
				return err
			}
		}
		receipt.Status = "extracted"
		receipt.Error = ""
		payload, _ := json.Marshal(receipt)
		_, err := tx.ExecContext(ctx, "UPDATE dream_receipts SET status=?,payload=? WHERE id=? AND status='scanned'", receipt.Status, payload, receipt.ID)
		return err
	})
	return receipt, err
}

func (s *Store) RecordDreamExtractionFailure(ctx context.Context, receiptID, message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("dream extraction failure message is required")
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		var r DreamReceipt
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM dream_receipts WHERE id=?", receiptID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Status != "scanned" || !s.matchesPartition(r.Partition.AgentID, r.Partition.AudienceID) {
			return errors.New("dream receipt is not retryable")
		}
		r.Error = message
		payload, _ := json.Marshal(r)
		_, err := tx.ExecContext(ctx, "UPDATE dream_receipts SET payload=? WHERE id=? AND status='scanned'", payload, r.ID)
		return err
	})
}

func (s *Store) DreamCandidates(ctx context.Context, p DreamPartition, status string, limit int) ([]DreamCandidate, error) {
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid bounded dream candidate query")
	}
	query := "SELECT payload FROM dream_candidates WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=?"
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
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DreamCandidate(ctx context.Context, id string) (DreamCandidate, error) {
	var c DreamCandidate
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM dream_candidates WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err == nil && (!s.matchesPartition(c.Partition.AgentID, c.Partition.AudienceID)) {
		err = errors.New("dream candidate belongs to another partition")
	}
	return c, err
}

func (s *Store) DreamReceipt(ctx context.Context, id string) (DreamReceipt, error) {
	var r DreamReceipt
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM dream_receipts WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	if err == nil && !s.matchesPartition(r.Partition.AgentID, r.Partition.AudienceID) {
		err = errors.New("dream receipt belongs to another partition")
	}
	return r, err
}

func (s *Store) MarkDreamSkillPromoted(ctx context.Context, id, actor, skillID string) error {
	if actor == "" || skillID == "" {
		return errors.New("Skill promotion requires actor and Skill ID")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		var c DreamCandidate
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM dream_candidates WHERE id=?", id).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if c.Kind != "procedure" || c.Status != "pending" || !s.matchesPartition(c.Partition.AgentID, c.Partition.AudienceID) {
			return errors.New("candidate is not a promotable procedure")
		}
		now := time.Now().UTC()
		c.Status, c.SkillID, c.ReviewActor, c.ReviewedAt = "promoted", skillID, actor, &now
		payload, _ := json.Marshal(c)
		_, err := tx.ExecContext(ctx, "UPDATE dream_candidates SET status=?,payload=? WHERE id=? AND status='pending'", c.Status, payload, id)
		return err
	})
}

func (s *Store) ReviewDreamCandidate(ctx context.Context, id, actor, note, decision, expectedSourceDigest string) (DreamCandidate, error) {
	var out DreamCandidate
	if decision == "accept" && s.files == nil {
		return out, errors.New("dream acceptance requires versioned file memory")
	}
	if actor == "" || note == "" || (decision != "accept" && decision != "reject") {
		return out, errors.New("dream review requires actor, note and accept or reject")
	}
	err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM dream_candidates WHERE id=?", id).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		var receipt DreamReceipt
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM dream_receipts WHERE id=?", out.ReceiptID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Partition != out.Partition {
			return errors.New("dream candidate receipt is invalid")
		}
		if decision == "accept" && (expectedSourceDigest == "" || expectedSourceDigest != receipt.SourceDigest) {
			return errors.New("dream source digest changed or was not revalidated")
		}
		if out.Status != "pending" {
			return errors.New("dream candidate is already reviewed")
		}
		if !s.matchesPartition(out.Partition.AgentID, out.Partition.AudienceID) {
			return errors.New("dream candidate belongs to another partition")
		}
		now := time.Now().UTC()
		out.ReviewedAt = &now
		out.ReviewActor, out.ReviewNote = actor, note
		if decision == "reject" {
			out.Status = "rejected"
		} else {
			if out.Kind == "procedure" {
				return errors.New("procedure requires explicit Skill promotion")
			}
			if !out.ExpiresAt.After(now) {
				return errors.New("expired dream candidate cannot be accepted")
			}
			var existingID string
			err := tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND sender_id=? AND text=?", out.Partition.AgentID, out.Partition.AudienceID, out.Partition.Scope, out.Partition.PersonID, out.Text).Scan(&existingID)
			if err == nil {
				m, e := s.readMemory(ctx, tx, existingID)
				if e != nil {
					return e
				}
				if m.Status != "active" || expired(m, now) {
					return errors.New("duplicate target is not eligible")
				}
				if m.ExpiresAt != nil && out.ExpiresAt.After(*m.ExpiresAt) {
					return errors.New("duplicate cannot extend or replace existing expiry")
				}
				out.MemoryID = m.ID
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			} else {
				kind := map[string]string{"working_preference": "preference", "knowledge": "project_fact", "task": "active_task"}[out.Kind]
				m := Memory{SenderID: out.Partition.PersonID, AgentID: out.Partition.AgentID, AudienceID: out.Partition.AudienceID, ID: "dream-memory-" + out.ID, Scope: out.Partition.Scope, Kind: kind, Text: out.Text, Status: "active", Revision: 1, ExternalReceiptID: out.ReceiptID, CreatedAt: now, SourceType: "dream_reviewed", CandidateID: out.ID, ExpiresAt: &out.ExpiresAt, ReviewNote: note}
				if receipt.Attribution == "host_observed" {
					m.SourceRunID = receipt.CanonicalRunID
				}
				m.RevisionDecision, _ = json.Marshal(map[string]any{"actor": actor, "source_refs": out.SourceRefs, "inferred": out.Inferred})
				if err = persistMemory(ctx, tx, m, true); err != nil {
					return err
				}
				out.MemoryID = m.ID
			}
			if receipt.Attribution == "host_observed" && receipt.CanonicalRunID != "" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?) ON CONFLICT(memory_id,run_id) DO NOTHING", out.MemoryID, receipt.CanonicalRunID); err != nil {
					return err
				}
			}
			out.Status = "accepted"
		}
		payload, _ := json.Marshal(out)
		_, err := tx.ExecContext(ctx, "UPDATE dream_candidates SET status=?,payload=? WHERE id=? AND status='pending'", out.Status, payload, id)
		return err
	})
	return out, err
}

func (s *Store) DreamMemories(ctx context.Context, p DreamPartition, now time.Time, limit int) ([]Memory, error) {
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid bounded dream memory query")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND sender_id=? AND status='active' AND (expires_at IS NULL OR expires_at>?) AND COALESCE(json_extract(payload,'$.source_type'),'')='dream_reviewed' ORDER BY id LIMIT ?", p.AgentID, p.AudienceID, p.Scope, p.PersonID, timestamp(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ArchiveExpiredDream(ctx context.Context, p DreamPartition, now time.Time, limit int) (int, error) {
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || limit < 1 || limit > 200 {
		return 0, errors.New("invalid archive limit")
	}
	count := 0
	err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND sender_id=? AND status='active' AND expires_at IS NOT NULL AND expires_at<=? AND COALESCE(json_extract(payload,'$.source_type'),'')='dream_reviewed' ORDER BY id LIMIT ?", p.AgentID, p.AudienceID, p.Scope, p.PersonID, timestamp(now), limit)
		if err != nil {
			return err
		}
		items, err := scanMemories(rows)
		err = errors.Join(err, rows.Close())
		if err != nil {
			return err
		}
		for _, m := range items {
			m.Status = "invalidated"
			m.Revision++
			m.ReviewNote = "archived after dream TTL expiry"
			if err = persistMemory(ctx, tx, m, false); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

func (s *Store) RetireDreamMemory(ctx context.Context, p DreamPartition, id, actor, note string, expectedRevision int) error {
	return s.changeDreamMemory(ctx, p, id, actor, note, expectedRevision, false)
}
func (s *Store) RestoreDreamMemory(ctx context.Context, p DreamPartition, id, actor, note string, expectedRevision int) error {
	return s.changeDreamMemory(ctx, p, id, actor, note, expectedRevision, true)
}
func (s *Store) changeDreamMemory(ctx context.Context, p DreamPartition, id, actor, note string, expectedRevision int, restore bool) error {
	if !validDreamPartition(p) || !s.matchesPartition(p.AgentID, p.AudienceID) || id == "" || actor == "" || strings.TrimSpace(note) == "" || expectedRevision < 1 {
		return errors.New("dream memory transition requires exact partition, actor, note and revision")
	}
	return s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		m, err := s.readMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.SenderID != p.PersonID || m.Scope != p.Scope || m.SourceType != "dream_reviewed" || m.Revision != expectedRevision {
			return errors.New("dream memory revision or partition changed")
		}
		now := time.Now().UTC()
		action := "retire"
		if restore {
			action = "restore"
			if m.Status != "invalidated" || m.ExpiresAt == nil || !m.ExpiresAt.After(now) {
				return errors.New("expired or non-retired dream memory cannot be restored")
			}
			m.Status = "active"
		} else {
			if m.Status != "active" {
				return errors.New("only active dream memory can be retired")
			}
			m.Status = "invalidated"
		}
		m.Revision++
		m.ReviewNote = note
		m.RevisionDecision, _ = json.Marshal(map[string]any{"policy": "dream-lifecycle-v1", "action": action, "actor": actor, "at": now})
		return persistMemory(ctx, tx, m, false)
	})
}

func (p DreamPartition) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", p.PersonID, p.AgentID, p.AudienceID, p.Scope)
}
