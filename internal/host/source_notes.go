package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"agent-host/internal/objectmemory"
	"agent-host/internal/state"
)

// Source notes are attributed quotations, not selected user assertions or
// demonstrated procedures. No command inside a quotation is executed.
func (h *Host) sourceNotePublication(ctx context.Context, r state.ExternalReceipt, index int, p state.ExternalPublication) (state.ExternalPublication, error) {
	item := r.Submission.Items[index]
	text := state.SourceNoteText(item)
	known, found, err := h.Store.ExactSourceNote(ctx, r.Connection.Scope, text)
	if err != nil {
		return p, err
	}
	if found && (known.SourceType != state.SourceNoteSource || known.Kind != state.SourceNoteKind || known.Status != "active" || known.ExpiresAt != nil && !known.ExpiresAt.After(time.Now())) {
		p.Result.Reason = "source_note_not_eligible_for_reuse"
		return p, nil
	}
	objects, err := h.identifyObjects(ctx, objectmemory.Input{Text: text, Kind: state.SourceNoteKind, Source: r.Submission.Source, Evidence: r.Submission.Evidence}, r.Submission.ItemObjects(index))
	if err != nil {
		return p, err
	}
	if len(objects.Objects) == 0 {
		return p, errors.New("source note requires source-grounded subjects")
	}
	p.Objects = objects.Objects
	p.Result.Status, p.Result.Reason = "published", "imported_source_note"
	p.Result.Audit, _ = json.Marshal(map[string]any{"policy": "attributed-source-note-import-v1", "basis": "verbatim non-error externally supplied tool result; content not verified as current fact or successful execution", "object_extraction": objects.Audit})
	if found {
		p.DuplicateIDs = []string{known.ID}
		p.Result.Reason = "duplicate_existing_memory"
		return p, nil
	}
	p.Memory = &state.Memory{ID: ID(), AgentID: r.Connection.AgentID, AudienceID: r.Connection.AudienceID, Scope: r.Connection.Scope, Kind: state.SourceNoteKind, Text: text, Status: "active", Revision: 1, CreatedAt: time.Now().UTC(), SourceType: state.SourceNoteSource, ExternalReceiptID: r.ID, EvidenceIDs: append([]string(nil), item.EvidenceIDs...), Objects: objects.Objects, ObjectAudit: objects.Audit, RevisionDecision: p.Result.Audit, ReviewNote: "Imported source quotation; not a verified statement or reviewed execution lesson"}
	return p, nil
}
