package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-host/internal/memory"
	"agent-host/internal/state"
)

// ProcessExternal never runs a business task or manufactures a native run. The
// owning worker serializes this operation and resumes pending items on startup.
func (h *Host) ProcessExternal(ctx context.Context, r state.ExternalReceipt) error {
	current, err := h.Store.StartExternalReceipt(ctx, r.Connection.ID, r.ID)
	if err != nil {
		return err
	}
	r = current
	for i, result := range r.Results {
		if result.Status != "pending" {
			continue
		}
		p, err := h.assessExternal(ctx, r, i)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			p = state.ExternalPublication{Result: state.ExternalItemResult{Status: "failed", Reason: err.Error(), Audit: p.Result.Audit}}
		}
		if err := h.Store.FinishExternalItem(ctx, r.ID, i, p); err != nil {
			// Recover any authoritative publication before attempting a failure
			// receipt. A storage failure is never replaced with weaker admission.
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			persistErr := h.Store.FinishExternalItem(failureCtx, r.ID, i, state.ExternalPublication{Result: state.ExternalItemResult{Status: "failed", Reason: err.Error()}})
			cancel()
			if persistErr != nil {
				return errors.Join(err, persistErr)
			}
		}
	}
	return nil
}

func (h *Host) assessExternal(ctx context.Context, r state.ExternalReceipt, index int) (state.ExternalPublication, error) {
	p := state.ExternalPublication{Result: state.ExternalItemResult{Status: "excluded", Reason: "not_selected"}}
	item := r.Submission.Items[index]
	query := item.Text
	if item.Procedure != nil {
		query = state.ExperienceText(*item.Procedure)
	}
	var err error
	p.ExpectedGeneration, err = h.Store.MemoryFingerprint(ctx, []string{r.Connection.Scope})
	if err != nil {
		return p, err
	}
	existing, disabled, err := h.comparisonMemories(ctx, r.Connection.Scope, query)
	if err != nil {
		return p, err
	}
	m := state.Memory{ID: ID(), AgentID: r.Connection.AgentID, AudienceID: state.OwnerAudienceID, Scope: r.Connection.Scope, Status: "active", Revision: 1, ExternalReceiptID: r.ID, CreatedAt: time.Now().UTC(), EvidenceIDs: append([]string(nil), item.EvidenceIDs...)}
	switch item.Kind {
	case "statement":
		if h.Decider == nil {
			return p, errors.New("external statement decision provider is unavailable")
		}
		for _, e := range r.Submission.Evidence {
			if e.ID == item.EvidenceIDs[0] {
				m.SenderID = "external:" + e.Author
			}
		}
		d, err := h.Decider.Decide(memory.WithActor(ctx, m.SenderID), item.Text, existing)
		p.Result.Audit, _ = json.Marshal(d)
		if err != nil {
			return p, err
		}
		m.Kind, m.Text, m.SourceType = d.Kind, item.Text, "user_statement"
		p.ConflictIDs, p.DuplicateIDs = d.ConflictIDs, d.DuplicateIDs
		if !d.Store && len(d.DuplicateIDs) == 0 {
			return p, nil
		}
	case "procedure":
		if h.Judge == nil {
			return p, errors.New("external procedure assessment provider is unavailable")
		}
		draft := *item.Procedure
		assessment, err := h.Judge.Assess(ctx, draft, r.Submission.ToolEvidence())
		p.Result.Audit = assessment.Audit
		if err != nil {
			return p, err
		}
		if !assessment.Eligible {
			p.Result.Reason = "procedure_not_supported_or_durable"
			return p, nil
		}
		c := state.Candidate{ID: fmt.Sprintf("%s-%d", r.ID, index), Scope: r.Connection.Scope, Draft: draft, AgentID: m.AgentID, AudienceID: m.AudienceID}
		relations, err := h.Judge.Compare(ctx, c, existing)
		p.Result.Audit, _ = json.Marshal(map[string]any{"assessment": assessment, "comparison": relations})
		if err != nil {
			return p, err
		}
		m.Kind, m.Text, m.SourceType = "procedure", state.ExperienceText(draft), "reviewed_experience"
		m.Conditions, m.Steps, m.Avoid = draft.Conditions, append([]string(nil), draft.Steps...), append([]string(nil), draft.Avoid...)
		// Only the Host-configured scope contributes an entity identity.
		m.Entities = []string{"project:" + m.Scope}
		m.ReviewNote = "Host-selected procedure from external-agent supplied evidence; not Host-observed execution"
		p.ConflictIDs, p.DuplicateIDs = relations.ConflictIDs, relations.DuplicateIDs
	default:
		return p, errors.New("unknown external item kind")
	}
	for _, id := range append(append([]string(nil), p.ConflictIDs...), p.DuplicateIDs...) {
		if disabled[id] {
			p.Result.Reason = "disabled_or_unresolved_memory_relation"
			p.ConflictIDs, p.DuplicateIDs = nil, nil
			return p, nil
		}
	}
	if len(p.ConflictIDs) > 0 {
		p.DuplicateIDs = nil
	}
	m.RevisionDecision = p.Result.Audit
	p.Memory = &m
	p.Result.Status = "published"
	p.Result.Reason = "selected"
	if len(p.DuplicateIDs) > 0 {
		p.Memory = nil
		p.Result.Reason = "duplicate_existing_memory"
	}
	return p, nil
}
