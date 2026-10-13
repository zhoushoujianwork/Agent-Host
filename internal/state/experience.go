package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
)

func timestamp(t time.Time) string                { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func expired(m Memory, now time.Time) bool        { return m.ExpiresAt != nil && !m.ExpiresAt.After(now) }
func recallEligible(m Memory, now time.Time) bool { return m.Status == "active" && !expired(m, now) }

// EvidenceFromEvents binds only native execution calls and results. Extractor
// invocations are a separate phase and cannot become evidence for themselves.
func EvidenceFromEvents(events []event.Event) ([]ToolEvidence, error) {
	calls := map[string]*ToolEvidence{}
	var order []string
	var lastSeq int64
	for _, e := range events {
		if e.Phase != "execute" || e.Data == nil || e.Data["tool"] == nil {
			continue
		}
		if e.Seq <= lastSeq {
			return nil, errors.New("tool evidence journal sequence is not increasing")
		}
		lastSeq = e.Seq
		b, err := json.Marshal(e.Data["tool"])
		if err != nil {
			return nil, fmt.Errorf("marshal tool observation: %w", err)
		}
		var o harness.ToolObservation
		if err := json.Unmarshal(b, &o); err != nil {
			return nil, fmt.Errorf("read tool observation: %w", err)
		}
		if strings.TrimSpace(o.ID) == "" {
			return nil, errors.New("tool observation has no ID")
		}
		switch o.Stage {
		case "call":
			if calls[o.ID] != nil {
				return nil, fmt.Errorf("duplicate tool call %s", o.ID)
			}
			if strings.TrimSpace(o.Name) == "" || !json.Valid(o.Input) {
				return nil, fmt.Errorf("tool call %s has no name or valid input", o.ID)
			}
			calls[o.ID] = &ToolEvidence{ID: o.ID, Name: o.Name, Input: append(json.RawMessage(nil), o.Input...), CallSeq: e.Seq}
			order = append(order, o.ID)
		case "result":
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(b, &fields); err != nil {
				return nil, err
			}
			var observedError *bool
			if err := json.Unmarshal(fields["is_error"], &observedError); err != nil || observedError == nil {
				return nil, fmt.Errorf("tool result %s has no observed error status", o.ID)
			}
			call := calls[o.ID]
			if call == nil || call.ResultSeq != 0 {
				return nil, fmt.Errorf("tool result %s has no unique preceding call", o.ID)
			}
			if o.Name != "" && call.Name != o.Name {
				return nil, fmt.Errorf("tool result %s name mismatch", o.ID)
			}
			if len(o.Result) > 0 && !json.Valid(o.Result) {
				return nil, fmt.Errorf("tool result %s has invalid result JSON", o.ID)
			}
			call.Result = append(json.RawMessage(nil), o.Result...)
			call.ResultSeq = e.Seq
			call.IsError = o.IsError
		default:
			return nil, fmt.Errorf("tool %s has invalid stage %q", o.ID, o.Stage)
		}
	}
	result := make([]ToolEvidence, 0, len(order))
	for _, id := range order {
		if calls[id].ResultSeq == 0 {
			return nil, fmt.Errorf("tool call %s has no observed result", id)
		}
		result = append(result, *calls[id])
	}
	return result, nil
}

type rowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readEvidence(ctx context.Context, q rowsQuerier, runID string) ([]ToolEvidence, error) {
	rows, err := q.QueryContext(ctx, "SELECT payload FROM events WHERE run_id=? ORDER BY seq", runID)
	if err != nil {
		return nil, err
	}
	var events []event.Event
	for rows.Next() {
		var b []byte
		var e event.Event
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(b, &e); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, e)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	return EvidenceFromEvents(events)
}

func (s *Store) ToolEvidence(ctx context.Context, runID string) ([]ToolEvidence, error) {
	if _, err := s.readRun(ctx, s.db, runID); err != nil {
		return nil, err
	}
	return readEvidence(ctx, s.db, runID)
}

func validateDraft(d ExperienceDraft, evidence []ToolEvidence) error {
	for _, field := range []struct {
		value string
		max   int
	}{{d.Title, 256}, {d.Summary, 4096}, {d.Conditions, 2048}} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > field.max {
			return errors.New("experience title, summary and conditions must be nonempty and within byte limits")
		}
	}
	if len(d.Steps) == 0 || len(d.Steps) > 12 || len(d.EvidenceIDs) == 0 || len(d.EvidenceIDs) > 32 {
		return errors.New("experience exceeds title, step or evidence limits")
	}
	for _, step := range d.Steps {
		if strings.TrimSpace(step) == "" || len(step) > 2048 {
			return errors.New("experience step must be nonempty and at most 2048 bytes")
		}
	}
	available := map[string]bool{}
	for _, e := range evidence {
		if executionExperienceEligible(e) {
			available[e.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range d.EvidenceIDs {
		if !available[id] || seen[id] {
			return fmt.Errorf("experience evidence %q is unknown or repeated", id)
		}
		seen[id] = true
	}
	return nil
}

// ExecutionExperienceEvidence excludes reference documents and tool catalog
// discovery from the execution experience policy. All observations remain
// available in the original journal.
func ExecutionExperienceEvidence(evidence []ToolEvidence) []ToolEvidence {
	var eligible []ToolEvidence
	for _, e := range evidence {
		if executionExperienceEligible(e) {
			eligible = append(eligible, e)
		}
	}
	return eligible
}

func executionExperienceEligible(e ToolEvidence) bool {
	return e.Name != "ToolSearch" && !strings.HasPrefix(e.Name, "mcp__email__") && !strings.HasPrefix(e.Name, "mcp__knowledge_") && !strings.HasPrefix(e.Name, "mcp__conversation_history__") && !strings.HasPrefix(e.Name, "mcp__host_context__")
}

// EvidenceEntities derives project/file identities only from typed native tool
// input. Free text, shell command strings and the generated draft cannot invent
// entities. Existing symlinks are resolved before enforcing the project bound.
func EvidenceEntities(scope string, draft ExperienceDraft, evidence []ToolEvidence) ([]string, error) {
	root, err := filepath.Abs(scope)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if actual, err := filepath.EvalSymlinks(root); err == nil {
		root = actual
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return EvidenceEntitiesAt(root, root, draft, evidence)
}

// EvidenceEntitiesAt separates the durable memory identity from the source run's
// filesystem boundary. Paths always describe the original execution workspace.
func EvidenceEntitiesAt(scope, workspace string, draft ExperienceDraft, evidence []ToolEvidence) ([]string, error) {
	if err := validateDraft(draft, evidence); err != nil {
		return nil, err
	}
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(workspace) == "" {
		return nil, errors.New("evidence scope and workspace are required")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if actual, err := filepath.EvalSymlinks(root); err == nil {
		root = actual
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	projectEntity := scope
	if scope == workspace {
		projectEntity = root
	}
	if !strings.HasPrefix(projectEntity, "project:") {
		projectEntity = "project:" + projectEntity
	}
	set := map[string]bool{projectEntity: true}
	selected := map[string]bool{}
	for _, id := range draft.EvidenceIDs {
		selected[id] = true
	}
	for _, e := range evidence {
		if !selected[e.ID] {
			continue
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal(e.Input, &input); err != nil {
			return nil, fmt.Errorf("tool input %s must be an object: %w", e.ID, err)
		}
		for _, key := range []string{"file_path", "filePath", "path"} {
			b, ok := input[key]
			if !ok {
				continue
			}
			var path string
			if err := json.Unmarshal(b, &path); err != nil {
				continue
			}
			if strings.TrimSpace(path) == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			path = filepath.Clean(path)
			// Resolve the longest existing ancestor, also for newly-created files.
			ancestor := path
			var suffix []string
			for {
				actual, resolveErr := filepath.EvalSymlinks(ancestor)
				if resolveErr == nil {
					path = actual
					for i := len(suffix) - 1; i >= 0; i-- {
						path = filepath.Join(path, suffix[i])
					}
					break
				}
				if !errors.Is(resolveErr, os.ErrNotExist) {
					return nil, resolveErr
				}
				parent := filepath.Dir(ancestor)
				if parent == ancestor {
					break
				}
				suffix = append(suffix, filepath.Base(ancestor))
				ancestor = parent
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil, err
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			set["file:"+filepath.ToSlash(path)] = true
		}
	}
	entities := make([]string, 0, len(set))
	for entity := range set {
		entities = append(entities, entity)
	}
	sort.Strings(entities)
	return entities, nil
}

// ExperienceText gives the reviewed record a stable searchable representation.
func ExperienceText(d ExperienceDraft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\nApplicable when: %s\n", d.Title, d.Summary, d.Conditions)
	for i, step := range d.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	return strings.TrimSpace(b.String())
}

func readCandidate(ctx context.Context, q rowQuerier, id string) (Candidate, error) {
	var candidate Candidate
	var b []byte
	var agentID, audienceID string
	err := q.QueryRowContext(ctx, "SELECT payload,agent_id,audience_id FROM candidates WHERE id=?", id).Scan(&b, &agentID, &audienceID)
	if err == nil {
		err = json.Unmarshal(b, &candidate)
		if err == nil {
			candidate.AgentID, candidate.AudienceID = agentID, audienceID
		}
	}
	return candidate, wrap("read candidate", err)
}

func writeCandidate(ctx context.Context, tx *sql.Tx, candidate Candidate, isNew bool) error {
	b, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if isNew {
		_, err = tx.ExecContext(ctx, "INSERT INTO candidates(id,agent_id,audience_id,run_id,scope,status,payload) VALUES(?,?,?,?,?,?,?)", candidate.ID, candidate.AgentID, candidate.AudienceID, candidate.RunID, candidate.Scope, candidate.Status, b)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE candidates SET status=?,payload=? WHERE id=?", candidate.Status, b, candidate.ID)
	}
	return err
}

func (s *Store) SaveCandidates(ctx context.Context, runID string, candidates []Candidate, audit json.RawMessage) error {
	if len(candidates) > 3 || !json.Valid(audit) {
		return errors.New("candidate extraction requires valid audit JSON and at most three candidates")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if len(run.Projects) > 1 {
			return errors.New("automatic experience extraction is disabled for multi-project runs")
		}
		if run.ExecutionStatus != "completed" || (run.ExperienceStatus != "pending" && run.ExperienceStatus != "failed") {
			return errors.New("run is not eligible for experience extraction")
		}
		evidence, err := readEvidence(ctx, tx, runID)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if err := s.assignPartition(&candidate.AgentID, &candidate.AudienceID); err != nil {
				return err
			}
			if candidate.ID == "" || candidate.RunID != runID || candidate.Scope != run.Scope || candidate.Status != "" && candidate.Status != "pending" || candidate.ReviewedAt != nil || candidate.MemoryID != "" || candidate.ReviewNote != "" {
				return errors.New("candidate must belong to this run and scope and have no prior review")
			}
			if strings.TrimSpace(candidate.Model) == "" || !json.Valid(candidate.Assessment) {
				return errors.New("candidate requires extraction model and assessment JSON")
			}
			entities, err := EvidenceEntitiesForRun(run, candidate.Draft, evidence)
			if err != nil {
				return err
			}
			candidate.Entities = entities
			candidate.Status = "pending"
			candidate.CreatedAt = time.Now().UTC()
			if err := writeCandidate(ctx, tx, candidate, true); err != nil {
				return err
			}
		}
		run.ExperienceStatus = "empty"
		if len(candidates) > 0 {
			run.ExperienceStatus = "review_pending"
		}
		run.ExperienceError = ""
		run.ExperienceDecision = append(json.RawMessage(nil), audit...)
		return writeRun(ctx, tx, run)
	})
}

func (s *Store) ExperienceFailure(ctx context.Context, runID, errorText string) error {
	if strings.TrimSpace(errorText) == "" {
		return errors.New("experience failure requires error details")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		run, err := s.readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "completed" || run.ExperienceStatus != "pending" && run.ExperienceStatus != "failed" {
			return errors.New("run is not eligible for experience failure")
		}
		run.ExperienceStatus = "failed"
		run.ExperienceError = errorText
		return writeRun(ctx, tx, run)
	})
}

func (s *Store) GetCandidate(ctx context.Context, id string) (Candidate, error) {
	return s.readCandidate(ctx, s.db, id)
}

func (s *Store) Candidates(ctx context.Context, scope, runID, status string) ([]Candidate, error) {
	if scope == "" {
		return nil, errors.New("candidate scope is required")
	}
	if status == "all" {
		status = ""
	}
	if status != "" && status != "pending" && status != "accepted" && status != "rejected" {
		return nil, errors.New("invalid candidate status")
	}
	agentID, audienceID := s.Partition()
	query := "SELECT payload FROM candidates WHERE agent_id=? AND audience_id=? AND scope=?"
	args := []any{agentID, audienceID, scope}
	if runID != "" {
		if _, err := s.readRun(ctx, s.db, runID); err != nil {
			return nil, err
		}
		query += " AND run_id=?"
		args = append(args, runID)
	}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY rowid"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Candidate{}
	for rows.Next() {
		var b []byte
		var c Candidate
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		c.AgentID, c.AudienceID = normalizedPartition(c.AgentID, c.AudienceID)
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *Store) CandidateEvidence(ctx context.Context, id string) ([]ToolEvidence, error) {
	candidate, err := s.GetCandidate(ctx, id)
	if err != nil {
		return nil, err
	}
	evidence, err := s.ToolEvidence(ctx, candidate.RunID)
	if err != nil {
		return nil, err
	}
	if err := validateDraft(candidate.Draft, evidence); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, id := range candidate.Draft.EvidenceIDs {
		ids[id] = true
	}
	result := []ToolEvidence{}
	for _, e := range evidence {
		if ids[e.ID] {
			result = append(result, e)
		}
	}
	return result, nil
}

func validateSnapshot(ctx context.Context, tx *sql.Tx, scope string, expected map[string]int, now time.Time, agentID, audienceID string) (map[string]Memory, error) {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND status IN ('active','conflicted') AND (expires_at IS NULL OR expires_at>?) ORDER BY rowid LIMIT 101", agentID, audienceID, scope, timestamp(now))
	if err != nil {
		return nil, err
	}
	memories, scanErr := scanMemories(rows)
	err = errors.Join(scanErr, rows.Close())
	if err != nil {
		return nil, err
	}
	if len(memories) > 100 {
		return nil, errors.New("scope exceeds MVP review limit of 100 memories")
	}
	if len(memories) != len(expected) {
		return nil, errors.New("memory comparison snapshot changed; review again")
	}
	current := map[string]Memory{}
	for _, m := range memories {
		if expected[m.ID] != m.Revision {
			return nil, fmt.Errorf("memory %s comparison revision changed; review again", m.ID)
		}
		current[m.ID] = m
	}
	return current, nil
}

func (s *Store) AcceptCandidate(ctx context.Context, id string, accept CandidateAcceptance) (Memory, error) {
	var accepted Memory
	if accept.MemoryID == "" || !json.Valid(accept.Decision) {
		return accepted, errors.New("candidate acceptance requires a memory ID and valid decision JSON")
	}
	if len(accept.DuplicateIDs) > 0 && len(accept.ConflictIDs) > 0 {
		return accepted, errors.New("duplicate evidence cannot bypass a conflict")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		candidate, err := s.readCandidate(ctx, tx, id)
		if err != nil {
			return err
		}
		if candidate.Status != "pending" {
			return errors.New("candidate is already reviewed")
		}
		run, err := s.readRun(ctx, tx, candidate.RunID)
		if err != nil {
			return err
		}
		if run.ExecutionStatus != "completed" || candidate.Scope != run.Scope {
			return errors.New("candidate source run or scope is invalid")
		}
		evidence, err := readEvidence(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		if err := validateDraft(candidate.Draft, evidence); err != nil {
			return err
		}
		now := time.Now().UTC()
		if accept.ExpiresAt != nil && !accept.ExpiresAt.After(now) {
			return errors.New("memory expiration must be in the future")
		}
		current, err := validateSnapshot(ctx, tx, candidate.Scope, accept.ExpectedRevisions, now, candidate.AgentID, candidate.AudienceID)
		if err != nil {
			return err
		}
		conflicts := map[string]Memory{}
		for _, id := range accept.ConflictIDs {
			m, ok := current[id]
			if !ok {
				return fmt.Errorf("conflict memory %s is outside the eligible scope", id)
			}
			conflicts[id] = m
		}
		if len(accept.DuplicateIDs) > 0 {
			for _, id := range accept.DuplicateIDs {
				m, ok := current[id]
				if !ok || !recallEligible(m, now) || m.SourceType != "reviewed_experience" {
					return errors.New("experience duplicate must reference active reviewed experience in this scope")
				}
				if accept.ExpiresAt != nil && (m.ExpiresAt == nil || m.ExpiresAt.After(*accept.ExpiresAt)) {
					return errors.New("duplicate target outlives this review expiration; invalidate it or choose an independent record")
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?) ON CONFLICT(memory_id,run_id) DO NOTHING", m.ID, run.ID); err != nil {
					return err
				}
				if accepted.ID == "" {
					accepted = m
				}
			}
		} else {
			m := Memory{AgentID: candidate.AgentID, AudienceID: candidate.AudienceID, ID: accept.MemoryID, Scope: candidate.Scope, Kind: "procedure", Text: ExperienceText(candidate.Draft), Status: "active", Revision: 1, SourceRunID: candidate.RunID, CreatedAt: now, SourceType: "reviewed_experience", CandidateID: candidate.ID, Conditions: candidate.Draft.Conditions, Steps: append([]string(nil), candidate.Draft.Steps...), Entities: append([]string(nil), candidate.Entities...), EvidenceIDs: append([]string(nil), candidate.Draft.EvidenceIDs...), ExpiresAt: accept.ExpiresAt, ReviewNote: accept.Note}
			if run.Source != nil {
				m.SenderID = run.Source.SenderID
			}
			if len(conflicts) > 0 {
				m.Status = "conflicted"
			}
			// Exact text dedup must not silently revive revoked, expired or
			// differently sourced knowledge. Such cases need explicit review.
			var existingID string
			err = tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE agent_id=? AND audience_id=? AND scope=? AND sender_id=? AND text=?", m.AgentID, m.AudienceID, m.Scope, m.SenderID, m.Text).Scan(&existingID)
			if err == nil {
				return errors.New("exact memory text already exists; review its eligible duplicate explicitly")
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := persistMemory(ctx, tx, m, true); err != nil {
				return err
			}
			for _, other := range conflicts {
				if other.Status != "conflicted" {
					other.Status = "conflicted"
					other.Revision++
					if err := persistMemory(ctx, tx, other, false); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO memory_evidence(memory_id,run_id) VALUES(?,?)", m.ID, run.ID); err != nil {
				return err
			}
			accepted = m
		}
		candidate.Status = "accepted"
		candidate.MemoryID = accepted.ID
		candidate.ReviewedAt = &now
		candidate.ReviewNote = accept.Note
		// Preserve extraction assessment and review decision separately.
		assessment, err := json.Marshal(map[string]any{"extraction": candidate.Assessment, "acceptance": accept.Decision})
		if err != nil {
			return err
		}
		candidate.Assessment = assessment
		if err := writeCandidate(ctx, tx, candidate, false); err != nil {
			return err
		}
		return finishReview(ctx, tx, run)
	})
	if err != nil {
		return Memory{}, err
	}
	return accepted, nil
}

func finishReview(ctx context.Context, tx *sql.Tx, run Run) error {
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM candidates WHERE run_id=? AND status='pending'", run.ID).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		run.ExperienceStatus = "reviewed"
	}
	return writeRun(ctx, tx, run)
}

func (s *Store) RejectCandidate(ctx context.Context, id, note string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		candidate, err := s.readCandidate(ctx, tx, id)
		if err != nil {
			return err
		}
		if candidate.Status != "pending" {
			return errors.New("candidate is already reviewed")
		}
		run, err := s.readRun(ctx, tx, candidate.RunID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		candidate.Status = "rejected"
		candidate.ReviewedAt = &now
		candidate.ReviewNote = note
		if err := writeCandidate(ctx, tx, candidate, false); err != nil {
			return err
		}
		return finishReview(ctx, tx, run)
	})
}

func (s *Store) InvalidateMemory(ctx context.Context, id, note string) error {
	return s.InvalidateMemoryRevision(ctx, id, note, 0)
}

// InvalidateMemoryRevision rejects stale workbench actions atomically with the write.
// A zero revision retains the existing CLI behavior.
func (s *Store) InvalidateMemoryRevision(ctx context.Context, id, note string, revision int) error {
	if strings.TrimSpace(note) == "" {
		return errors.New("memory invalidation requires a reason")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, err := s.readMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Status != "active" && m.Status != "conflicted" {
			return errors.New("memory is already invalidated")
		}
		if revision != 0 && m.Revision != revision {
			return errors.New("memory revision changed; refresh before disabling")
		}
		m.Status = "invalidated"
		m.Revision++
		m.ReviewNote = note
		return persistMemory(ctx, tx, m, false)
	})
}
