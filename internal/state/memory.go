package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

func readMemory(ctx context.Context, q rowQuerier, id string) (Memory, error) {
	var m Memory
	var payload []byte
	var agentID, audienceID, senderID string
	err := q.QueryRowContext(ctx, "SELECT payload,agent_id,audience_id,sender_id FROM memories WHERE id=?", id).Scan(&payload, &agentID, &audienceID, &senderID)
	if err == nil {
		err = json.Unmarshal(payload, &m)
		if err == nil {
			m.AgentID, m.AudienceID, m.SenderID = agentID, audienceID, senderID
		}
	}
	return m, wrap("read memory", err)
}

func (s *Store) Memory(ctx context.Context, id string) (Memory, error) {
	if s.files != nil {
		var m Memory
		err := s.memoryTransaction(ctx, func(tx *sql.Tx) error { var err error; m, err = s.readMemory(ctx, tx, id); return err })
		return m, err
	}
	return s.readMemory(ctx, s.db, id)
}

func (s *Store) Memories(ctx context.Context, scope string) ([]Memory, error) {
	if s.files != nil {
		var memories []Memory
		err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
			agentID, audienceID := s.Partition()
			rows, err := tx.QueryContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND status IN ('active','conflicted') AND (expires_at IS NULL OR expires_at>?) ORDER BY rowid", agentID, audienceID, scope, timestamp(time.Now()))
			if err != nil {
				return err
			}
			memories, err = scanMemories(rows)
			return errors.Join(err, rows.Close())
		})
		return memories, err
	}
	agentID, audienceID := s.Partition()
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND status IN ('active','conflicted') AND (expires_at IS NULL OR expires_at>?) ORDER BY rowid LIMIT 201", agentID, audienceID, scope, timestamp(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memories, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if len(memories) > 200 {
		return nil, errors.New("scope exceeds MVP decision limit of 200 memories; narrower candidate retrieval is required")
	}
	return memories, nil
}

func scanMemories(rows *sql.Rows) ([]Memory, error) {
	memories := []Memory{}
	for rows.Next() {
		var payload []byte
		var m Memory
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil, err
		}
		m.AgentID, m.AudienceID = normalizedPartition(m.AgentID, m.AudienceID)
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

func (s *Store) SessionMemories(ctx context.Context, id string) ([]Memory, error) {
	if _, err := s.readSession(ctx, s.db, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM runs WHERE session_id=? ORDER BY created_at,rowid", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memories []Memory
	positions := map[string]int{}
	for rows.Next() {
		var payload []byte
		var run Run
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &run); err != nil {
			return nil, err
		}
		for _, m := range run.Context {
			if i, ok := positions[m.ID]; ok {
				memories[i] = m
			} else {
				positions[m.ID] = len(memories)
				memories = append(memories, m)
			}
		}
	}
	return memories, rows.Err()
}

func (s *Store) Search(ctx context.Context, scope, query string, limit int) ([]Memory, error) {
	result, err := s.Retrieve(ctx, scope, query, limit)
	return result.Matches, err
}

// searchTokens uses the same representation at write and query time. CJK
// bigrams make short Chinese words searchable without an external dictionary.
// A single CJK rune cannot provide the two-rune match supported by this MVP.
func searchTokens(text string) []string {
	var tokens []string
	seen := map[string]bool{}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			tokens = append(tokens, t)
		}
	}
	var word, cjk []rune
	flushWord := func() { add(string(word)); word = nil }
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			add(string(cjk[i : i+2]))
		}
		cjk = nil
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			flushWord()
			cjk = append(cjk, r)
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			flushCJK()
			word = append(word, r)
		} else {
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return tokens
}

func persistMemory(ctx context.Context, tx *sql.Tx, m Memory, isNew bool) error {
	if len(m.ObjectAudit) > 0 {
		if len(m.Objects) == 0 || !json.Valid(m.ObjectAudit) {
			return errors.New("audited object extraction requires subjects")
		}
		if err := validateMemoryObjects(m.Objects); err != nil {
			return err
		}
	}
	m.AgentID, m.AudienceID = normalizedPartition(m.AgentID, m.AudienceID)
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var expiry any
	if m.ExpiresAt != nil {
		expiry = timestamp(*m.ExpiresAt)
	}
	if isNew {
		_, err = tx.ExecContext(ctx, "INSERT INTO memories(id,agent_id,audience_id,sender_id,scope,text,status,expires_at,payload) VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.AgentID, m.AudienceID, m.SenderID, m.Scope, m.Text, m.Status, expiry, payload)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE memories SET text=?,status=?,expires_at=?,payload=? WHERE id=?", m.Text, m.Status, expiry, payload, m.ID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO memory_revisions(memory_id,revision,payload) VALUES(?,?,?)", m.ID, m.Revision, payload); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM memory_fts WHERE memory_id=?", m.ID); err != nil {
		return err
	}
	if recallEligible(m, time.Now()) {
		if _, err = tx.ExecContext(ctx, "INSERT INTO memory_fts(memory_id,tokens) VALUES(?,?)", m.ID, strings.Join(searchTokens(m.Text), " ")); err != nil {
			return err
		}
	}
	return persistAnnotation(ctx, tx, m)
}

func (s *Store) FinalizeMemory(ctx context.Context, id string, updates []MemoryUpdate, decision json.RawMessage, expectedGeneration ...string) error {
	if len(expectedGeneration) > 1 {
		return errors.New("memory finalization accepts one expected generation")
	}
	if len(decision) == 0 || !json.Valid(decision) {
		return errors.New("memory finalization requires valid raw decision JSON")
	}
	return s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(expectedGeneration) == 1 && (s.files != nil || expectedGeneration[0] != "") {
			fingerprint, err := memoryFingerprintTx(ctx, tx, []string{run.Scope}, run.AgentID, run.AudienceID)
			if err != nil {
				return err
			}
			if expectedGeneration[0] == "" || expectedGeneration[0] != fingerprint {
				return errors.New("memory publication snapshot changed; compare the user statement again")
			}
		}
		if len(run.Projects) > 1 {
			return errors.New("automatic memory writeback is disabled for multi-project runs")
		}
		if run.ExecutionStatus != "completed" || (run.MemoryStatus != "pending" && run.MemoryStatus != "failed") {
			return errors.New("run is not eligible for memory finalization; finalized work is not replayed")
		}
		changed := false
		for _, update := range updates {
			m := update.Memory
			if err := s.assignPartition(&m.AgentID, &m.AudienceID); err != nil {
				return err
			}
			m.SenderID = ""
			if run.Source != nil {
				m.SenderID = run.Source.SenderID
			}
			if m.SourceType != "" && m.SourceType != "user_statement" || m.CandidateID != "" || len(m.EvidenceIDs) > 0 || len(m.Steps) > 0 || len(m.Entities) > 0 || m.Conditions != "" || m.ExpiresAt != nil || m.ReviewNote != "" {
				return errors.New("user statement finalization cannot claim experience provenance")
			}
			m.SourceType = "user_statement"
			if m.ID == "" || m.Scope != run.Scope || m.SourceRunID != run.ID || m.Text != run.Input || strings.TrimSpace(m.Text) == "" {
				return errors.New("memory must retain exact user input, completed source run and scope")
			}
			if m.Kind != "preference" && m.Kind != "constraint" && m.Kind != "project_fact" && m.Kind != "decision" {
				return fmt.Errorf("unsupported memory kind %q", m.Kind)
			}
			if m.Status != "" && m.Status != "active" && m.Status != "conflicted" {
				return fmt.Errorf("invalid memory status %q", m.Status)
			}
			if m.Revision != 0 && m.Revision != 1 {
				return errors.New("new memory revision must be 1")
			}
			if len(update.DuplicateIDs) > 0 {
				if len(update.ConflictIDs) > 0 {
					return errors.New("duplicate evidence cannot bypass a conflict")
				}
				for _, duplicateID := range update.DuplicateIDs {
					existing, err := s.readMemory(ctx, tx, duplicateID)
					if err != nil {
						return err
					}
					if existing.SenderID != m.SenderID || existing.Scope != run.Scope || !recallEligible(existing, time.Now()) || existing.SourceType == "reviewed_experience" {
						return errors.New("duplicate evidence requires active user-statement memory in the same scope")
					}
					if len(m.ObjectAudit) > 0 && !sameObjectIdentities(existing.Objects, m.Objects) {
						return errors.New("duplicate object context differs; explicitly repair the existing memory")
					}
					if _, err = tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?) ON CONFLICT(memory_id,run_id) DO NOTHING", existing.ID, run.ID); err != nil {
						return err
					}
				}
				continue
			}
			var existingID string
			err = tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND sender_id=? AND text=?", m.AgentID, m.AudienceID, m.Scope, m.SenderID, m.Text).Scan(&existingID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			isNew := errors.Is(err, sql.ErrNoRows)
			if !isNew {
				proposedObjects, proposedAudit := m.Objects, m.ObjectAudit
				m, err = s.readMemory(ctx, tx, existingID)
				if err != nil {
					return err
				}
				if len(proposedAudit) > 0 && !sameObjectIdentities(m.Objects, proposedObjects) {
					return errors.New("exact duplicate object context differs; explicitly repair the existing memory")
				}
				if m.SourceType == "reviewed_experience" || m.Status == "invalidated" || expired(m, time.Now()) {
					return errors.New("exact duplicate is not an eligible user statement")
				}
			}
			conflicts := map[string]Memory{}
			selfConflict := false
			for _, conflictID := range update.ConflictIDs {
				if conflictID == m.ID {
					if isNew {
						return errors.New("new memory cannot conflict with itself")
					}
					selfConflict = true
					continue
				}
				other, readErr := s.readMemory(ctx, tx, conflictID)
				if readErr != nil {
					return readErr
				}
				if other.Scope != run.Scope || other.Status == "invalidated" || expired(other, time.Now()) {
					return errors.New("memory conflict requires eligible memory in the same project scope")
				}
				conflicts[other.ID] = other
			}
			if isNew {
				m.Status = "active"
				m.Revision = 1
				m.CreatedAt = time.Now().UTC()
				if len(conflicts) > 0 {
					m.Status = "conflicted"
				}
				if err = persistMemory(ctx, tx, m, true); err != nil {
					return err
				}
				changed = true
			} else if (len(conflicts) > 0 || selfConflict) && m.Status != "conflicted" {
				m.Status = "conflicted"
				m.Revision++
				if err = persistMemory(ctx, tx, m, false); err != nil {
					return err
				}
				changed = true
			}
			for _, other := range conflicts {
				if other.Status != "conflicted" {
					other.Status = "conflicted"
					other.Revision++
					if err = persistMemory(ctx, tx, other, false); err != nil {
						return err
					}
					changed = true
				}
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?) ON CONFLICT(memory_id,run_id) DO NOTHING", m.ID, run.ID); err != nil {
				return err
			}
		}
		run.Decision = append(json.RawMessage(nil), decision...)
		run.MemoryError = ""
		run.MemoryStatus = "unchanged"
		if changed {
			run.MemoryStatus = "saved"
		}
		return writeRun(ctx, tx, run)
	})
}
