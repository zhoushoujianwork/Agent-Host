package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const schemaAssistanceV13 = `
CREATE TABLE assistance_jobs(source_id TEXT PRIMARY KEY REFERENCES discovery_sources(id), payload BLOB NOT NULL);
CREATE TABLE matters(id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES discovery_sources(id), payload BLOB NOT NULL);
CREATE INDEX matters_source ON matters(source_id,id);
CREATE TABLE matter_revisions(matter_id TEXT NOT NULL REFERENCES matters(id), revision INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(matter_id,revision));
CREATE TABLE matter_evidence(matter_id TEXT NOT NULL REFERENCES matters(id), evidence_id TEXT NOT NULL REFERENCES discovery_evidence(id), PRIMARY KEY(matter_id,evidence_id));
CREATE TABLE suggestions(id TEXT PRIMARY KEY, matter_id TEXT NOT NULL UNIQUE REFERENCES matters(id), payload BLOB NOT NULL);
PRAGMA user_version=13;
`

var ErrContextUnavailable = errors.New("context is unavailable within the current task grants or eligibility")

// ContextRun exposes admitted metadata to the binding resolver. All subsequent
// content access uses the derived partition and exact single-project scope.
func (s *Store) ContextRun(ctx context.Context, id string) (Run, error) {
	r, err := readRun(ctx, s.db, id)
	r.AgentID, r.AudienceID = normalizedPartition(r.AgentID, r.AudienceID)
	return r, err
}

// Matters are source-derived, changing observations, never learned memories.
type MatterDraft struct {
	MatterID         string   `json:"matter_id"`
	Kind             string   `json:"kind"`
	Status           string   `json:"status"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	OpenItem         string   `json:"open_item"`
	EvidenceIDs      []string `json:"evidence_ids"`
	ExpectedRevision int      `json:"-"`
}

type Matter struct {
	ID            string      `json:"id"`
	SourceID      string      `json:"source_id"`
	ProjectID     string      `json:"project_id"`
	Role          string      `json:"role"`
	Revision      int         `json:"revision"`
	Draft         MatterDraft `json:"observation"`
	Model         string      `json:"model"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	OwnerResolved bool        `json:"owner_resolved"`
}

type MatterView struct {
	Matter     Matter               `json:"matter"`
	Coverage   *DiscoveryCollection `json:"coverage"`
	Fresh      bool                 `json:"fresh"`
	Limitation string               `json:"limitation"`
}

type AssistanceJob struct {
	Digest string          `json:"digest"`
	Model  string          `json:"model"`
	Error  string          `json:"error,omitempty"`
	Audit  json.RawMessage `json:"audit"`
	At     time.Time       `json:"at"`
}

type Suggestion struct {
	ID             string    `json:"id"`
	MatterID       string    `json:"matter_id"`
	ProjectID      string    `json:"project_id"`
	Revision       int       `json:"revision"`
	MatterRevision int       `json:"matter_revision"`
	Action         string    `json:"action"`
	Reason         string    `json:"reason"`
	Status         string    `json:"status"` // ready, postponed, dismissed, withdrawn, selected
	ReviewAt       time.Time `json:"review_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	RunID          string    `json:"run_id,omitempty"`
}

type PreparationSource struct {
	SuggestionID       string   `json:"suggestion_id"`
	SuggestionRevision int      `json:"suggestion_revision"`
	MatterID           string   `json:"matter_id"`
	MatterRevision     int      `json:"matter_revision"`
	EvidenceIDs        []string `json:"evidence_ids"`
}

func AssistanceDigest(evidence []DiscoveryEvidence) string {
	ids := make([]string, 0, len(evidence))
	for _, e := range evidence {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return discoveryHash(ids)
}

func (s *Store) AssistanceJob(ctx context.Context, sourceID string) (AssistanceJob, error) {
	return readAssistanceJob(ctx, s.db, sourceID)
}

func readAssistanceJob(ctx context.Context, q rowQuerier, sourceID string) (AssistanceJob, error) {
	var b []byte
	var j AssistanceJob
	err := q.QueryRowContext(ctx, "SELECT payload FROM assistance_jobs WHERE source_id=?", sourceID).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &j)
	}
	return j, err
}

func readMatter(ctx context.Context, q rowQuerier, id string) (Matter, error) {
	var b []byte
	var m Matter
	err := q.QueryRowContext(ctx, "SELECT payload FROM matters WHERE id=?", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	return m, err
}

func (s *Store) SourceMatters(ctx context.Context, sourceID string) ([]Matter, error) {
	_, audience := s.Partition()
	if audience != OwnerAudienceID {
		return nil, ErrContextUnavailable
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM matters WHERE source_id=? ORDER BY id LIMIT 101", sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Matter{}
	for rows.Next() {
		var b []byte
		var m Matter
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) > 100 {
		return nil, errors.New("source exceeds the 100-matter reconciliation limit")
	}
	return out, rows.Err()
}

// Reconciliation receives current source content only. Superseded citations
// retain a stable matter identity with scrubbed old prose; missing or withdrawn
// citations exclude the entire dependent observation from model input.
func (s *Store) ReconciliationMatters(ctx context.Context, sourceID string) ([]Matter, error) {
	all, err := s.SourceMatters(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	out := []Matter{}
	for _, m := range all {
		ids := []string{}
		changed := false
		available := true
		for _, id := range m.Draft.EvidenceIDs {
			var b []byte
			err := s.db.QueryRowContext(ctx, `SELECT current.payload FROM discovery_evidence old JOIN discovery_current pointer ON pointer.source_id=old.source_id AND pointer.message_id=old.message_id JOIN discovery_evidence current ON current.id=pointer.evidence_id WHERE old.id=? AND old.source_id=?`, id, sourceID).Scan(&b)
			var current DiscoveryEvidence
			if err != nil || json.Unmarshal(b, &current) != nil || current.Availability != "available" {
				available = false
				break
			}
			ids = append(ids, current.ID)
			changed = changed || current.ID != id
		}
		if !available {
			continue
		}
		if changed {
			m.Draft.EvidenceIDs = ids
			m.Draft.Title = ""
			m.Draft.Summary = ""
			m.Draft.OpenItem = ""
			m.Draft.Status = "uncertain"
		}
		out = append(out, m)
	}
	return out, nil
}

func currentDiscoveryEvidence(ctx context.Context, q rowQuerier, sourceID, id string) (DiscoveryEvidence, error) {
	var b []byte
	var e DiscoveryEvidence
	err := q.QueryRowContext(ctx, `SELECT e.payload FROM discovery_evidence e JOIN discovery_current c ON c.evidence_id=e.id WHERE e.id=? AND c.source_id=?`, id, sourceID).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &e)
	}
	if err != nil || e.Availability != "available" {
		return DiscoveryEvidence{}, ErrContextUnavailable
	}
	return e, nil
}

// Commit observations, their revisions and one suggestion per matter atomically.
// Failed analysis records a retryable job without changing matters or knowledge.
func (s *Store) SaveMatterExtraction(ctx context.Context, sourceID, digest, model string, drafts []MatterDraft, audit json.RawMessage, failure string) error {
	_, audience := s.Partition()
	if audience != OwnerAudienceID || digest == "" || len(drafts) > 100 || len(audit) > 128<<10 || !json.Valid(audit) || failure != "" && len(drafts) != 0 {
		return errors.New("invalid matter extraction result")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		source, err := readDiscoverySource(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if failure == "" && (source.LastCollection == nil || !source.LastCollection.Complete || source.LastCollection.Error != "" || model == "") {
			return errors.New("matter reconciliation requires complete collection and an identified model")
		}
		if failure == "" {
			rows, err := tx.QueryContext(ctx, "SELECT evidence_id FROM discovery_current WHERE source_id=? ORDER BY evidence_id", sourceID)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			if discoveryHash(ids) != digest {
				return errors.New("evidence changed during matter reconciliation; retry from current snapshots")
			}
		}
		seen := map[string]bool{}
		for _, d := range drafts {
			if d.Kind != "question" && d.Kind != "commitment" || d.Status != "open" && d.Status != "reported_resolved" && d.Status != "closed" && d.Status != "uncertain" && d.Status != "conflicted" || strings.TrimSpace(d.Title) == "" || len(d.Title) > 256 || strings.TrimSpace(d.Summary) == "" || len(d.Summary) > 2048 || len(d.OpenItem) > 2048 || d.Status == "open" && strings.TrimSpace(d.OpenItem) == "" || len(d.EvidenceIDs) < 1 || len(d.EvidenceIDs) > 32 {
				return errors.New("invalid bounded matter observation")
			}
			ids := map[string]bool{}
			var anchor DiscoveryEvidence
			for _, id := range d.EvidenceIDs {
				if ids[id] {
					return errors.New("duplicate matter citation")
				}
				ids[id] = true
				e, err := currentDiscoveryEvidence(ctx, tx, sourceID, id)
				if err != nil || e.ProjectID != source.ProjectID {
					return ErrContextUnavailable
				}
				if anchor.ID == "" || e.CreatedAt.Before(anchor.CreatedAt) || e.CreatedAt.Equal(anchor.CreatedAt) && e.MessageID < anchor.MessageID {
					anchor = e
				}
			}
			id := d.MatterID
			if id == "" {
				id = "matter-" + discoveryHash([]string{sourceID, anchor.MessageID, d.Kind})
			}
			if seen[id] {
				return errors.New("duplicate matter reconciliation")
			}
			seen[id] = true
			m, err := readMatter(ctx, tx, id)
			if errors.Is(err, sql.ErrNoRows) {
				if d.MatterID != "" || d.ExpectedRevision != 0 {
					return ErrContextUnavailable
				}
				m = Matter{ID: id, SourceID: sourceID, ProjectID: source.ProjectID, Role: "current_matter", CreatedAt: now}
			} else if err != nil {
				return err
			} else if m.SourceID != sourceID || d.MatterID == "" || d.ExpectedRevision != m.Revision {
				return errors.New("matter changed during reconciliation; rerun from current evidence")
			}
			d.MatterID = id
			m.Revision++
			m.Draft, m.Model, m.UpdatedAt = d, model, now
			b, _ := json.Marshal(m)
			if _, err = tx.ExecContext(ctx, "INSERT INTO matters(id,source_id,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", id, sourceID, b); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO matter_revisions(matter_id,revision,payload) VALUES(?,?,?)", id, m.Revision, b); err != nil {
				return err
			}
			for _, evidenceID := range d.EvidenceIDs {
				if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO matter_evidence(matter_id,evidence_id) VALUES(?,?)", id, evidenceID); err != nil {
					return err
				}
			}
			if err = refreshSuggestion(ctx, tx, m, now); err != nil {
				return err
			}
		}
		job := AssistanceJob{Digest: digest, Model: model, Error: failure, Audit: audit, At: now}
		b, _ := json.Marshal(job)
		_, err = tx.ExecContext(ctx, "INSERT INTO assistance_jobs(source_id,payload) VALUES(?,?) ON CONFLICT(source_id) DO UPDATE SET payload=excluded.payload", sourceID, b)
		return err
	})
}

func readSuggestion(ctx context.Context, q rowQuerier, id string) (Suggestion, error) {
	var b []byte
	var r Suggestion
	err := q.QueryRowContext(ctx, "SELECT payload FROM suggestions WHERE id=?", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
}

func writeSuggestion(ctx context.Context, tx *sql.Tx, r Suggestion) error {
	b, _ := json.Marshal(r)
	_, err := tx.ExecContext(ctx, "INSERT INTO suggestions(id,matter_id,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", r.ID, r.MatterID, b)
	return err
}

func refreshSuggestion(ctx context.Context, tx *sql.Tx, m Matter, now time.Time) error {
	id := "suggestion-" + discoveryHash(m.ID)
	r, err := readSuggestion(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		r = Suggestion{ID: id, MatterID: m.ID, ProjectID: m.ProjectID, Status: "ready"}
	} else if err != nil {
		return err
	}
	r.Revision++
	r.MatterRevision, r.UpdatedAt = m.Revision, now
	r.Action = "Prepare a cited brief and checklist"
	r.Reason = "An open " + m.Draft.Kind + " was observed in the selected conversation. Preparation can organize the evidence and remaining unknowns."
	if m.OwnerResolved || m.Draft.Status != "open" {
		if r.Status != "selected" && r.Status != "dismissed" {
			r.Status = "withdrawn"
		}
	} else if r.Status == "withdrawn" {
		r.Status = "ready"
	}
	return writeSuggestion(ctx, tx, r)
}

func (s *Store) MatterView(ctx context.Context, projectID string, sourceIDs []string, id string) (MatterView, error) {
	return s.matterView(ctx, s.db, projectID, sourceIDs, id)
}

func (s *Store) matterView(ctx context.Context, q rowQuerier, projectID string, sourceIDs []string, id string) (MatterView, error) {
	_, audience := s.Partition()
	if audience != OwnerAudienceID {
		return MatterView{}, ErrContextUnavailable
	}
	m, err := readMatter(ctx, q, id)
	if err != nil || m.ProjectID != projectID || !hasContextSource(sourceIDs, m.SourceID) {
		return MatterView{}, ErrContextUnavailable
	}
	source, err := readDiscoverySource(ctx, q, m.SourceID)
	if err != nil || source.ProjectID != projectID {
		return MatterView{}, ErrContextUnavailable
	}
	for _, id := range m.Draft.EvidenceIDs {
		if _, err := currentDiscoveryEvidence(ctx, q, m.SourceID, id); err != nil {
			return MatterView{}, ErrContextUnavailable
		}
	}
	v := MatterView{Matter: m, Coverage: source.LastCollection, Limitation: "Status is inferred from collected participant reports; silence does not establish a blocker or verified success."}
	if c := source.LastCollection; c != nil {
		job, err := readAssistanceJob(ctx, q, m.SourceID)
		v.Fresh = err == nil && job.Error == "" && !job.At.Before(c.At) && c.Complete && !c.Partial && c.Error == "" && !c.To.Before(c.At) && time.Since(c.At) < 24*time.Hour
	}
	if !v.Fresh {
		v.Limitation += " Collection or reconciliation is incomplete, failed or older than 24 hours; suggestions are unavailable."
	}
	return v, nil
}

func hasContextSource(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func (s *Store) MatterEvidence(ctx context.Context, projectID string, sourceIDs []string, id string) ([]DiscoveryEvidence, error) {
	v, err := s.MatterView(ctx, projectID, sourceIDs, id)
	if err != nil {
		return nil, err
	}
	out := []DiscoveryEvidence{}
	for _, id := range v.Matter.Draft.EvidenceIDs {
		e, err := currentDiscoveryEvidence(ctx, s.db, v.Matter.SourceID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Lexical overlap uses the same multilingual tokenizer as saved memory. Empty
// query lists scoped matters; it makes no claim about uncollected work.
func (s *Store) SearchMatters(ctx context.Context, projectID string, sourceIDs []string, query string, limit int) ([]MatterView, error) {
	if limit < 1 || limit > 20 || len(query) > 256 || len(sourceIDs) > 16 {
		return nil, errors.New("invalid matter search bounds")
	}
	_, audience := s.Partition()
	if audience != OwnerAudienceID {
		return nil, ErrContextUnavailable
	}
	tokens := searchTokens(query)
	out := []MatterView{}
	scores := map[string]int{}
	for _, sourceID := range sourceIDs {
		m, err := s.SourceMatters(ctx, sourceID)
		if err != nil {
			return nil, err
		}
		for _, item := range m {
			v, err := s.MatterView(ctx, projectID, sourceIDs, item.ID)
			if errors.Is(err, ErrContextUnavailable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			fields := []string{strings.ToLower(item.Draft.Title), strings.ToLower(item.Draft.OpenItem), strings.ToLower(item.Draft.Summary)}
			weights := []int{4, 3, 1}
			match := strings.TrimSpace(query) == ""
			for _, token := range tokens {
				for i, field := range fields {
					if strings.Contains(field, token) {
						match = true
						scores[item.ID] += weights[i]
					}
				}
			}
			if match {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if scores[out[i].Matter.ID] != scores[out[j].Matter.ID] {
			return scores[out[i].Matter.ID] > scores[out[j].Matter.ID]
		}
		if out[i].Matter.UpdatedAt.Equal(out[j].Matter.UpdatedAt) {
			return out[i].Matter.ID < out[j].Matter.ID
		}
		return out[i].Matter.UpdatedAt.After(out[j].Matter.UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) Suggestion(ctx context.Context, projectID string, sourceIDs []string, id string) (Suggestion, MatterView, error) {
	r, err := readSuggestion(ctx, s.db, id)
	if err != nil || r.ProjectID != projectID {
		return Suggestion{}, MatterView{}, ErrContextUnavailable
	}
	v, err := s.MatterView(ctx, projectID, sourceIDs, r.MatterID)
	if err != nil {
		return Suggestion{}, MatterView{}, err
	}
	if !v.Fresh || v.Matter.OwnerResolved || v.Matter.Draft.Status != "open" {
		if r.Status != "selected" && r.Status != "dismissed" {
			r.Status = "withdrawn"
		}
	} else if r.Status == "postponed" && !r.ReviewAt.After(time.Now()) {
		r.Status = "ready"
	}
	return r, v, nil
}

func (s *Store) Suggestions(ctx context.Context, projectID string, sourceIDs []string) ([]Suggestion, error) {
	views, err := s.SearchMatters(ctx, projectID, sourceIDs, "", 20)
	if err != nil {
		return nil, err
	}
	out := []Suggestion{}
	for _, v := range views {
		r, _, err := s.Suggestion(ctx, projectID, sourceIDs, "suggestion-"+discoveryHash(v.Matter.ID))
		if err != nil {
			return nil, err
		}
		if r.Status == "ready" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Store) FeedbackSuggestion(ctx context.Context, projectID string, sourceIDs []string, id string, revision int, action string, until time.Time) error {
	r, _, err := s.Suggestion(ctx, projectID, sourceIDs, id)
	if err != nil {
		return err
	}
	if r.Revision != revision || r.Status == "selected" && action != "resolve" || action != "dismiss" && action != "postpone" && action != "resolve" || action == "postpone" && !until.After(time.Now()) {
		return errors.New("feedback requires a current unselected suggestion and a valid action/time")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, err := readSuggestion(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return errors.New("suggestion changed before feedback")
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		switch action {
		case "dismiss":
			r.Status = "dismissed"
		case "postpone":
			r.Status, r.ReviewAt = "postponed", until.UTC()
		case "resolve":
			m, err := readMatter(ctx, tx, r.MatterID)
			if err != nil {
				return err
			}
			m.OwnerResolved = true
			m.Revision++
			m.UpdatedAt = r.UpdatedAt
			b, _ := json.Marshal(m)
			if _, err = tx.ExecContext(ctx, "UPDATE matters SET payload=? WHERE id=?", b, m.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO matter_revisions(matter_id,revision,payload) VALUES(?,?,?)", m.ID, m.Revision, b); err != nil {
				return err
			}
			r.Status, r.MatterRevision = "withdrawn", m.Revision
		}
		return writeSuggestion(ctx, tx, r)
	})
}

// The suggestion claim and business run are one transaction. A crash, retry or
// second selection can inspect the linked run, but can never replay the action.
func (s *Store) CreatePreparationRun(ctx context.Context, run Run, sourceIDs []string) error {
	_, audience := s.Partition()
	if run.Preparation == nil || run.Source != nil || audience != OwnerAudienceID || run.ProjectID == "" || len(run.Projects) != 1 || run.Projects[0].ID != run.ProjectID || run.Projects[0].Scope != run.Scope {
		return errors.New("preparation requires an explicit local owner selection")
	}
	r, v, err := s.Suggestion(ctx, run.ProjectID, sourceIDs, run.Preparation.SuggestionID)
	if err != nil {
		return err
	}
	if r.Status != "ready" || r.Revision != run.Preparation.SuggestionRevision || r.MatterRevision != run.Preparation.MatterRevision || r.MatterID != run.Preparation.MatterID || !v.Fresh {
		return errors.New("suggestion is stale, unavailable or already selected")
	}
	if discoveryHash(run.Preparation.EvidenceIDs) != discoveryHash(v.Matter.Draft.EvidenceIDs) {
		return errors.New("selected preparation does not cite the current evidence")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, err := readSuggestion(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if current.Revision != r.Revision || current.RunID != "" || current.Status == "selected" {
			return errors.New("preparation was already selected or changed")
		}
		view, err := s.matterView(ctx, tx, run.ProjectID, sourceIDs, current.MatterID)
		if err != nil || !view.Fresh || view.Matter.OwnerResolved || view.Matter.Draft.Status != "open" || view.Matter.Revision != run.Preparation.MatterRevision {
			return ErrContextUnavailable
		}
		if err = s.assignPartition(&run.AgentID, &run.AudienceID); err != nil {
			return err
		}
		if err = createRun(ctx, tx, run); err != nil {
			return err
		}
		r.Status, r.RunID, r.UpdatedAt = "selected", run.ID, time.Now().UTC()
		r.Revision++
		if err = writeSuggestion(ctx, tx, r); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) ContextMemory(ctx context.Context, scope, id string, revision int) (Memory, error) {
	m, err := s.Memory(ctx, id)
	if err != nil || m.Scope != scope || m.Status != "active" || m.ExpiresAt != nil && !m.ExpiresAt.After(time.Now()) || revision != 0 && m.Revision != revision {
		return Memory{}, ErrContextUnavailable
	}
	return m, nil
}

func (s *Store) ContextMemoryEvidence(ctx context.Context, scope, id string, revision int) (any, error) {
	m, err := s.ContextMemory(ctx, scope, id, revision)
	if err != nil {
		return nil, err
	}
	if m.ExternalReceiptID != "" {
		return s.ExternalMemoryEvidence(ctx, m)
	}
	r, err := s.Run(ctx, m.SourceRunID)
	if err != nil || r.Scope != scope || len(r.Projects) > 1 {
		return nil, ErrContextUnavailable
	}
	if m.SourceType == "reviewed_experience" {
		all, err := s.CandidateEvidence(ctx, m.CandidateID)
		if err != nil {
			return nil, ErrContextUnavailable
		}
		selected := []ToolEvidence{}
		for _, e := range all {
			if hasContextSource(m.EvidenceIDs, e.ID) {
				selected = append(selected, e)
			}
		}
		if len(selected) != len(m.EvidenceIDs) {
			return nil, ErrContextUnavailable
		}
		return map[string]any{"role": "observed_tool_evidence", "run_id": r.ID, "evidence": selected}, nil
	}
	return map[string]any{"role": "attributed_user_statement", "run_id": r.ID, "input": r.Input, "source": r.Source, "supplements": r.Supplements, "created_at": r.CreatedAt}, nil
}

func (p PreparationSource) String() string {
	return fmt.Sprintf("suggestion %s revision %d; matter %s revision %d", p.SuggestionID, p.SuggestionRevision, p.MatterID, p.MatterRevision)
}
