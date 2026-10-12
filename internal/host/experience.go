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
	if len(extraction.Drafts) > 0 {
		err = j.phase(ctx, "assess", "memory.jev", h.Config.DecisionModel, func(ctx context.Context) error {
			for _, draft := range extraction.Drafts {
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
					continue
				}
				entities, err := state.EvidenceEntitiesForRun(run, draft, evidence)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(assessment)
				if err != nil {
					return err
				}
				candidates = append(candidates, state.Candidate{ID: ID(), RunID: run.ID, Scope: run.Scope, Draft: draft, Entities: entities, Status: "pending", Model: extraction.Model, Assessment: raw, CreatedAt: time.Now().UTC()})
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
	return h.finishCandidates(ctx, j, candidates, audit)
}

func (h *Host) finishCandidates(ctx context.Context, j *journal, candidates []state.Candidate, audit json.RawMessage) error {
	err := j.phase(ctx, "candidates", "memory.sqlite", "", func(ctx context.Context) error {
		if err := h.Store.SaveCandidates(ctx, j.run.ID, candidates, audit); err != nil {
			return err
		}
		ids := make([]string, 0, len(candidates))
		for _, c := range candidates {
			ids = append(ids, c.ID)
		}
		return j.emit(event.Event{Type: "candidates.saved", Phase: "candidates", Component: "memory.sqlite", Status: "completed", Message: fmt.Sprintf("%d candidates await review; pending candidates are excluded from recall", len(candidates)), Data: map[string]any{"candidate_ids": ids}})
	})
	if err != nil {
		return h.experienceFailure(j, err)
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
	err = j.phase(ctx, "review", "memory.jev", h.Config.DecisionModel, func(ctx context.Context) error {
		var err error
		existing, err = h.Store.Memories(ctx, c.Scope)
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
	if len(relation.ConflictIDs) > 0 {
		relation.ConflictIDs = append(relation.ConflictIDs, relation.DuplicateIDs...)
		relation.DuplicateIDs = nil
	}
	revisions := make(map[string]int, len(existing))
	for _, m := range existing {
		revisions[m.ID] = m.Revision
	}
	audit, err := json.Marshal(relation)
	if err != nil {
		return state.Memory{}, err
	}
	if strings.TrimSpace(note) == "" {
		note = "Accepted through local CLI"
	}
	var m state.Memory
	err = j.phase(ctx, "publish", "memory.sqlite", "", func(ctx context.Context) error {
		var err error
		m, err = h.Store.AcceptCandidate(ctx, c.ID, state.CandidateAcceptance{MemoryID: ID(), Note: note, ExpiresAt: expires, ConflictIDs: relation.ConflictIDs, DuplicateIDs: relation.DuplicateIDs, ExpectedRevisions: revisions, Decision: audit})
		if err != nil {
			return err
		}
		return j.emit(event.Event{Type: "candidate.accepted", Phase: "publish", Component: "memory.sqlite", Status: "completed", Message: fmt.Sprintf("candidate=%s memory=%s status=%s", c.ID, m.ID, m.Status), Data: map[string]any{"candidate_id": c.ID, "memory": m}})
	})
	return m, err
}
