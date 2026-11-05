package dream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-host/internal/experience"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

const LifecyclePolicyVersion = "dream-lifecycle-v1"

type ExtractionInput struct {
	Partition                              state.DreamPartition
	Source, Directory, Digest, Attribution string
	CanonicalRunID, CanonicalSessionID     string
	Documents                              []Document
}

type Draft struct {
	Kind       string    `json:"kind"`
	Text       string    `json:"text"`
	Inferred   bool      `json:"inferred"`
	ExpiresAt  time.Time `json:"expires_at"`
	SourceRefs []string  `json:"source_refs"`
}

type Extractor interface {
	Extract(context.Context, ExtractionInput) ([]Draft, error)
}

type Lifecycle struct {
	Store     *state.Store
	Extractor Extractor
	Now       func() time.Time
	Batch     int
}

func (l Lifecycle) Process(ctx context.Context, source string, destination Destination) ([]state.DreamReceipt, error) {
	if l.Store == nil || l.Extractor == nil {
		return nil, errors.New("dream lifecycle requires Store and Extractor")
	}
	report, err := Scan(source, destination)
	if err != nil {
		return nil, err
	}
	p := state.DreamPartition{PersonID: destination.PersonID, AgentID: destination.Agent, AudienceID: destination.Audience, Scope: destination.MemoryScope}
	now := time.Now().UTC()
	if l.Now != nil {
		now = l.Now().UTC()
	}
	var receipts []state.DreamReceipt
	extractionAttempts := 0
	for _, entry := range report.Entries {
		if !entry.Eligible {
			continue
		}
		attribution := "external_agent_supplied"
		documents := append([]Document(nil), entry.Documents...)
		allowedRefs := map[string]bool{}
		failedRefs := map[string]bool{}
		for _, document := range entry.Documents {
			allowedRefs["artifact:"+document.Path] = true
			lower := strings.ToLower(string(document.Body))
			if strings.Contains(lower, `"is_error":true`) || strings.Contains(lower, `"status":"failed"`) || strings.Contains(lower, "\nblocked") || strings.Contains(lower, "error:") {
				failedRefs["artifact:"+document.Path] = true
			}
		}
		diagnostic := ""
		if entry.Manifest.CanonicalRunID != "" {
			if run, e := l.Store.Run(ctx, entry.Manifest.CanonicalRunID); e == nil && run.AgentID == p.AgentID && run.AudienceID == p.AudienceID && run.Scope == p.Scope && run.SessionID == entry.Manifest.CanonicalSession && run.Source != nil && run.Source.SenderID == p.PersonID {
				attribution = "host_observed"
				evidence, evidenceErr := l.Store.ToolEvidence(ctx, run.ID)
				if evidenceErr != nil {
					return receipts, evidenceErr
				}
				body, marshalErr := json.Marshal(experience.LearningEvidence(evidence))
				if marshalErr != nil {
					return receipts, marshalErr
				}
				documents = append(documents, Document{Path: "host-tool-evidence.json", Body: body})
				for _, item := range evidence {
					ref := "tool:" + item.ID
					allowedRefs[ref] = true
					if item.IsError {
						failedRefs[ref] = true
					}
				}
				if run.ExecutionStatus != "completed" || run.MemoryAttribution == "unresolved" || run.AcceptanceStatus == "" || run.AcceptanceStatus == "unverified" || run.AcceptanceStatus == "unresolved" || run.AcceptanceStatus == "blocked" || run.AcceptanceStatus == "failed" || run.AcceptanceStatus == "rejected" || run.AcceptanceStatus == "canceled" || run.AcceptanceStatus == "unknown" {
					diagnostic = "canonical run is not positively resolved"
				}
			}
		}
		key := hashStrings(entry.IdempotencyKey, LifecyclePolicyVersion)
		status := "scanned"
		if diagnostic != "" {
			status = "diagnostic_only"
		}
		receipt := state.DreamReceipt{ID: "dream-receipt-" + key[:24], IdempotencyKey: key, Source: filepath.Join(report.Source, entry.Directory), SourceDigest: entry.Manifest.Digest, Partition: p, Status: status, CanonicalRunID: entry.Manifest.CanonicalRunID, CanonicalSessionID: entry.Manifest.CanonicalSession, Attribution: attribution, Error: diagnostic, CreatedAt: now}
		receipt, err = l.Store.SaveDreamExtraction(ctx, receipt, nil)
		if err != nil {
			return receipts, err
		}
		receipts = append(receipts, receipt)
		if receipt.Status == "extracted" || receipt.Status == "diagnostic_only" {
			continue
		}
		if l.Batch > 0 && extractionAttempts >= l.Batch {
			continue
		}
		extractionAttempts++
		drafts, e := l.Extractor.Extract(ctx, ExtractionInput{Partition: p, Source: report.Source, Directory: entry.Directory, Digest: entry.Manifest.Digest, Attribution: attribution, CanonicalRunID: entry.Manifest.CanonicalRunID, CanonicalSessionID: entry.Manifest.CanonicalSession, Documents: documents})
		if e != nil {
			_ = l.Store.RecordDreamExtractionFailure(ctx, receipt.ID, e.Error())
			return receipts, e
		}
		if len(drafts) > 32 {
			return receipts, errors.New("dream extraction exceeds candidate budget")
		}
		candidates := make([]state.DreamCandidate, 0, len(drafts))
		for i, d := range drafts {
			if d.Kind != "knowledge" && d.Kind != "working_preference" && d.Kind != "task" && d.Kind != "procedure" {
				return receipts, fmt.Errorf("unsupported dream candidate kind %q", d.Kind)
			}
			lower := strings.ToLower(d.Text)
			if strings.Contains(lower, "security.md") || strings.Contains(lower, "agents.md") {
				return receipts, errors.New("operator-owned policy document cannot become a dream candidate")
			}
			if strings.TrimSpace(d.Text) == "" || len(d.Text) > 32<<10 || !d.ExpiresAt.After(now) || d.ExpiresAt.After(now.Add(366*24*time.Hour)) {
				return receipts, errors.New("dream candidate text or TTL exceeds policy")
			}
			if len(d.SourceRefs) < 1 || len(d.SourceRefs) > 32 {
				return receipts, errors.New("dream candidate requires bounded exact source refs")
			}
			excludedProcedure := false
			for _, ref := range d.SourceRefs {
				if !allowedRefs[ref] {
					return receipts, fmt.Errorf("unknown dream source ref %q", ref)
				}
				if d.Kind == "procedure" && failedRefs[ref] {
					excludedProcedure = true
				}
			}
			if excludedProcedure {
				_ = l.Store.RecordDreamExtractionFailure(ctx, receipt.ID, "positive procedure excluded because cited source reports failure")
				continue
			}
			id := hashStrings(receipt.ID, fmt.Sprint(i), d.Kind, d.Text, d.ExpiresAt.UTC().Format(time.RFC3339Nano))
			candidates = append(candidates, state.DreamCandidate{ID: "dream-candidate-" + id[:24], ReceiptID: receipt.ID, Partition: p, Kind: d.Kind, Text: d.Text, SourceRefs: append([]string(nil), d.SourceRefs...), ExpiresAt: d.ExpiresAt.UTC(), Inferred: d.Inferred, CreatedAt: now})
		}
		receipt, err = l.Store.CompleteDreamExtraction(ctx, receipt.ID, candidates)
		if err != nil {
			return receipts, err
		}
		receipts[len(receipts)-1] = receipt
	}
	return receipts, nil
}

type HOTManifestEntry struct {
	ID        string     `json:"id"`
	Revision  int        `json:"revision"`
	Kind      string     `json:"kind"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type HOTBundle struct {
	Version   string               `json:"version"`
	Partition state.DreamPartition `json:"partition"`
	Documents map[string]string    `json:"documents"`
	Memories  []HOTManifestEntry   `json:"memories"`
	Digest    string               `json:"digest"`
}

func ExportHOT(ctx context.Context, store *state.Store, p state.DreamPartition, now time.Time, limit int) (HOTBundle, error) {
	memories, err := store.DreamMemories(ctx, p, now, limit)
	if err != nil {
		return HOTBundle{}, err
	}
	b := HOTBundle{Version: "dream-hot-v1", Partition: p, Documents: map[string]string{"USER.md": "", "MEMORY.md": "", "TASKS.md": ""}}
	for _, m := range memories {
		name := "MEMORY.md"
		if m.Kind == "preference" {
			name = "USER.md"
		} else if m.Kind == "active_task" {
			name = "TASKS.md"
		}
		line := "- " + m.Text + "\n"
		total := len(b.Documents["USER.md"]) + len(b.Documents["MEMORY.md"]) + len(b.Documents["TASKS.md"])
		if total+len(line) > 12<<10 {
			break
		}
		b.Documents[name] += line
		b.Memories = append(b.Memories, HOTManifestEntry{m.ID, m.Revision, m.Kind, m.ExpiresAt})
	}
	sort.Slice(b.Memories, func(i, j int) bool { return b.Memories[i].ID < b.Memories[j].ID })
	b.Digest = hotBundleDigest(b)
	return b, nil
}

func hotBundleDigest(b HOTBundle) string {
	raw, _ := json.Marshal(struct {
		Version   string
		Partition state.DreamPartition
		Documents map[string]string
		Memories  []HOTManifestEntry
	}{b.Version, b.Partition, b.Documents, b.Memories})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func ReviewCandidate(ctx context.Context, store *state.Store, id, actor, note, decision string) (state.DreamCandidate, error) {
	c, err := store.DreamCandidate(ctx, id)
	if err != nil {
		return c, err
	}
	receipt, err := store.DreamReceipt(ctx, c.ReceiptID)
	if err != nil {
		return c, err
	}
	destination := Destination{PersonID: c.Partition.PersonID, Agent: c.Partition.AgentID, Audience: c.Partition.AudienceID, MemoryScope: c.Partition.Scope}
	report, err := Scan(filepath.Dir(receipt.Source), destination)
	if err != nil {
		return c, err
	}
	digest := ""
	for _, entry := range report.Entries {
		if entry.Directory == filepath.Base(receipt.Source) && entry.Eligible && entry.Manifest != nil {
			digest = entry.Manifest.Digest
		}
	}
	if digest == "" || digest != receipt.SourceDigest {
		return c, errors.New("dream source changed since extraction")
	}
	return store.ReviewDreamCandidate(ctx, id, actor, note, decision, digest)
}

func PromoteSkill(ctx context.Context, store *state.Store, candidateID, root, actor string) (managedskills.Entry, error) {
	c, err := store.DreamCandidate(ctx, candidateID)
	if err != nil {
		return managedskills.Entry{}, err
	}
	if c.Status == "promoted" && c.SkillID != "" {
		catalog, e := managedskills.Load(root)
		if e != nil {
			return managedskills.Entry{}, e
		}
		for _, record := range catalog.Records {
			if record.ID == c.SkillID && len(record.Revisions) > 0 {
				return record.Revisions[len(record.Revisions)-1].Entry, nil
			}
		}
	}
	if c.Kind != "procedure" || c.Status != "pending" || actor == "" || !c.ExpiresAt.After(time.Now()) {
		return managedskills.Entry{}, errors.New("Skill promotion requires pending procedure and actor")
	}
	if c.Partition.PersonID != "local-owner" || c.Partition.AudienceID != state.OwnerAudienceID {
		return managedskills.Entry{}, errors.New("person-private Skill promotion is restricted to the local owner")
	}
	receipt, receiptErr := store.DreamReceipt(ctx, c.ReceiptID)
	if receiptErr != nil {
		return managedskills.Entry{}, receiptErr
	}
	report, scanErr := Scan(filepath.Dir(receipt.Source), Destination{PersonID: c.Partition.PersonID, Agent: c.Partition.AgentID, Audience: c.Partition.AudienceID, MemoryScope: c.Partition.Scope})
	if scanErr != nil {
		return managedskills.Entry{}, scanErr
	}
	valid := false
	for _, entry := range report.Entries {
		if entry.Directory == filepath.Base(receipt.Source) && entry.Eligible && entry.Manifest.Digest == receipt.SourceDigest {
			valid = true
		}
	}
	if !valid {
		return managedskills.Entry{}, errors.New("dream source changed since extraction")
	}
	id := "dream-" + strings.TrimPrefix(c.ID, "dream-candidate-")
	catalog, err := managedskills.Load(root)
	if err == nil {
		for _, r := range catalog.Records {
			for _, v := range r.Revisions {
				for _, p := range v.Entry.Provenance {
					if p == "dream-candidate:"+c.ID {
						return v.Entry, store.MarkDreamSkillPromoted(ctx, c.ID, actor, v.Entry.ID)
					}
				}
			}
		}
	}
	tmp, err := os.MkdirTemp(filepath.Dir(root), ".dream-skill-")
	if err != nil {
		return managedskills.Entry{}, err
	}
	defer os.RemoveAll(tmp)
	canonical, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return managedskills.Entry{}, err
	}
	tmp = canonical
	if err = os.Chmod(tmp, 0700); err != nil {
		return managedskills.Entry{}, err
	}
	description := strings.TrimSpace(strings.Split(c.Text, "\n")[0])
	description = strings.ReplaceAll(description, "---", "-")
	if len(description) > 500 {
		description = description[:500]
	}
	meta, _ := json.Marshal(description)
	body := "---\nname: " + id + "\ndescription: " + string(meta) + "\n---\n\n" + c.Text + "\n"
	if err = os.WriteFile(filepath.Join(tmp, "SKILL.md"), []byte(body), 0600); err != nil {
		return managedskills.Entry{}, err
	}
	e, err := managedskills.Import(tmp, managedskills.Entry{ID: id, Owner: actor, Applicability: "Explicitly reviewed dream procedure", Agents: []string{c.Partition.AgentID}, Audiences: []string{c.Partition.AudienceID}, Scopes: []string{c.Partition.Scope}, Provenance: []string{"dream-candidate:" + c.ID}})
	if err != nil {
		return e, err
	}
	catalog, err = managedskills.Update(root, "import", id, actor, "", "", 0, &e)
	if err != nil {
		return e, err
	}
	found := false
	for _, record := range catalog.Records {
		if record.ID == id {
			e = record.Revisions[len(record.Revisions)-1].Entry
			found = true
			break
		}
	}
	if !found {
		return e, errors.New("promoted Skill revision was not persisted")
	}
	err = store.MarkDreamSkillPromoted(ctx, c.ID, actor, e.ID)
	return e, err
}

func hashStrings(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
