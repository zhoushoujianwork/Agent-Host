package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SchemaDiscoveryV12 is applied by the owning Host's ordinary migration path.
// Daily DWS evidence has no relationship to task history or learned memories.
const SchemaDiscoveryV12 = `
CREATE TABLE discovery_sources(id TEXT PRIMARY KEY, payload BLOB NOT NULL);
CREATE TABLE discovery_collections(id INTEGER PRIMARY KEY AUTOINCREMENT, source_id TEXT NOT NULL REFERENCES discovery_sources(id), created_at TEXT NOT NULL, payload BLOB NOT NULL);
CREATE INDEX discovery_collections_source ON discovery_collections(source_id,id);
CREATE TABLE discovery_evidence(id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES discovery_sources(id), message_id TEXT NOT NULL, digest TEXT NOT NULL, observed_at TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(source_id,message_id,digest));
CREATE INDEX discovery_evidence_source ON discovery_evidence(source_id,observed_at,id);
CREATE TABLE discovery_current(source_id TEXT NOT NULL REFERENCES discovery_sources(id), message_id TEXT NOT NULL, evidence_id TEXT NOT NULL REFERENCES discovery_evidence(id), PRIMARY KEY(source_id,message_id));
CREATE TABLE discovery_topics(id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES discovery_sources(id), fingerprint TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','accepted','dismissed')), created_at TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(source_id,fingerprint));
CREATE TABLE discovery_topic_evidence(topic_id TEXT NOT NULL REFERENCES discovery_topics(id), evidence_id TEXT NOT NULL REFERENCES discovery_evidence(id), PRIMARY KEY(topic_id,evidence_id));
PRAGMA user_version=12;
`

type DiscoverySource struct {
	ID                 string               `json:"id"`
	Profile            string               `json:"profile"`
	TenantID           string               `json:"tenant_id"`
	PrincipalID        string               `json:"principal_id"`
	ConversationID     string               `json:"conversation_id"`
	Type               string               `json:"type"`
	PeerOpenDingTalkID string               `json:"peer_open_dingtalk_id,omitempty"`
	ProjectID          string               `json:"project_id"`
	Checkpoint         DiscoveryCheckpoint  `json:"checkpoint"`
	LastCollection     *DiscoveryCollection `json:"last_collection,omitempty"`
	ExtractionDigest   string               `json:"extraction_digest,omitempty"`
	ExtractionStatus   string               `json:"extraction_status,omitempty"`
	ExtractionError    string               `json:"extraction_error,omitempty"`
	ExtractionModel    string               `json:"extraction_model,omitempty"`
	ExtractionAudit    json.RawMessage      `json:"extraction_audit,omitempty"`
}

type DiscoveryCheckpoint struct {
	CompleteThrough time.Time `json:"complete_through,omitempty"`
	PendingFrom     time.Time `json:"pending_from,omitempty"`
	PendingTo       time.Time `json:"pending_to,omitempty"`
}

type DiscoveryCollection struct {
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	At                 time.Time `json:"at"`
	Complete           bool      `json:"complete"`
	Partial            bool      `json:"partial"`
	HasMore            bool      `json:"has_more"`
	Count              int       `json:"count"`
	PagesFetched       int       `json:"pages_fetched"`
	LimitsFingerprint  string    `json:"limits_fingerprint,omitempty"`
	TargetVerification string    `json:"target_verification,omitempty"`
	Error              string    `json:"error,omitempty"`
	// Ledger preserves provider coverage fields, genuine pagination and failures.
	Ledger json.RawMessage `json:"ledger,omitempty"`
}

type DiscoveryEvidence struct {
	ID             string          `json:"id"`
	SourceID       string          `json:"source_id"`
	Profile        string          `json:"profile"`
	TenantID       string          `json:"tenant_id"`
	PrincipalID    string          `json:"principal_id"`
	ConversationID string          `json:"conversation_id"`
	ProjectID      string          `json:"project_id"`
	MessageID      string          `json:"message_id"`
	Digest         string          `json:"digest"`
	AuthorID       string          `json:"author_id"`
	Author         json.RawMessage `json:"author,omitempty"`
	SenderType     string          `json:"sender_type,omitempty"`
	MessageType    string          `json:"message_type,omitempty"`
	Text           string          `json:"text"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at,omitempty"`
	ObservedAt     time.Time       `json:"observed_at"`
	ThreadID       string          `json:"thread_id,omitempty"`
	QuotedMessage  json.RawMessage `json:"quoted_message,omitempty"`
	Forwarded      json.RawMessage `json:"forwarded,omitempty"`
	Availability   string          `json:"availability"`
}

type TopicDraft struct {
	Title              string   `json:"title"`
	Summary            string   `json:"summary"`
	ReportedOutcome    string   `json:"reported_outcome"`
	UnresolvedQuestion string   `json:"unresolved_question"`
	EvidenceIDs        []string `json:"evidence_ids"`
}

type TopicProposal struct {
	ID          string     `json:"id"`
	SourceID    string     `json:"source_id"`
	ProjectID   string     `json:"project_id"`
	Audience    string     `json:"audience"`
	Status      string     `json:"status"`
	Fingerprint string     `json:"fingerprint"`
	Draft       TopicDraft `json:"draft"`
	Model       string     `json:"model"`
	CreatedAt   time.Time  `json:"created_at"`
	DecidedAt   time.Time  `json:"decided_at,omitempty"`
}

type TopicExport struct {
	Proposal TopicProposal       `json:"proposal"`
	Evidence []DiscoveryEvidence `json:"evidence"`
	// Acceptance authorizes this bounded owner-private handoff only; it is not
	// task execution, permission to reply, or approval of durable memory.
	Disposition string `json:"disposition"`
}

func DiscoverySourceID(profile, conversationID, projectID string, selection ...string) string {
	return "dws-" + discoveryHash(append([]string{profile, conversationID, projectID}, selection...))
}

func discoveryHash(value any) string {
	b, _ := json.Marshal(value)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

// PrepareDiscoveryEvidence derives an immutable revision from attributed
// content. Observation time cannot create a new revision on overlapping reads.
func PrepareDiscoveryEvidence(e DiscoveryEvidence) DiscoveryEvidence {
	e.ID, e.Digest, e.ObservedAt = "", "", time.Time{}
	e.Digest = discoveryHash(e)
	e.ID = "dws-evidence-" + discoveryHash([]string{e.SourceID, e.MessageID, e.Digest})
	return e
}

func readDiscoverySource(ctx context.Context, q rowQuerier, id string) (DiscoverySource, error) {
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT payload FROM discovery_sources WHERE id=?", id).Scan(&b)
	var out DiscoverySource
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

func (s *Store) DiscoverySource(ctx context.Context, id string) (DiscoverySource, error) {
	return readDiscoverySource(ctx, s.db, id)
}

func (s *Store) DiscoverySources(ctx context.Context) ([]DiscoverySource, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM discovery_sources ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoverySource{}
	for rows.Next() {
		var b []byte
		var item DiscoverySource
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func writeDiscoverySource(ctx context.Context, tx *sql.Tx, source DiscoverySource) error {
	b, err := json.Marshal(source)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO discovery_sources(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", source.ID, b)
	return err
}

func validDiscoverySource(source DiscoverySource) error {
	if source.ID != DiscoverySourceID(source.Profile, source.ConversationID, source.ProjectID, source.Type, source.PeerOpenDingTalkID) || source.TenantID == "" || source.PrincipalID == "" || source.Profile != source.TenantID+":"+source.PrincipalID || source.ConversationID == "" || source.ProjectID == "" || source.Type != "dm" && source.Type != "group" || source.Type == "dm" && source.PeerOpenDingTalkID == "" || source.Type == "group" && source.PeerOpenDingTalkID != "" {
		return errors.New("invalid attributed discovery source")
	}
	return nil
}

// SaveDiscoveryCollection commits snapshots and coverage/checkpoint together.
// A partial or failed range remains pending and never advances complete coverage.
func (s *Store) SaveDiscoveryCollection(ctx context.Context, source DiscoverySource, collection DiscoveryCollection, evidence []DiscoveryEvidence) (int, error) {
	if err := validDiscoverySource(source); err != nil {
		return 0, err
	}
	if !collection.To.After(collection.From) || collection.At.IsZero() || collection.Count != len(evidence) || len(evidence) > 500 || collection.Complete && (collection.Partial || collection.HasMore || collection.Error != "") {
		return 0, errors.New("invalid discovery collection coverage")
	}
	if len(collection.Ledger) > 128<<10 || len(collection.Ledger) > 0 && !json.Valid(collection.Ledger) {
		return 0, errors.New("invalid discovery coverage ledger")
	}
	added := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := readProject(ctx, tx, source.ProjectID); err != nil {
			return fmt.Errorf("discovery project: %w", err)
		}
		old, err := readDiscoverySource(ctx, tx, source.ID)
		if err == nil {
			if old.Profile != source.Profile || old.ConversationID != source.ConversationID || old.ProjectID != source.ProjectID || old.Type != source.Type || old.PeerOpenDingTalkID != source.PeerOpenDingTalkID {
				return errors.New("discovery source identity changed")
			}
			source = old
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !source.Checkpoint.PendingFrom.IsZero() && (!collection.From.Equal(source.Checkpoint.PendingFrom) || !collection.To.Equal(source.Checkpoint.PendingTo)) {
			return errors.New("discovery collection must preserve the exact pending range")
		}
		if !source.Checkpoint.CompleteThrough.IsZero() && collection.From.After(source.Checkpoint.CompleteThrough) {
			return errors.New("discovery collection would silently skip an uncovered range")
		}
		source.LastCollection = &collection
		if collection.Complete {
			if collection.To.After(source.Checkpoint.CompleteThrough) {
				source.Checkpoint.CompleteThrough = collection.To
			}
			source.Checkpoint.PendingFrom = time.Time{}
			source.Checkpoint.PendingTo = time.Time{}
		} else {
			source.Checkpoint.PendingFrom = collection.From
			source.Checkpoint.PendingTo = collection.To
		}
		if err = writeDiscoverySource(ctx, tx, source); err != nil {
			return err
		}
		for _, e := range evidence {
			if e.SourceID != source.ID || e.Profile != source.Profile || e.TenantID != source.TenantID || e.PrincipalID != source.PrincipalID || e.ConversationID != source.ConversationID || e.ProjectID != source.ProjectID || e.MessageID == "" || e.AuthorID == "" || e.CreatedAt.IsZero() || e.CreatedAt.Before(collection.From) || !e.CreatedAt.Before(collection.To) || len(e.Text) > 32768 || e.Availability != "available" && e.Availability != "withdrawn" {
				return errors.New("discovery evidence does not match collection source/window")
			}
			check := PrepareDiscoveryEvidence(e)
			if e.ID != check.ID || e.Digest != check.Digest {
				return errors.New("discovery evidence revision digest mismatch")
			}
			e.ObservedAt = collection.At
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO discovery_evidence(id,source_id,message_id,digest,observed_at,payload) VALUES(?,?,?,?,?,?)", e.ID, e.SourceID, e.MessageID, e.Digest, collection.At.Format(time.RFC3339Nano), b)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			added += int(n)
			// A stale overlap must not replace a newer provider revision.
			var previous []byte
			err = tx.QueryRowContext(ctx, "SELECT e.payload FROM discovery_current c JOIN discovery_evidence e ON e.id=c.evidence_id WHERE c.source_id=? AND c.message_id=?", source.ID, e.MessageID).Scan(&previous)
			if err == nil {
				var p DiscoveryEvidence
				if err = json.Unmarshal(previous, &p); err != nil {
					return err
				}
				if p.UpdatedAt.After(e.UpdatedAt) {
					continue
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO discovery_current(source_id,message_id,evidence_id) VALUES(?,?,?) ON CONFLICT(source_id,message_id) DO UPDATE SET evidence_id=excluded.evidence_id", source.ID, e.MessageID, e.ID); err != nil {
				return err
			}
		}
		b, err := json.Marshal(collection)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO discovery_collections(source_id,created_at,payload) VALUES(?,?,?)", source.ID, collection.At.Format(time.RFC3339Nano), b)
		return err
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}

func (s *Store) DiscoveryEvidence(ctx context.Context, sourceID string, limit int) ([]DiscoveryEvidence, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("discovery evidence limit must be 1-500")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT e.payload FROM discovery_current c JOIN discovery_evidence e ON e.id=c.evidence_id WHERE c.source_id=? ORDER BY json_extract(e.payload,'$.created_at') DESC,e.id DESC LIMIT ?", sourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoveryEvidence{}
	for rows.Next() {
		var b []byte
		var e DiscoveryEvidence
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) DiscoveryEvidenceCount(ctx context.Context, sourceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM discovery_current WHERE source_id=?", sourceID).Scan(&n)
	return n, err
}

func ValidateTopicDraft(d TopicDraft, evidence []DiscoveryEvidence) error {
	if strings.TrimSpace(d.Title) == "" || len(d.Title) > 256 || strings.TrimSpace(d.Summary) == "" || len(d.Summary) > 2048 || len(d.ReportedOutcome) > 2048 || len(d.UnresolvedQuestion) > 2048 || len(d.EvidenceIDs) < 1 || len(d.EvidenceIDs) > 32 {
		return errors.New("topic draft exceeds bounded shape")
	}
	known := map[string]bool{}
	for _, e := range evidence {
		known[e.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range d.EvidenceIDs {
		if !known[id] || seen[id] {
			return errors.New("topic contains unknown or duplicate evidence reference")
		}
		seen[id] = true
	}
	return nil
}

// SaveTopicExtraction records the independent model job and source-backed
// proposals atomically. It never inserts business tasks or learned memories.
func (s *Store) SaveTopicExtraction(ctx context.Context, sourceID, digest, model string, drafts []TopicDraft, audit json.RawMessage, extractionErr string) (int, error) {
	if digest == "" || len(drafts) > 3 || len(audit) > 128<<10 || !json.Valid(audit) || extractionErr != "" && len(drafts) > 0 || extractionErr == "" && len(drafts) > 0 && strings.TrimSpace(model) == "" {
		return 0, errors.New("invalid topic extraction result")
	}
	added := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		source, err := readDiscoverySource(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		source.ExtractionDigest, source.ExtractionModel, source.ExtractionAudit, source.ExtractionError = digest, model, audit, extractionErr
		source.ExtractionStatus = "completed"
		if extractionErr != "" {
			source.ExtractionStatus = "failed"
		}
		for _, d := range drafts {
			evidence := []DiscoveryEvidence{}
			for _, id := range d.EvidenceIDs {
				var b []byte
				var e DiscoveryEvidence
				if err = tx.QueryRowContext(ctx, "SELECT payload FROM discovery_evidence WHERE id=? AND source_id=?", id, sourceID).Scan(&b); err != nil {
					return fmt.Errorf("topic evidence: %w", err)
				}
				if err = json.Unmarshal(b, &e); err != nil {
					return err
				}
				evidence = append(evidence, e)
			}
			if err = ValidateTopicDraft(d, evidence); err != nil {
				return err
			}
			ids := append([]string(nil), d.EvidenceIDs...)
			sort.Strings(ids)
			fingerprint := discoveryHash(ids)
			topic := TopicProposal{ID: "topic-" + discoveryHash([]string{sourceID, fingerprint}), SourceID: sourceID, ProjectID: source.ProjectID, Audience: "owner-private", Status: "pending", Fingerprint: fingerprint, Draft: d, Model: model, CreatedAt: time.Now().UTC()}
			b, err := json.Marshal(topic)
			if err != nil {
				return err
			}
			r, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO discovery_topics(id,source_id,fingerprint,status,created_at,payload) VALUES(?,?,?,?,?,?)", topic.ID, sourceID, fingerprint, topic.Status, topic.CreatedAt.Format(time.RFC3339Nano), b)
			if err != nil {
				return err
			}
			n, err := r.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				continue
			}
			added += int(n)
			for _, id := range ids {
				if _, err = tx.ExecContext(ctx, "INSERT INTO discovery_topic_evidence(topic_id,evidence_id) VALUES(?,?)", topic.ID, id); err != nil {
					return err
				}
			}
		}
		return writeDiscoverySource(ctx, tx, source)
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}

func readTopicProposal(ctx context.Context, q rowQuerier, id string) (TopicProposal, error) {
	var b []byte
	var p TopicProposal
	err := q.QueryRowContext(ctx, "SELECT payload FROM discovery_topics WHERE id=?", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	return p, err
}
func (s *Store) TopicProposal(ctx context.Context, id string) (TopicProposal, error) {
	return readTopicProposal(ctx, s.db, id)
}

func (s *Store) TopicProposals(ctx context.Context, status string, limit int) ([]TopicProposal, error) {
	if status != "" && status != "pending" && status != "accepted" && status != "dismissed" || limit < 1 || limit > 100 {
		return nil, errors.New("invalid topic proposal filter or limit")
	}
	query := "SELECT payload FROM discovery_topics"
	args := []any{}
	if status != "" {
		query += " WHERE status=?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopicProposal{}
	for rows.Next() {
		var b []byte
		var p TopicProposal
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DismissTopic(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := readTopicProposal(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Status != "pending" {
			return errors.New("only pending topics can be dismissed")
		}
		p.Status, p.DecidedAt = "dismissed", time.Now().UTC()
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE discovery_topics SET status=?,payload=? WHERE id=?", p.Status, b, id)
		return err
	})
}

func (s *Store) AcceptTopic(ctx context.Context, id, projectID string) (TopicExport, error) {
	out := TopicExport{Evidence: []DiscoveryEvidence{}, Disposition: "accepted_for_owner_private_handoff_only"}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := readTopicProposal(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Status != "pending" || projectID != p.ProjectID || p.Audience != "owner-private" {
			return errors.New("topic acceptance requires pending proposal, exact project and owner-private audience")
		}
		if _, err = readProject(ctx, tx, projectID); err != nil {
			return err
		}
		total := 0
		for _, id := range p.Draft.EvidenceIDs {
			var b []byte
			var e DiscoveryEvidence
			if err = tx.QueryRowContext(ctx, "SELECT payload FROM discovery_evidence WHERE id=? AND source_id=?", id, p.SourceID).Scan(&b); err != nil {
				return err
			}
			if err = json.Unmarshal(b, &e); err != nil {
				return err
			}
			if e.ProjectID != projectID || e.Availability != "available" {
				return errors.New("topic evidence is unavailable or outside the requested project")
			}
			total += len(b)
			if total > 128<<10 {
				return errors.New("topic export exceeds 128 KiB; no evidence was truncated")
			}
			out.Evidence = append(out.Evidence, e)
		}
		p.Status, p.DecidedAt = "accepted", time.Now().UTC()
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE discovery_topics SET status=?,payload=? WHERE id=?", p.Status, b, p.ID); err != nil {
			return err
		}
		out.Proposal = p
		return nil
	})
	return out, err
}

// TopicReview reads exactly the immutable snapshots cited by the proposal.
// It is safe before acceptance and after a terminal decision/output failure.
func (s *Store) TopicReview(ctx context.Context, id string) (TopicExport, error) {
	out := TopicExport{Evidence: []DiscoveryEvidence{}, Disposition: "review_only"}
	p, err := s.TopicProposal(ctx, id)
	if err != nil {
		return out, err
	}
	total := 0
	for _, id := range p.Draft.EvidenceIDs {
		var b []byte
		var e DiscoveryEvidence
		if err = s.db.QueryRowContext(ctx, "SELECT payload FROM discovery_evidence WHERE id=? AND source_id=?", id, p.SourceID).Scan(&b); err != nil {
			return TopicExport{}, fmt.Errorf("topic review source unavailable: %w", err)
		}
		if err = json.Unmarshal(b, &e); err != nil {
			return TopicExport{}, err
		}
		if e.ProjectID != p.ProjectID || e.Availability != "available" {
			return TopicExport{}, errors.New("topic review evidence is unavailable or outside its project")
		}
		total += len(b)
		if total > 128<<10 {
			return TopicExport{}, errors.New("topic review exceeds 128 KiB; no evidence was truncated")
		}
		out.Evidence = append(out.Evidence, e)
	}
	if err = ValidateTopicDraft(p.Draft, out.Evidence); err != nil {
		return TopicExport{}, err
	}
	out.Proposal = p
	return out, nil
}

// PruneDiscoveryCache retains every snapshot cited by any retained proposal,
// including dismissed proposals, so review/export evidence never disappears.
func (s *Store) PruneDiscoveryCache(ctx context.Context, before time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		// Expired current pointers must not keep old cited chats in the active
		// extraction cache. Citations pin immutable evidence, not discovery input.
		if _, err := tx.ExecContext(ctx, "DELETE FROM discovery_current WHERE evidence_id IN (SELECT id FROM discovery_evidence WHERE julianday(observed_at)<julianday(?))", before.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM discovery_evidence WHERE julianday(observed_at)<julianday(?) AND id NOT IN (SELECT evidence_id FROM discovery_topic_evidence) AND id NOT IN (SELECT evidence_id FROM matter_evidence)", before.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM discovery_collections WHERE julianday(created_at)<julianday(?)", before.UTC().Format(time.RFC3339Nano))
		return err
	})
}
