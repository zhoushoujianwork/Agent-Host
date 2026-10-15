package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"agent-host/internal/experience"
	"agent-host/internal/objectmemory"
	"agent-host/internal/state"
)

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (h *Host) identifyObjects(ctx context.Context, in objectmemory.Input, declared []state.MemoryObject) (objectmemory.Extraction, error) {
	if len(declared) > 0 {
		if err := state.ValidateMemoryObjects(declared); err != nil {
			return objectmemory.Extraction{}, err
		}
		return objectmemory.Extraction{Objects: declared, Audit: json.RawMessage(`{"policy":"required-object-extraction-v1","method":"explicit-source-association","status":"identified"}`)}, nil
	}
	if h.Objects == nil {
		return objectmemory.Extraction{}, errors.New("object extraction provider is required before memory publication")
	}
	out, err := h.Objects.Extract(ctx, in)
	if err != nil {
		return out, err
	}
	if len(out.Objects) == 0 || !json.Valid(out.Audit) {
		return out, errors.New("object extraction returned no audited subjects")
	}
	return out, state.ValidateMemoryObjects(out.Objects)
}

func nativeObjectInput(run state.Run, text, kind string, evidence []state.ToolEvidence) objectmemory.Input {
	now := time.Now().UTC()
	author := "local-owner"
	if run.Source != nil && run.Source.SenderID != "" {
		author = run.Source.SenderID
	}
	in := objectmemory.Input{Text: text, Kind: kind, Source: state.ExternalSource{Entry: "agent-host-native", Conversation: run.SessionID, Turn: run.ID, ObservedAt: now}, Evidence: []state.ExternalEvidence{{ID: "user:" + run.ID, Role: "user", Author: author, Text: run.Input, ObservedAt: now}}}
	for _, e := range experience.LearningEvidence(evidence) {
		isError := e.IsError
		in.Evidence = append(in.Evidence, state.ExternalEvidence{ID: e.ID, Role: "tool", Author: "agent-host-native", Text: "Recorded tool call and result; source supplied below.", ObservedAt: now, Tool: &state.ExternalTool{Name: e.Name, Input: e.Input, Result: e.Result, IsError: &isError}})
	}
	return in
}

// ExtractMemoryObjects repairs one existing record from its retained evidence.
// It never resumes the business task or invents a user confirmation.
func (h *Host) ExtractMemoryObjects(ctx context.Context, scope, id string, revision int) (state.Memory, error) {
	if revision < 1 {
		return state.Memory{}, errors.New("object extraction repair requires an expected revision")
	}
	m, err := h.Store.ContextMemory(ctx, scope, id, revision)
	if err != nil {
		return m, err
	}
	var in objectmemory.Input
	if m.ExternalReceiptID != "" {
		source, evidence, err := h.Store.ExternalObjectEvidence(ctx, m)
		if err != nil {
			return m, err
		}
		in = objectmemory.Input{Text: m.Text, Kind: m.Kind, Source: source, Evidence: evidence}
	} else {
		run, err := h.Store.Run(ctx, m.SourceRunID)
		if err != nil {
			return m, err
		}
		evidence, err := h.Store.ToolEvidence(ctx, run.ID)
		if err != nil {
			return m, err
		}
		selected := []state.ToolEvidence{}
		for _, e := range evidence {
			for _, id := range m.EvidenceIDs {
				if e.ID == id {
					selected = append(selected, e)
				}
			}
		}
		in = nativeObjectInput(run, m.Text, m.Kind, selected)
	}
	out, err := h.identifyObjects(ctx, in, nil)
	if err != nil {
		return m, err
	}
	return h.Store.AssociateMemoryObjects(ctx, scope, m.ID, m.Revision, out.Objects, out.Audit)
}
