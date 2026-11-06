package dream

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	Kind                string    `json:"kind"`
	Text                string    `json:"text"`
	Inferred            bool      `json:"inferred"`
	ExpiresAt           time.Time `json:"expires_at"`
	SourceRefs          []string  `json:"source_refs"`
	TargetSkillID       string    `json:"target_skill_id,omitempty"`
	TargetSkillRevision int       `json:"target_skill_revision,omitempty"`
	TargetSkillDigest   string    `json:"target_skill_digest,omitempty"`
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
	p := state.DreamPartition{PersonID: destination.PersonID, AgentID: destination.Agent, AudienceID: destination.Audience, Scope: destination.MemoryScope}
	now := time.Now().UTC()
	if l.Now != nil {
		now = l.Now().UTC()
	}
	var receipts []state.DreamReceipt
	var processErrors []error
	extractionAttempts := 0
	pageLimit := 64
	if l.Batch > pageLimit {
		pageLimit = l.Batch
	}
	canonical, nextCursor, err := l.Store.DreamCanonicalRunPage(ctx, p, pageLimit)
	if err != nil {
		return nil, err
	}
	report, after, scanErr := ScanPage(ctx, source, destination, nextCursor.HandoffAfter, 64)
	if scanErr != nil {
		processErrors = append(processErrors, scanErr)
	} else {
		nextCursor.HandoffAfter = after
	}
	seenRuns := map[string]bool{}
	for _, record := range canonical {
		seenRuns[record.Run.ID] = true
		evidence, evidenceErr := l.Store.ToolEvidence(ctx, record.Run.ID)
		snapshot, snapshotErr := canonicalRunSource(record, p, evidence, evidenceErr)
		if snapshotErr != nil {
			return receipts, snapshotErr
		}
		receipt, _, processErr := l.processSource(ctx, snapshot, now, &extractionAttempts)
		if receipt.ID != "" {
			receipts = append(receipts, receipt)
		}
		if processErr != nil {
			processErrors = append(processErrors, processErr)
		}
	}
	for _, entry := range report.Entries {
		if !entry.Eligible {
			digest := hashStrings(entry.Directory, fmt.Sprint(entry.Diagnostics))
			missing := make([]string, 0, len(entry.Diagnostics))
			for _, d := range entry.Diagnostics {
				missing = append(missing, d.Code)
			}
			work := state.DreamWorkItem{ID: "dream-work-" + hashStrings(p.String(), filepath.Join(report.Source, entry.Directory))[:24], Partition: p, SourceKey: "handoff:" + filepath.Join(report.Source, entry.Directory), Source: filepath.Join(report.Source, entry.Directory), EvidenceDigest: digest, PolicyVersion: state.DreamWorkPolicyVersion, MissingReasons: uniqueStrings(missing)}
			if _, _, e := l.Store.PrepareDreamWork(ctx, work, now); e != nil {
				return receipts, e
			}
			continue
		}
		if entry.Manifest.CanonicalRunID != "" {
			record, e := l.Store.DreamCanonicalRunByID(ctx, p, entry.Manifest.CanonicalRunID)
			if e == nil && record.Run.SessionID == entry.Manifest.CanonicalSession {
				if seenRuns[record.Run.ID] {
					continue
				}
				evidence, evidenceErr := l.Store.ToolEvidence(ctx, record.Run.ID)
				snapshot, snapshotErr := canonicalRunSource(record, p, evidence, evidenceErr)
				if snapshotErr != nil {
					return receipts, snapshotErr
				}
				receipt, _, processErr := l.processSource(ctx, snapshot, now, &extractionAttempts)
				if receipt.ID != "" {
					receipts = append(receipts, receipt)
				}
				if processErr != nil {
					processErrors = append(processErrors, processErr)
				}
				seenRuns[record.Run.ID] = true
				continue
			}
		}
		allowed, failed := map[string]bool{}, map[string]bool{}
		for _, document := range entry.Documents {
			ref := "artifact:" + document.Path
			allowed[ref] = true
			lower := strings.ToLower(string(document.Body))
			if strings.Contains(lower, `"is_error":true`) || strings.Contains(lower, `"status":"failed"`) || strings.Contains(lower, "\nblocked") || strings.Contains(lower, "error:") {
				failed[ref] = true
			}
		}
		sourcePath := filepath.Join(report.Source, entry.Directory)
		work := state.DreamWorkItem{ID: "dream-work-" + hashStrings(p.String(), sourcePath)[:24], Partition: p, SourceKey: "handoff:" + sourcePath, Source: sourcePath, RunID: entry.Manifest.CanonicalRunID, SessionID: entry.Manifest.CanonicalSession, EvidenceDigest: entry.Manifest.Digest, PolicyVersion: state.DreamWorkPolicyVersion}
		legacyKey := hashStrings(entry.IdempotencyKey, LifecyclePolicyVersion)
		snapshot := canonicalSource{Work: work, Input: ExtractionInput{Partition: p, Source: report.Source, Directory: entry.Directory, Digest: entry.Manifest.Digest, Attribution: "external_agent_supplied", CanonicalRunID: entry.Manifest.CanonicalRunID, CanonicalSessionID: entry.Manifest.CanonicalSession, Documents: append([]Document(nil), entry.Documents...)}, AllowedRefs: allowed, FailedRefs: failed, LegacyReceiptID: "dream-receipt-" + legacyKey[:24]}
		receipt, _, processErr := l.processSource(ctx, snapshot, now, &extractionAttempts)
		if receipt.ID != "" {
			receipts = append(receipts, receipt)
		}
		if processErr != nil {
			processErrors = append(processErrors, processErr)
		}
	}
	if err = l.Store.SaveDreamScanCursor(ctx, nextCursor); err != nil {
		return receipts, err
	}
	return receipts, errors.Join(processErrors...)
}

func (l Lifecycle) processSource(ctx context.Context, source canonicalSource, now time.Time, attempts *int) (state.DreamReceipt, bool, error) {
	work, ready, err := l.Store.PrepareDreamWork(ctx, source.Work, now)
	if err != nil {
		return state.DreamReceipt{}, false, err
	}
	if !ready {
		if len(work.ReceiptIDs) > 0 {
			receipt, readErr := l.Store.DreamReceipt(ctx, work.ReceiptIDs[len(work.ReceiptIDs)-1])
			return receipt, false, readErr
		}
		return state.DreamReceipt{}, false, nil
	}
	if source.LegacyReceiptID != "" {
		legacy, readErr := l.Store.DreamReceipt(ctx, source.LegacyReceiptID)
		if readErr == nil {
			if legacy.Status == "extracted" && legacy.Partition == work.Partition && legacy.Source == work.Source && legacy.SourceDigest == work.EvidenceDigest {
				items, candidateErr := l.Store.DreamCandidatesByReceipt(ctx, legacy.ID)
				if candidateErr != nil {
					return legacy, false, candidateErr
				}
				_, finishErr := l.Store.FinishDreamWork(ctx, work, "processed", "", []string{legacy.ID}, candidateIDs(items), now)
				return legacy, false, finishErr
			}
		} else if !errors.Is(readErr, sql.ErrNoRows) {
			return state.DreamReceipt{}, false, readErr
		}
	}
	policy := work.PolicyVersion + ":" + LifecyclePolicyVersion
	key := hashStrings(work.Partition.String(), work.EvidenceDigest, work.SourceKey, policy)
	receipt := state.DreamReceipt{ID: "dream-receipt-" + key[:24], IdempotencyKey: key, Source: work.Source, SourceDigest: work.EvidenceDigest, Partition: work.Partition, Status: "scanned", CanonicalRunID: work.RunID, CanonicalSessionID: work.SessionID, Attribution: source.Input.Attribution, CreatedAt: now}
	receipt, err = l.Store.SaveDreamExtraction(ctx, receipt, nil)
	if err != nil {
		return receipt, false, err
	}
	if receipt.Status == "extracted" {
		items, e := l.Store.DreamCandidatesByReceipt(ctx, receipt.ID)
		if e == nil {
			_, e = l.Store.FinishDreamWork(ctx, work, "processed", "", []string{receipt.ID}, candidateIDs(items), now)
		}
		return receipt, false, e
	}
	if l.Batch > 0 && *attempts >= l.Batch {
		return receipt, false, nil
	}
	*attempts++
	drafts, extractErr := l.Extractor.Extract(ctx, source.Input)
	if extractErr != nil {
		_ = l.Store.RecordDreamExtractionFailure(ctx, receipt.ID, extractErr.Error())
		_, finishErr := l.Store.FinishDreamWork(ctx, work, "retryable", extractErr.Error(), []string{receipt.ID}, nil, now)
		return receipt, true, errors.Join(extractErr, finishErr)
	}
	candidates, err := validateDreamDrafts(receipt, drafts, source.AllowedRefs, source.FailedRefs, source.Work.SkillEvidence, now)
	if err != nil {
		_ = l.Store.RecordDreamExtractionFailure(ctx, receipt.ID, err.Error())
		_, finishErr := l.Store.FinishDreamWork(ctx, work, "retryable", err.Error(), []string{receipt.ID}, nil, now)
		return receipt, true, errors.Join(err, finishErr)
	}
	receipt, err = l.Store.CompleteDreamExtraction(ctx, receipt.ID, candidates)
	if err == nil {
		_, err = l.Store.FinishDreamWork(ctx, work, "processed", "", []string{receipt.ID}, candidateIDs(candidates), now)
	}
	return receipt, true, err
}

func validateDreamDrafts(receipt state.DreamReceipt, drafts []Draft, allowedRefs, failedRefs map[string]bool, skillEvidence []state.DreamSkillEvidence, now time.Time) ([]state.DreamCandidate, error) {
	if len(drafts) > 32 {
		return nil, errors.New("dream extraction exceeds candidate budget")
	}
	candidates := make([]state.DreamCandidate, 0, len(drafts))
	for i, d := range drafts {
		if d.Kind != "knowledge" && d.Kind != "working_preference" && d.Kind != "task" && d.Kind != "procedure" {
			return nil, fmt.Errorf("unsupported dream candidate kind %q", d.Kind)
		}
		lower := strings.ToLower(d.Text)
		if strings.Contains(lower, "security.md") || strings.Contains(lower, "agents.md") {
			return nil, errors.New("operator-owned policy document cannot become a dream candidate")
		}
		if strings.TrimSpace(d.Text) == "" || len(d.Text) > 32<<10 || !d.ExpiresAt.After(now) || d.ExpiresAt.After(now.Add(366*24*time.Hour)) {
			return nil, errors.New("dream candidate text or TTL exceeds policy")
		}
		if len(d.SourceRefs) < 1 || len(d.SourceRefs) > 32 {
			return nil, errors.New("dream candidate requires bounded exact source refs")
		}
		excluded := false
		for _, ref := range d.SourceRefs {
			if !allowedRefs[ref] {
				return nil, fmt.Errorf("unknown dream source ref %q", ref)
			}
			if d.Kind == "procedure" && failedRefs[ref] {
				excluded = true
			}
		}
		if excluded {
			continue
		}
		if err := validateDraftSkillTarget(d, skillEvidence); err != nil {
			return nil, err
		}
		id := hashStrings(receipt.ID, fmt.Sprint(i), d.Kind, d.Text, d.ExpiresAt.UTC().Format(time.RFC3339Nano), d.TargetSkillID, fmt.Sprint(d.TargetSkillRevision), d.TargetSkillDigest)
		candidates = append(candidates, state.DreamCandidate{ID: "dream-candidate-" + id[:24], ReceiptID: receipt.ID, Partition: receipt.Partition, Kind: d.Kind, Text: d.Text, SourceRefs: append([]string(nil), d.SourceRefs...), ExpiresAt: d.ExpiresAt.UTC(), Inferred: d.Inferred, CreatedAt: now, TargetSkillID: d.TargetSkillID, TargetSkillRevision: d.TargetSkillRevision, TargetSkillDigest: d.TargetSkillDigest})
	}
	return candidates, nil
}

func validateDraftSkillTarget(d Draft, evidence []state.DreamSkillEvidence) error {
	hasTarget := d.TargetSkillID != "" || d.TargetSkillRevision != 0 || d.TargetSkillDigest != ""
	if !hasTarget {
		return nil
	}
	if d.Kind != "procedure" || d.TargetSkillID == "" || d.TargetSkillRevision < 1 || d.TargetSkillDigest == "" {
		return errors.New("dream Skill target requires a procedure and exact ID, revision and digest")
	}
	for _, observed := range evidence {
		if (observed.Kind == "loaded" || observed.Kind == "reference_read") && observed.Status == "completed" && observed.SkillID == d.TargetSkillID && observed.Revision == d.TargetSkillRevision && observed.Digest == d.TargetSkillDigest {
			return nil
		}
	}
	return errors.New("dream Skill target was not an exact completed load or reference read")
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
	if strings.HasPrefix(receipt.Source, "host-run:") {
		if receipt.Partition != c.Partition || receipt.Source != "host-run:"+receipt.CanonicalRunID || receipt.CanonicalRunID == "" {
			return c, errors.New("dream canonical receipt does not match candidate partition")
		}
		record, e := store.DreamCanonicalRunByID(ctx, c.Partition, receipt.CanonicalRunID)
		if e != nil {
			return c, errors.New("dream canonical source unavailable")
		}
		evidence, evidenceErr := store.ToolEvidence(ctx, record.Run.ID)
		snapshot, e := canonicalRunSource(record, c.Partition, evidence, evidenceErr)
		if e != nil || len(snapshot.Work.MissingReasons) > 0 || snapshot.Work.EvidenceDigest != receipt.SourceDigest || snapshot.Work.RunID != receipt.CanonicalRunID || snapshot.Work.SessionID != receipt.CanonicalSessionID {
			return c, errors.New("dream canonical source changed or incomplete since extraction")
		}
		return store.ReviewDreamCandidate(ctx, id, actor, note, decision, receipt.SourceDigest)
	}
	parent := filepath.Dir(receipt.Source)
	if err := privateDirectory(parent); err != nil {
		return c, err
	}
	entry := (Scanner{}).scanDirectory(parent, filepath.Base(receipt.Source), Destination{PersonID: c.Partition.PersonID, Agent: c.Partition.AgentID, Audience: c.Partition.AudienceID, MemoryScope: c.Partition.Scope})
	if !entry.Eligible || entry.Manifest == nil || entry.Manifest.Digest != receipt.SourceDigest {
		return c, errors.New("dream source changed since extraction")
	}
	return store.ReviewDreamCandidate(ctx, id, actor, note, decision, entry.Manifest.Digest)
}

func PromoteSkill(ctx context.Context, store *state.Store, candidateID, root, actor string) (managedskills.Entry, error) {
	return PromoteSkillWithTarget(ctx, store, candidateID, root, actor, SkillTarget{})
}

func PromoteSkillWithTarget(ctx context.Context, store *state.Store, candidateID, root, actor string, explicit SkillTarget) (managedskills.Entry, error) {
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
			if record.ID != c.SkillID {
				continue
			}
			for _, revision := range record.Revisions {
				if c.SkillRevision > 0 && revision.Entry.Revision == c.SkillRevision && revision.Entry.Digest == c.SkillDigest {
					return revision.Entry, nil
				}
				if c.SkillRevision == 0 {
					for _, provenance := range revision.Entry.Provenance {
						if provenance == "dream-candidate:"+c.ID {
							return revision.Entry, nil
						}
					}
				}
			}
		}
		return managedskills.Entry{}, errors.New("recorded promoted Skill revision is unavailable")
	}
	if c.Kind != "procedure" || c.Status != "pending" || actor == "" || !c.ExpiresAt.After(time.Now()) {
		return managedskills.Entry{}, errors.New("Skill promotion requires pending procedure and actor")
	}
	if c.Partition.PersonID != "local-owner" || c.Partition.AudienceID != state.OwnerAudienceID {
		return managedskills.Entry{}, errors.New("person-private Skill promotion is restricted to the local owner")
	}
	target, err := resolveSkillTarget(c, explicit)
	if err != nil {
		return managedskills.Entry{}, err
	}
	receipt, receiptErr := store.DreamReceipt(ctx, c.ReceiptID)
	if receiptErr != nil {
		return managedskills.Entry{}, receiptErr
	}
	if strings.HasPrefix(receipt.Source, "host-run:") {
		record, e := store.DreamCanonicalRunByID(ctx, c.Partition, receipt.CanonicalRunID)
		if e != nil {
			return managedskills.Entry{}, errors.New("dream canonical source unavailable")
		}
		evidence, evidenceErr := store.ToolEvidence(ctx, record.Run.ID)
		snapshot, e := canonicalRunSource(record, c.Partition, evidence, evidenceErr)
		if e != nil || len(snapshot.Work.MissingReasons) > 0 || snapshot.Work.EvidenceDigest != receipt.SourceDigest || snapshot.Work.SessionID != receipt.CanonicalSessionID {
			return managedskills.Entry{}, errors.New("dream canonical source changed or incomplete since extraction")
		}
		if err = validatePromotionTarget(target, snapshot.Work.SkillEvidence); err != nil {
			return managedskills.Entry{}, err
		}
		return promoteValidatedSkill(ctx, store, c, root, actor, target)
	}
	if !target.empty() {
		return managedskills.Entry{}, errors.New("Skill revision target requires canonical Host load evidence; candidate remains pending")
	}
	parent := filepath.Dir(receipt.Source)
	if err := privateDirectory(parent); err != nil {
		return managedskills.Entry{}, err
	}
	entry := (Scanner{}).scanDirectory(parent, filepath.Base(receipt.Source), Destination{PersonID: c.Partition.PersonID, Agent: c.Partition.AgentID, Audience: c.Partition.AudienceID, MemoryScope: c.Partition.Scope})
	if !entry.Eligible || entry.Manifest.Digest != receipt.SourceDigest {
		return managedskills.Entry{}, errors.New("dream source changed since extraction")
	}
	return promoteValidatedSkill(ctx, store, c, root, actor, target)
}

func promoteValidatedSkill(ctx context.Context, store *state.Store, c state.DreamCandidate, root, actor string, target SkillTarget) (managedskills.Entry, error) {
	if !target.empty() {
		return promoteSkillRevision(ctx, store, c, root, actor, target)
	}
	id := "dream-" + strings.TrimPrefix(c.ID, "dream-candidate-")
	catalog, err := managedskills.Load(root)
	if err == nil {
		for _, r := range catalog.Records {
			for _, v := range r.Revisions {
				for _, p := range v.Entry.Provenance {
					if p == "dream-candidate:"+c.ID {
						return v.Entry, store.MarkDreamSkillPromoted(ctx, c.ID, actor, v.Entry.ID, v.Entry.Revision, v.Entry.Digest)
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
	err = store.MarkDreamSkillPromoted(ctx, c.ID, actor, e.ID, e.Revision, e.Digest)
	return e, err
}

func hashStrings(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
