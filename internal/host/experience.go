package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

// finalizeExperience reads saved execution evidence. It never resumes the
// business invocation or recursively finalizes the extraction invocation.
func (h *Host) finalizeExperience(ctx context.Context, j *journal) error {
	if j.run.AutomaticMemorySkipReason() != "" {
		return errors.New("automatic experience extraction is paused until project attribution is established")
	}
	var evidence []state.ToolEvidence
	var run state.Run
	err := j.phase(ctx, "evidence", "state.tools", "", func(ctx context.Context) error {
		var err error
		run, err = h.Store.Run(ctx, j.run.ID)
		if err != nil {
			return err
		}
		evidence, err = h.Store.ToolEvidence(ctx, run.ID)
		if err != nil {
			return err
		}
		return j.emit(event.Event{Type: "evidence.collected", Phase: "evidence", Component: "state.tools", Status: "completed", Message: fmt.Sprintf("%d correlated tool calls/results", len(evidence)), Data: map[string]any{"evidence": evidence}})
	})
	if err != nil {
		return h.experienceFailure(j, err)
	}
	evidence = state.ExecutionExperienceEvidence(evidence)
	evidence = experience.LearningEvidence(evidence)
	if len(evidence) == 0 {
		return h.finishCandidates(ctx, j, nil, json.RawMessage(`{"policy":"experience-v2","reason":"no_execution_experience_evidence"}`))
	}
	if h.Extractor == nil || h.Judge == nil {
		return h.experienceFailure(j, errors.New("experience extraction and judgment are required for tool evidence"))
	}
	var extraction experience.Extraction
	err = j.phase(ctx, "extract", "memory.extractor", h.Config.Model, func(ctx context.Context) error {
		var err error
		extraction, err = h.Extractor.Extract(ctx, run, evidence, func(o harness.Observation) error {
			e := event.Event{Type: "extraction.evidence", Phase: "extract", Component: "harness.claude.extractor", Status: "running", Agent: "memory-extractor", Model: o.Model, Message: o.Message, Data: map[string]any{"native_type": o.Type, "native_session_id": o.NativeSessionID, "raw": o.Raw}}
			if o.Type == "text_delta" {
				e.Type = "text.delta"
				e.Delta = o.Delta
			}
			return j.emit(e)
		})
		typ, status := "extraction.completed", "completed"
		if err != nil {
			typ, status = "extraction.failed", "failed"
		}
		emitErr := j.emit(event.Event{Type: typ, Phase: "extract", Component: "memory.extractor", Agent: "memory-extractor", Model: extraction.Model, Status: status, Message: fmt.Sprintf("%d proposed experiences", len(extraction.Drafts)), Data: map[string]any{"extraction": extraction}})
		return errors.Join(err, emitErr)
	})
	if err != nil {
		return h.experienceFailure(j, err)
	}
	var candidates []state.Candidate
	var assessments []experience.Assessment
	refinementUsed := false
	refinedCount := 0
	if len(extraction.Drafts) > 0 {
		err = j.phase(ctx, "assess", "memory.jev", h.Config.DecisionModel, func(ctx context.Context) error {
			for _, draft := range extraction.Drafts {
				model := extraction.Model
				assessment, err := h.Judge.Assess(ctx, draft, evidence)
				assessments = append(assessments, assessment)
				status := "completed"
				if err != nil {
					status = "failed"
				}
				emitErr := j.emit(event.Event{Type: "experience.assessed", Phase: "assess", Component: "memory.jev", Status: status, Model: assessment.Model, Message: fmt.Sprintf("candidate eligible=%t", assessment.Eligible), Data: map[string]any{"draft": draft, "assessment": assessment}})
				if err = errors.Join(err, emitErr); err != nil {
					return err
				}
				if !assessment.Eligible {
					refiner, ok := h.Extractor.(experience.Refiner)
					var reason struct {
						Reason string `json:"reason"`
					}
					_ = json.Unmarshal(assessment.Audit, &reason)
					cited, correction := correctionEvidence(draft, evidence)
					if !ok || refinementUsed || !correction || (reason.Reason != "not_supported" && reason.Reason != "assessment_abstained_low_probability") {
						continue
					}
					refinementUsed = true
					refined, refineErr := refiner.Refine(ctx, run, draft, cited)
					status := "completed"
					if refineErr != nil {
						status = "failed"
					}
					if emitErr := j.emit(event.Event{Type: "experience.refinement.completed", Phase: "assess", Component: "memory.extractor", Model: refined.Model, Status: status, Message: "one bounded narrowing attempt using the original cited correction evidence", Data: map[string]any{"original_draft": draft, "original_reason": reason.Reason, "extraction": refined}}); emitErr != nil {
						return errors.Join(refineErr, emitErr)
					}
					if refineErr != nil {
						return refineErr
					}
					if len(refined.Drafts) > 1 || strings.TrimSpace(refined.Model) == "" {
						return errors.New("experience refinement requires a resolved model and at most one draft")
					}
					if len(refined.Drafts) == 0 {
						continue
					}
					draft = refined.Drafts[0]
					if err := experience.ValidateDraft(draft, cited); err != nil {
						return err
					}
					assessment, err = h.Judge.Assess(ctx, draft, cited)
					assessments = append(assessments, assessment)
					status = "completed"
					if err != nil {
						status = "failed"
					}
					if emitErr := j.emit(event.Event{Type: "experience.refinement.assessed", Phase: "assess", Component: "memory.jev", Model: assessment.Model, Status: status, Message: fmt.Sprintf("refined candidate eligible=%t", assessment.Eligible), Data: map[string]any{"draft": draft, "assessment": assessment}}); emitErr != nil {
						return errors.Join(err, emitErr)
					}
					if err != nil {
						return err
					}
					if !assessment.Eligible {
						continue
					}
					model = refined.Model
					refinedCount++
				}
				entities, err := state.EvidenceEntitiesForRun(run, draft, evidence)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(assessment)
				if err != nil {
					return err
				}
				candidates = append(candidates, state.Candidate{ID: ID(), RunID: run.ID, Scope: run.Scope, Draft: draft, Entities: entities, Status: "pending", Model: model, Assessment: raw, CreatedAt: time.Now().UTC()})
			}
			return nil
		})
		if err != nil {
			return h.experienceFailure(j, err)
		}
	}
	audit, err := json.Marshal(map[string]any{"policy": "experience-v1", "extraction": extraction, "assessments": assessments})
	if err != nil {
		return h.experienceFailure(j, err)
	}
	if err := j.emit(event.Event{Type: "experience.learning.summary", Phase: "assess", Component: "host", Status: "completed", Message: fmt.Sprintf("drafts=%d eligible=%d refined=%d; publication and reuse remain separate", len(extraction.Drafts), len(candidates), refinedCount), Data: map[string]any{"drafts": len(extraction.Drafts), "eligible": len(candidates), "refinement_attempted": refinementUsed, "refined_eligible": refinedCount, "assessments": len(assessments)}}); err != nil {
		return h.experienceFailure(j, err)
	}
	return h.finishCandidates(ctx, j, candidates, audit)
}

func correctionEvidence(draft state.ExperienceDraft, evidence []state.ToolEvidence) ([]state.ToolEvidence, bool) {
	refs := make(map[string]bool, len(draft.EvidenceIDs))
	for _, id := range draft.EvidenceIDs {
		refs[id] = true
	}
	var cited []state.ToolEvidence
	failed, reported := false, false
	var failureResultSeq int64
	for _, item := range evidence {
		if refs[item.ID] {
			cited = append(cited, item)
			failed = failed || item.IsError
			if item.IsError {
				failureResultSeq = item.ResultSeq
			} else if failureResultSeq > 0 && item.CallSeq > failureResultSeq && len(item.Result) > 0 && string(item.Result) != "null" {
				reported = true
			}
		}
	}
	// This detects only a possible correction. The unchanged evidence gate still
	// decides whether the proposed successful path is actually supported.
	return cited, failed && reported
}

func (h *Host) finishCandidates(ctx context.Context, j *journal, candidates []state.Candidate, audit json.RawMessage) error {
	err := j.phase(ctx, "candidates", h.memoryComponent(), "", func(ctx context.Context) error {
		if err := h.Store.SaveCandidates(ctx, j.run.ID, candidates, audit); err != nil {
			return err
		}
		ids := make([]string, 0, len(candidates))
		for _, c := range candidates {
			ids = append(ids, c.ID)
		}
		message := fmt.Sprintf("%d candidates await review; pending candidates are excluded from recall", len(candidates))
		if h.Store.FileMemoryEnabled() {
			message = fmt.Sprintf("%d supported candidates saved for automatic comparison and publication", len(candidates))
		}
		return j.emit(event.Event{Type: "candidates.saved", Phase: "candidates", Component: h.memoryComponent(), Status: "completed", Message: message, Data: map[string]any{"candidate_ids": ids}})
	})
	if err != nil {
		return h.experienceFailure(j, err)
	}
	if h.Store.FileMemoryEnabled() {
		for _, candidate := range candidates {
			if _, err := h.acceptExperience(ctx, candidate.ID, "Automatically selected from recorded execution evidence", nil, true); err != nil {
				return h.experienceFailure(j, err)
			}
		}
	}
	return nil
}

func (h *Host) experienceFailure(j *journal, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := h.Store.Run(ctx, j.run.ID)
	if err != nil {
		return errors.Join(cause, err)
	}
	if r.ExperienceStatus == "pending" || r.ExperienceStatus == "failed" {
		return errors.Join(cause, h.Store.ExperienceFailure(ctx, r.ID, cause.Error()))
	}
	if h.Store.FileMemoryEnabled() && r.ExperienceStatus == "review_pending" {
		return errors.Join(cause, h.Store.ExperiencePublicationFailure(ctx, r.ID, cause.Error()))
	}
	return cause
}

func (h *Host) RetryExperience(ctx context.Context, id string) (state.Run, error) {
	h = h.partitioned()
	r, err := h.Store.Run(ctx, id)
	if err != nil {
		return r, err
	}
	if r.AutomaticMemorySkipReason() != "" {
		return r, errors.New("automatic experience retry is paused until project attribution is established")
	}
	if r.Scope != h.Config.Scope {
		return r, errors.New("run belongs to another workspace")
	}
	if r.MemoryStatus != "saved" && r.MemoryStatus != "unchanged" {
		return r, fmt.Errorf("source user memory is %s; finish retry-memory first", r.MemoryStatus)
	}
	if h.Store.FileMemoryEnabled() && r.ExecutionStatus == "completed" && r.ExperienceStatus == "review_pending" {
		j := &journal{h: h, run: r, start: time.Now()}
		candidates, err := h.Store.Candidates(ctx, r.Scope, r.ID, "pending")
		if err != nil {
			return r, err
		}
		for _, c := range candidates {
			if _, err := h.acceptExperience(ctx, c.ID, "Automatically selected; recovered publication", nil, true); err != nil {
				return h.receipt(j, h.experienceFailure(j, err))
			}
		}
		return h.receipt(j, nil)
	}
	if r.ExecutionStatus != "completed" || (r.ExperienceStatus != "pending" && r.ExperienceStatus != "failed") {
		return r, fmt.Errorf("run is not eligible for experience retry: execution=%s experience=%s", r.ExecutionStatus, r.ExperienceStatus)
	}
	j := &journal{h: h, run: r, start: time.Now()}
	err = h.finalizeExperience(ctx, j)
	return h.receipt(j, err)
}

// AcceptExperience only publishes the exact previously reviewed candidate.
// Jev checks current relationships; no generation or business execution runs.
func (h *Host) AcceptExperience(ctx context.Context, id, note string, expires *time.Time) (state.Memory, error) {
	return h.acceptExperience(ctx, id, note, expires, false)
}

func (h *Host) acceptExperience(ctx context.Context, id, note string, expires *time.Time, automatic bool) (state.Memory, error) {
	h = h.partitioned()
	if expires != nil && !expires.After(time.Now()) {
		return state.Memory{}, errors.New("memory expiration must be in the future")
	}
	c, err := h.Store.GetCandidate(ctx, id)
	if err != nil {
		return state.Memory{}, err
	}
	if c.Scope != h.Config.Scope {
		return state.Memory{}, errors.New("candidate belongs to another workspace")
	}
	if c.Status != "pending" {
		return state.Memory{}, fmt.Errorf("candidate is already %s", c.Status)
	}
	if h.Judge == nil {
		return state.Memory{}, errors.New("experience judge is required")
	}
	r, err := h.Store.Run(ctx, c.RunID)
	if err != nil {
		return state.Memory{}, err
	}
	j := &journal{h: h, run: r, start: time.Now()}
	var relation experience.Relations
	var existing []state.Memory
	var disabled map[string]bool
	var generation string
	if h.Store.FileMemoryEnabled() {
		generation, err = h.Store.MemoryFingerprint(ctx, []string{c.Scope})
		if err != nil {
			return state.Memory{}, err
		}
	}
	err = j.phase(ctx, "review", "memory.jev", h.Config.DecisionModel, func(ctx context.Context) error {
		var err error
		existing, disabled, err = h.comparisonMemories(ctx, c.Scope, state.ExperienceText(c.Draft))
		if err != nil {
			return err
		}
		relation, err = h.Judge.Compare(ctx, c, existing)
		status := "completed"
		if err != nil {
			status = "failed"
		}
		return errors.Join(err, j.emit(event.Event{Type: "candidate.compared", Phase: "review", Component: "memory.jev", Model: relation.Model, Status: status, Message: "compare reviewed candidate with current memory", Data: map[string]any{"candidate_id": c.ID, "relations": relation}}))
	})
	if err != nil {
		return state.Memory{}, err
	}
	for _, id := range append(append([]string(nil), relation.ConflictIDs...), relation.DuplicateIDs...) {
		if disabled[id] {
			if !automatic {
				return state.Memory{}, errors.New("experience relates to disabled knowledge; explicit restoration or correction is required")
			}
			if err := h.Store.RejectCandidate(ctx, c.ID, "Excluded by disabled or unresolved knowledge relation"); err != nil {
				return state.Memory{}, err
			}
			return state.Memory{}, j.emit(event.Event{Type: "candidate.excluded", Phase: "publish", Component: h.memoryComponent(), Status: "completed", Message: "disabled or unresolved lesson was not republished", Data: map[string]any{"candidate_id": c.ID, "excluded_memory_id": id, "relations": relation}})
		}
	}
	if len(relation.ConflictIDs) > 0 {
		relation.ConflictIDs = append(relation.ConflictIDs, relation.DuplicateIDs...)
		relation.DuplicateIDs = nil
	}
	revisions := make(map[string]int, len(existing))
	for _, m := range existing {
		if !disabled[m.ID] {
			revisions[m.ID] = m.Revision
		}
	}
	audit, err := json.Marshal(relation)
	if err != nil {
		return state.Memory{}, err
	}
	if strings.TrimSpace(note) == "" {
		note = "Accepted through local CLI"
	}
	var m state.Memory
	err = j.phase(ctx, "publish", h.memoryComponent(), "", func(ctx context.Context) error {
		var err error
		m, err = h.Store.AcceptCandidate(ctx, c.ID, state.CandidateAcceptance{MemoryID: ID(), Note: note, ExpiresAt: expires, ConflictIDs: relation.ConflictIDs, DuplicateIDs: relation.DuplicateIDs, ExpectedRevisions: revisions, ExpectedGeneration: generation, Decision: audit})
		if err != nil {
			return err
		}
		return j.emit(event.Event{Type: "candidate.accepted", Phase: "publish", Component: h.memoryComponent(), Status: "completed", Message: fmt.Sprintf("candidate=%s memory=%s status=%s", c.ID, m.ID, m.Status), Data: map[string]any{"candidate_id": c.ID, "memory": m}})
	})
	return m, err
}

// RestructureExperience edits organization only, using the adopted lesson's
// recorded source subset. It never runs the source task or adds source evidence.
func (h *Host) RestructureExperience(ctx context.Context, id string, revision int, note string) (state.Memory, error) {
	h = h.partitioned()
	m, err := h.Store.Memory(ctx, id)
	if err != nil {
		return state.Memory{}, err
	}
	if m.Scope != h.Config.Scope || m.Revision != revision || m.Status != "active" || m.Kind != "procedure" || m.SourceType != "reviewed_experience" {
		return state.Memory{}, errors.New("experience changed or is outside the active scope")
	}
	if h.Extractor == nil || h.Judge == nil {
		return state.Memory{}, errors.New("experience extraction and judgment are required")
	}
	run, err := h.Store.Run(ctx, m.SourceRunID)
	if err != nil {
		return state.Memory{}, err
	}
	all, err := h.Store.ToolEvidence(ctx, m.SourceRunID)
	if err != nil {
		return state.Memory{}, err
	}
	evidence := make([]state.ToolEvidence, 0, len(m.EvidenceIDs))
	for _, ref := range m.EvidenceIDs {
		for _, item := range all {
			if item.ID == ref {
				evidence = append(evidence, item)
				break
			}
		}
	}
	if len(evidence) != len(m.EvidenceIDs) {
		return state.Memory{}, errors.New("adopted experience source evidence is incomplete")
	}
	var extraction experience.Extraction
	if organizer, ok := h.Extractor.(experience.Restructurer); ok {
		extraction, err = organizer.Restructure(ctx, m, run, evidence)
	} else {
		extraction, err = h.Extractor.Extract(ctx, run, evidence, nil)
	}
	j := &journal{h: h, run: run, start: time.Now()}
	status := "completed"
	if err != nil {
		status = "failed"
	}
	if emitErr := j.emit(event.Event{Type: "experience.restructure.extracted", Phase: "structure", Component: "memory.extractor", Status: status, Data: map[string]any{"memory_id": id, "source_revision": revision, "extraction": extraction}}); emitErr != nil {
		return state.Memory{}, errors.Join(err, emitErr)
	}
	if err != nil {
		return state.Memory{}, err
	}
	if len(extraction.Drafts) != 1 {
		return state.Memory{}, errors.New("restructuring requires exactly one evidence-backed lesson")
	}
	draft := extraction.Drafts[0]
	failed := false
	for _, item := range evidence {
		failed = failed || item.IsError
	}
	if failed && len(draft.Avoid) == 0 {
		return state.Memory{}, errors.New("restructured lesson omitted observed failure avoidance")
	}
	assessment, err := h.Judge.Assess(ctx, draft, evidence)
	status = "completed"
	if err != nil {
		status = "failed"
	}
	if emitErr := j.emit(event.Event{Type: "experience.restructure.assessed", Phase: "structure", Component: "memory.jev", Status: status, Data: map[string]any{"memory_id": id, "source_revision": revision, "draft": draft, "assessment": assessment}}); emitErr != nil {
		return state.Memory{}, errors.Join(err, emitErr)
	}
	if err != nil {
		return state.Memory{}, err
	}
	if !assessment.Eligible {
		return state.Memory{}, errors.New("restructured experience did not pass usefulness and evidence screening")
	}
	audit, err := json.Marshal(map[string]any{"policy": "experience-structure-v1", "source_revision": revision, "draft": draft, "extraction": extraction, "assessment": assessment})
	if err != nil {
		return state.Memory{}, err
	}
	if strings.TrimSpace(note) == "" {
		note = "Separated successful steps from observed pitfalls using original evidence"
	}
	return h.Store.RestructureExperience(ctx, id, revision, draft, audit, note)
}
