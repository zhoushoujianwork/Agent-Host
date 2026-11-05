package dream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

type AutomaticSkillDraftResult struct {
	CandidateID string `json:"candidate_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	SkillID     string `json:"skill_id,omitempty"`
	Revision    int    `json:"revision,omitempty"`
	Digest      string `json:"digest,omitempty"`
}

type SkillTarget struct {
	ID       string
	Revision int
	Digest   string
}

func (t SkillTarget) empty() bool { return t.ID == "" && t.Revision == 0 && t.Digest == "" }

func (t SkillTarget) complete() bool { return t.ID != "" && t.Revision > 0 && t.Digest != "" }

func resolveSkillTarget(candidate state.DreamCandidate, explicit SkillTarget) (SkillTarget, error) {
	target := SkillTarget{ID: candidate.TargetSkillID, Revision: candidate.TargetSkillRevision, Digest: candidate.TargetSkillDigest}
	if !target.empty() && !target.complete() {
		return SkillTarget{}, errors.New("candidate target Skill lacks exact managed revision metadata; candidate remains pending")
	}
	if !explicit.empty() && !explicit.complete() {
		return SkillTarget{}, errors.New("explicit target Skill requires ID, revision and digest; candidate remains pending")
	}
	if target.empty() {
		return explicit, nil
	}
	if !explicit.empty() && explicit != target {
		return SkillTarget{}, errors.New("explicit target Skill does not match extracted candidate target; candidate remains pending")
	}
	return target, nil
}

func validatePromotionTarget(target SkillTarget, evidence []state.DreamSkillEvidence) error {
	if target.empty() {
		return nil
	}
	missingMetadata := false
	for _, observed := range evidence {
		if observed.Kind != "loaded" && observed.Kind != "reference_read" || observed.Status != "completed" || observed.SkillID != target.ID {
			continue
		}
		if observed.Revision < 1 || observed.Digest == "" {
			missingMetadata = true
			continue
		}
		if observed.Revision == target.Revision && observed.Digest == target.Digest {
			return nil
		}
	}
	if missingMetadata {
		return errors.New("loaded native Skill lacks exact managed revision metadata; candidate remains pending")
	}
	return errors.New("target Skill was not an exact completed load or reference read; candidate remains pending")
}

func promoteSkillRevision(ctx context.Context, store *state.Store, candidate state.DreamCandidate, root, actor string, target SkillTarget) (managedskills.Entry, error) {
	provenance := "dream-candidate:" + candidate.ID
	reference := "references/dream-change-" + candidate.ID + ".md"
	referenceBody := "# Dream correction draft\n\n" +
		"This correction is unreviewed and remains disabled until normal Skill review, publication, and selection.\n\n" +
		"Candidate: `" + candidate.ID + "`\n\n" +
		"## Proposed correction\n\n" + strings.TrimSpace(candidate.Text) + "\n\n" +
		"## Evidence references\n\n"
	for _, ref := range candidate.SourceRefs {
		referenceBody += "- `" + ref + "`\n"
	}
	appendix := fmt.Sprintf("## Dream correction draft (unreviewed)\n\nA proposed correction from `%s` is preserved in [%s](%s). It is reference material only until separately reviewed and published.", candidate.ID, reference, reference)
	entry, err := managedskills.AppendDraftRevision(root, target.ID, actor, target.Revision, target.Digest, managedskills.DraftRevisionChange{Provenance: provenance, ReferencePath: reference, ReferenceBody: referenceBody, SkillAppendix: appendix})
	if err != nil {
		return entry, err
	}
	if err = store.MarkDreamSkillPromoted(ctx, candidate.ID, actor, entry.ID, entry.Revision, entry.Digest); err != nil {
		return entry, err
	}
	return entry, nil
}

// DraftSkillAutomatically is reserved for a Host-validated idle channel
// partition. It creates only an immutable draft; review, publication and
// enablement remain separate operator actions.
func DraftSkillAutomatically(ctx context.Context, store *state.Store, candidateID, root string, validated state.DreamPartition, now time.Time) (AutomaticSkillDraftResult, error) {
	result := AutomaticSkillDraftResult{CandidateID: candidateID}
	if store == nil || candidateID == "" || validated.PersonID == "" || validated.AgentID == "" || validated.AudienceID != state.OwnerAudienceID || validated.Scope == "" {
		return result, errors.New("automatic Skill draft requires exact trusted owner partition")
	}
	candidate, err := store.DreamCandidate(ctx, candidateID)
	if err != nil {
		return result, err
	}
	if candidate.Partition != validated {
		return result, errors.New("automatic Skill draft candidate does not match trusted partition")
	}
	if candidate.Status == "promoted" && candidate.SkillID != "" && candidate.SkillRevision > 0 && candidate.SkillDigest != "" {
		catalog, loadErr := managedskills.Load(root)
		if loadErr != nil {
			return result, loadErr
		}
		for _, record := range catalog.Records {
			for _, revision := range record.Revisions {
				if revision.Entry.ID == candidate.SkillID && revision.Entry.Revision == candidate.SkillRevision && revision.Entry.Digest == candidate.SkillDigest {
					result.Status, result.SkillID, result.Revision, result.Digest = "drafted", revision.Entry.ID, revision.Entry.Revision, revision.Entry.Digest
					return result, nil
				}
			}
		}
		return result, errors.New("recorded automatic Skill draft revision is unavailable")
	}
	if candidate.Kind != "procedure" || candidate.Status != "pending" {
		return result, errors.New("automatic Skill draft requires pending procedure")
	}
	receipt, err := store.DreamReceipt(ctx, candidate.ReceiptID)
	if err != nil {
		return result, err
	}
	baseKey := hashStrings("automatic-skill-draft-v1", validated.String(), candidate.ID, receipt.SourceDigest)
	block := func(key string, cause error) (AutomaticSkillDraftResult, error) {
		result.Status, result.Reason = "blocked", cause.Error()
		_, recordErr := store.RecordDreamSkillDraftBlocked(ctx, validated, candidate.ID, key, result.Reason, now)
		return result, recordErr
	}
	if !candidate.ExpiresAt.After(now) {
		return block(baseKey, errors.New("automatic Skill draft candidate expired"))
	}
	if receipt.Partition != validated || receipt.Status != "extracted" || receipt.Attribution != "host_observed" || receipt.CanonicalRunID == "" || receipt.Source != "host-run:"+receipt.CanonicalRunID {
		return block(baseKey, errors.New("automatic Skill draft requires canonical Host-observed receipt"))
	}
	record, err := store.DreamCanonicalRunByID(ctx, validated, receipt.CanonicalRunID)
	if err != nil {
		return result, err
	}
	evidence, evidenceErr := store.ToolEvidence(ctx, record.Run.ID)
	snapshot, err := canonicalRunSource(record, validated, evidence, evidenceErr)
	if err != nil {
		return result, err
	}
	catalog, err := managedskills.Load(root)
	if err != nil {
		return result, err
	}
	target, targetErr := resolveSkillTarget(candidate, SkillTarget{})
	catalogKey := automaticDraftCatalogKey(catalog, candidate, target)
	key := hashStrings(baseKey, snapshot.Work.EvidenceDigest, catalogKey)
	if candidate.SkillDraftStatus == "blocked" && candidate.SkillDraftKey == key {
		result.Status, result.Reason = "blocked", candidate.SkillDraftReason
		return result, nil
	}
	if len(snapshot.Work.MissingReasons) != 0 {
		return block(key, fmt.Errorf("canonical run is not fresh completed accepted evidence: %s", strings.Join(snapshot.Work.MissingReasons, ",")))
	}
	if snapshot.Work.EvidenceDigest != receipt.SourceDigest || snapshot.Work.RunID != receipt.CanonicalRunID || snapshot.Work.SessionID != receipt.CanonicalSessionID {
		return block(key, errors.New("canonical source changed since extraction"))
	}
	if targetErr != nil {
		return block(key, targetErr)
	}
	if err = validatePromotionTarget(target, snapshot.Work.SkillEvidence); err != nil {
		return block(key, err)
	}
	entry, err := promoteValidatedSkill(ctx, store, candidate, root, "dream-background", target)
	if err != nil {
		return block(key, err)
	}
	result.Status, result.SkillID, result.Revision, result.Digest = "drafted", entry.ID, entry.Revision, entry.Digest
	return result, nil
}

func automaticDraftCatalogKey(catalog managedskills.Catalog, candidate state.DreamCandidate, target SkillTarget) string {
	id := target.ID
	if id == "" {
		id = "dream-" + strings.TrimPrefix(candidate.ID, "dream-candidate-")
	}
	for _, record := range catalog.Records {
		if record.ID == id && len(record.Revisions) != 0 {
			latest := record.Revisions[len(record.Revisions)-1].Entry
			return hashStrings(id, fmt.Sprint(latest.Revision), latest.Digest)
		}
	}
	return hashStrings(id, "absent")
}
