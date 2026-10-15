package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const schemaExternalMemoryV15 = `
CREATE TABLE IF NOT EXISTS external_memory_connections(id TEXT PRIMARY KEY, payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS external_memory_receipts(id TEXT PRIMARY KEY, connection_id TEXT NOT NULL REFERENCES external_memory_connections(id), submission_key TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(connection_id,submission_key));
CREATE INDEX IF NOT EXISTS external_memory_pending ON external_memory_receipts(json_extract(payload,'$.processing_status'));
PRAGMA user_version=15;
`

const MaxExternalBytes = 64 << 10

// ExternalConnection is admitted by local configuration, never by source text.
type ExternalConnection struct {
	ID         string `json:"id"`
	AgentID    string `json:"agent_id"`
	AudienceID string `json:"audience_id"`
	ProjectID  string `json:"project_id,omitempty"`
	Scope      string `json:"scope"`
	Enabled    bool   `json:"enabled"`
}

type ExternalSource struct {
	Entry        string    `json:"entry"`
	Conversation string    `json:"conversation,omitempty"`
	Turn         string    `json:"turn,omitempty"`
	ObservedAt   time.Time `json:"observed_at"`
}

// These are caller-supplied excerpts, not Host-observed execution events.
type ExternalEvidence struct {
	ID         string        `json:"id"`
	Role       string        `json:"role"`
	Author     string        `json:"author"`
	Text       string        `json:"text"`
	ObservedAt time.Time     `json:"observed_at"`
	Tool       *ExternalTool `json:"tool,omitempty"`
}

type ExternalTool struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Result  json.RawMessage `json:"result"`
	IsError *bool           `json:"is_error"`
}

type ExternalItem struct {
	Kind        string           `json:"kind"` // statement or procedure
	Text        string           `json:"text,omitempty"`
	EvidenceIDs []string         `json:"evidence_ids"`
	Procedure   *ExperienceDraft `json:"procedure,omitempty"`
}

type ExternalSubmission struct {
	SchemaVersion int                `json:"schema_version"`
	Key           string             `json:"idempotency_key"`
	Source        ExternalSource     `json:"source"`
	Items         []ExternalItem     `json:"items"`
	Evidence      []ExternalEvidence `json:"evidence"`
}

type ExternalItemResult struct {
	Status   string          `json:"status"` // pending, published, excluded, failed
	Reason   string          `json:"reason,omitempty"`
	MemoryID string          `json:"memory_id,omitempty"`
	Revision int             `json:"revision,omitempty"`
	Audit    json.RawMessage `json:"audit,omitempty"`
}

type ExternalReceipt struct {
	ID               string               `json:"id"`
	Connection       ExternalConnection   `json:"connection"`
	Digest           string               `json:"digest"`
	Submission       ExternalSubmission   `json:"submission"`
	ReceivedAt       time.Time            `json:"received_at"`
	IngestionStatus  string               `json:"ingestion_status"`
	ProcessingStatus string               `json:"processing_status"`
	Results          []ExternalItemResult `json:"results"`
}

func boundedExternalText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsRune(s, '\x00')
}

func (v ExternalSubmission) Validate() error {
	b, err := json.Marshal(v)
	if err != nil || len(b) > MaxExternalBytes {
		return errors.New("external submission exceeds 64 KiB or contains invalid JSON")
	}
	if v.SchemaVersion != 1 || !boundedExternalText(v.Key, 128) || !boundedExternalText(v.Source.Entry, 64) || len(v.Source.Conversation) > 512 || len(v.Source.Turn) > 512 || v.Source.ObservedAt.IsZero() || len(v.Items) < 1 || len(v.Items) > 3 || len(v.Evidence) < 1 || len(v.Evidence) > 16 {
		return errors.New("provide schema_version=1, submission key, source time, 1–3 items and 1–16 evidence excerpts")
	}
	available := map[string]ExternalEvidence{}
	for _, e := range v.Evidence {
		if !boundedExternalText(e.ID, 128) || available[e.ID].ID != "" || !boundedExternalText(e.Author, 256) || !boundedExternalText(e.Text, 8192) || e.ObservedAt.IsZero() {
			return errors.New("evidence requires unique bounded IDs, authors, text and observation times")
		}
		if e.Role != "user" && e.Role != "assistant" && e.Role != "tool" {
			return errors.New("evidence role must be user, assistant or tool")
		}
		if e.Role == "tool" {
			if e.Tool == nil || !boundedExternalText(e.Tool.Name, 128) || !json.Valid(e.Tool.Input) || !json.Valid(e.Tool.Result) || e.Tool.IsError == nil {
				return errors.New("tool evidence requires name, JSON input/result and explicit is_error")
			}
		} else if e.Tool != nil {
			return errors.New("only tool evidence may contain tool observations")
		}
		available[e.ID] = e
	}
	for _, item := range v.Items {
		if len(item.EvidenceIDs) < 1 || len(item.EvidenceIDs) > 16 {
			return errors.New("items require 1–16 evidence references")
		}
		seen := map[string]bool{}
		for _, id := range item.EvidenceIDs {
			if available[id].ID == "" || seen[id] {
				return errors.New("unknown or repeated evidence reference")
			}
			seen[id] = true
		}
		switch item.Kind {
		case "statement":
			if item.Procedure != nil || !boundedExternalText(item.Text, 4096) {
				return errors.New("statement requires bounded exact source text and no procedure")
			}
			// A generated summary cannot impersonate the quoted user statement.
			if len(item.EvidenceIDs) != 1 || available[item.EvidenceIDs[0]].Role != "user" || available[item.EvidenceIDs[0]].Text != item.Text {
				return errors.New("statement must retain exactly one cited user excerpt")
			}
		case "procedure":
			if item.Text != "" || item.Procedure == nil {
				return errors.New("procedure requires a draft and no statement text")
			}
			d := *item.Procedure
			if len(d.EvidenceIDs) != len(item.EvidenceIDs) {
				return errors.New("procedure and item evidence must match")
			}
			for _, id := range d.EvidenceIDs {
				if !seen[id] {
					return errors.New("procedure evidence is outside item")
				}
			}
			if err := validateDraft(d, v.ToolEvidence()); err != nil {
				return err
			}
		default:
			return errors.New("item kind must be statement or procedure")
		}
	}
	return nil
}

func (v ExternalSubmission) ToolEvidence() []ToolEvidence {
	out := []ToolEvidence{}
	for i, e := range v.Evidence {
		if e.Tool != nil {
			out = append(out, ToolEvidence{Provenance: "external_agent_supplied", ID: e.ID, Name: e.Tool.Name, Input: e.Tool.Input, Result: e.Tool.Result, IsError: *e.Tool.IsError, CallSeq: int64(i*2 + 1), ResultSeq: int64(i*2 + 2)})
		}
	}
	return out
}

func (s *Store) SaveExternalConnection(ctx context.Context, c ExternalConnection) error {
	if !s.FileMemoryEnabled() {
		return errors.New("external memory requires the file-memory backend; migrate explicitly first")
	}
	if !validFileID(c.ID) || c.AgentID == "" || c.AudienceID != OwnerAudienceID || c.Scope == "" {
		return errors.New("external connection requires a private admitted scope")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var old []byte
		err := tx.QueryRowContext(ctx, "SELECT payload FROM external_memory_connections WHERE id=?", c.ID).Scan(&old)
		if err == nil {
			var previous ExternalConnection
			if err := json.Unmarshal(old, &previous); err != nil {
				return err
			}
			if previous.AgentID != c.AgentID || previous.AudienceID != c.AudienceID || previous.Scope != c.Scope || previous.ProjectID != c.ProjectID || !previous.Enabled && c.Enabled {
				return errors.New("connection identity is immutable; create a new connection after revocation")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO external_memory_connections(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", c.ID, b)
		return err
	})
}

func externalConnection(ctx context.Context, q rowQuerier, id string) (ExternalConnection, error) {
	var c ExternalConnection
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM external_memory_connections WHERE id=?", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err == nil && !c.Enabled {
		err = errors.New("memory connection is revoked")
	}
	return c, err
}

func (s *Store) ExternalConnection(ctx context.Context, id string) (ExternalConnection, error) {
	return externalConnection(ctx, s.db, id)
}

func readExternalReceipt(ctx context.Context, q rowQuerier, id string) (ExternalReceipt, error) {
	var r ExternalReceipt
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM external_memory_receipts WHERE id=?", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
}

func writeExternalReceipt(ctx context.Context, tx *sql.Tx, r ExternalReceipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE external_memory_receipts SET payload=? WHERE id=?", b, r.ID)
	return err
}

func (s *Store) ReceiveExternal(ctx context.Context, connection, id string, v ExternalSubmission) (ExternalReceipt, error) {
	var r ExternalReceipt
	if err := v.Validate(); err != nil {
		return r, err
	}
	b, _ := json.Marshal(v)
	digest := fileHash(b)
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		c, err := externalConnection(ctx, tx, connection)
		if err != nil {
			return err
		}
		var found string
		err = tx.QueryRowContext(ctx, "SELECT id FROM external_memory_receipts WHERE connection_id=? AND submission_key=?", connection, v.Key).Scan(&found)
		if err == nil {
			r, err = readExternalReceipt(ctx, tx, found)
			if err == nil && r.Digest != digest {
				return errors.New("idempotency key already has a different payload")
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r = ExternalReceipt{ID: id, Connection: c, Digest: digest, Submission: v, ReceivedAt: time.Now().UTC(), IngestionStatus: "received", ProcessingStatus: "pending", Results: make([]ExternalItemResult, len(v.Items))}
		for i := range r.Results {
			r.Results[i].Status = "pending"
		}
		payload, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO external_memory_receipts(id,connection_id,submission_key,payload) VALUES(?,?,?,?)", id, connection, v.Key, payload)
		return err
	})
	return r, err
}

func (s *Store) ExternalReceipt(ctx context.Context, connection, id, key string) (ExternalReceipt, error) {
	var r ExternalReceipt
	// Recover a published receipt before exposing its operational status.
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := externalConnection(ctx, tx, connection); err != nil {
			return err
		}
		if key != "" {
			if err := tx.QueryRowContext(ctx, "SELECT id FROM external_memory_receipts WHERE connection_id=? AND submission_key=?", connection, key).Scan(&id); err != nil {
				return err
			}
		}
		var err error
		r, err = readExternalReceipt(ctx, tx, id)
		if err == nil && r.Connection.ID != connection {
			return ErrContextUnavailable
		}
		return err
	})
	return r, err
}

// PendingExternal returns jobs for a single owning worker to resume after restart.
func (s *Store) PendingExternal(ctx context.Context) ([]ExternalReceipt, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM external_memory_receipts WHERE json_extract(payload,'$.processing_status') IN ('pending','processing') ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExternalReceipt{}
	for rows.Next() {
		var b []byte
		var r ExternalReceipt
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		if r.ProcessingStatus == "pending" || r.ProcessingStatus == "processing" {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func (s *Store) StartExternalReceipt(ctx context.Context, connection, id string) (ExternalReceipt, error) {
	var r ExternalReceipt
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		c, err := externalConnection(ctx, tx, connection)
		if err != nil {
			return err
		}
		if !s.matchesPartition(c.AgentID, c.AudienceID) {
			return ErrContextUnavailable
		}
		r, err = readExternalReceipt(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Connection.ID != connection {
			return ErrContextUnavailable
		}
		if r.ProcessingStatus == "pending" {
			r.ProcessingStatus = "processing"
			return writeExternalReceipt(ctx, tx, r)
		}
		return nil
	})
	return r, err
}

type ExternalPublication struct {
	Result             ExternalItemResult
	Memory             *Memory
	DuplicateIDs       []string
	ConflictIDs        []string
	ExpectedGeneration string
}

// FinishExternalItem atomically publishes knowledge and its receipt through the
// file manifest. A crash after the manifest write recovers the receipt update.
func (s *Store) FinishExternalItem(ctx context.Context, id string, index int, p ExternalPublication) error {
	return s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		r, err := readExternalReceipt(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := externalConnection(ctx, tx, r.Connection.ID)
		if err != nil {
			return err
		}
		if !s.matchesPartition(c.AgentID, c.AudienceID) || index < 0 || index >= len(r.Results) {
			return ErrContextUnavailable
		}
		if r.Results[index].Status != "pending" {
			return nil
		}
		if p.Result.Status != "published" && p.Result.Status != "excluded" && p.Result.Status != "failed" {
			return errors.New("invalid external item outcome")
		}
		if p.Result.Status == "published" && p.Memory == nil && len(p.DuplicateIDs) == 0 {
			return errors.New("publication requires a memory or an eligible duplicate")
		}
		if p.Memory != nil || len(p.DuplicateIDs) > 0 {
			generation, err := memoryFingerprintTx(ctx, tx, []string{c.Scope}, c.AgentID, c.AudienceID)
			if err != nil {
				return err
			}
			if p.ExpectedGeneration == "" || generation != p.ExpectedGeneration {
				return errors.New("external memory publication snapshot changed; submit a new reviewed attempt")
			}
			if len(p.DuplicateIDs) > 0 && len(p.ConflictIDs) > 0 {
				return errors.New("duplicates cannot bypass conflicts")
			}
			if len(p.DuplicateIDs) > 0 {
				for _, other := range p.DuplicateIDs {
					m, err := s.readMemory(ctx, tx, other)
					if err != nil || m.Scope != c.Scope || !recallEligible(m, time.Now()) {
						return errors.New("external duplicate target is no longer eligible")
					}
					if (r.Submission.Items[index].Kind == "procedure") != (m.SourceType == "reviewed_experience") {
						return errors.New("external duplicate cannot change source evidence type")
					}
					if p.Result.MemoryID == "" {
						p.Result.MemoryID = m.ID
						p.Result.Revision = m.Revision
					}
				}
			} else {
				m := *p.Memory
				if m.ExternalReceiptID != r.ID || m.SourceRunID != "" || m.Scope != c.Scope || !samePartition(m.AgentID, m.AudienceID, c.AgentID, c.AudienceID) || m.Revision != 1 || m.Status != "active" || m.CandidateID != "" {
					return errors.New("external memory source attribution is invalid")
				}
				item := r.Submission.Items[index]
				if !slices.Equal(m.EvidenceIDs, item.EvidenceIDs) {
					return errors.New("external publication changed evidence references")
				}
				for _, id := range m.EvidenceIDs {
					if !hasContextSource(item.EvidenceIDs, id) {
						return errors.New("external publication changed evidence references")
					}
				}
				if item.Kind == "statement" {
					if m.Text != item.Text || m.SourceType != "user_statement" || (m.Kind != "preference" && m.Kind != "constraint" && m.Kind != "project_fact" && m.Kind != "decision") || len(m.Steps) != 0 || len(m.Entities) != 0 || m.Conditions != "" {
						return errors.New("external statement must preserve its source and cannot claim a procedure")
					}
				} else if m.Text != ExperienceText(*item.Procedure) || m.SourceType != "reviewed_experience" || m.Kind != "procedure" || m.Conditions != item.Procedure.Conditions || !slices.Equal(m.Steps, item.Procedure.Steps) || !slices.Equal(m.Avoid, item.Procedure.Avoid) {
					return errors.New("external procedure must preserve its assessed draft")
				}
				if len(p.ConflictIDs) > 0 {
					m.Status = "conflicted"
					p.Result.Status = "excluded"
					p.Result.Reason = "conflicting_or_uncertain_memory"
				}
				if err := persistMemory(ctx, tx, m, true); err != nil {
					return err
				}
				p.Result.MemoryID = m.ID
				p.Result.Revision = m.Revision
				for _, other := range p.ConflictIDs {
					v, err := s.readMemory(ctx, tx, other)
					if err != nil || v.Scope != c.Scope || v.Status == "invalidated" || expired(v, time.Now()) {
						return errors.New("external conflict target is no longer eligible")
					}
					if v.Status != "conflicted" {
						v.Status = "conflicted"
						v.Revision++
						if err := persistMemory(ctx, tx, v, false); err != nil {
							return err
						}
					}
				}
			}
		}
		r.Results[index] = p.Result
		r.ProcessingStatus = "completed"
		for _, result := range r.Results {
			if result.Status == "pending" {
				r.ProcessingStatus = "processing"
				break
			}
			if result.Status == "failed" {
				r.ProcessingStatus = "failed"
			}
		}
		return writeExternalReceipt(ctx, tx, r)
	})
}

func (s *Store) ExternalMemoryEvidence(ctx context.Context, m Memory) (any, error) {
	r, err := readExternalReceipt(ctx, s.db, m.ExternalReceiptID)
	if err != nil || r.Connection.Scope != m.Scope || !s.matchesPartition(r.Connection.AgentID, r.Connection.AudienceID) {
		return nil, ErrContextUnavailable
	}
	selected := []ExternalEvidence{}
	for _, e := range r.Submission.Evidence {
		if hasContextSource(m.EvidenceIDs, e.ID) {
			selected = append(selected, e)
		}
	}
	if len(selected) != len(m.EvidenceIDs) {
		return nil, fmt.Errorf("external source evidence is missing")
	}
	return map[string]any{"role": "external_agent_supplied_evidence", "receipt_id": r.ID, "source": r.Submission.Source, "received_at": r.ReceivedAt, "digest": r.Digest, "coverage": "selected excerpts; caller-attributed, not Host-observed execution", "evidence": selected}, nil
}
