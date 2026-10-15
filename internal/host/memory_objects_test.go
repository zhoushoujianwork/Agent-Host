package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/annotation"
	"agent-host/internal/memory"
	"agent-host/internal/objectmemory"
	"agent-host/internal/state"
)

type fixtureObjects struct{}

func (fixtureObjects) Extract(_ context.Context, in objectmemory.Input) (objectmemory.Extraction, error) {
	var objects []state.MemoryObject
	for _, e := range in.Evidence {
		for _, o := range annotation.Extract("fixture", e.Text).Objects {
			objects = append(objects, state.MemoryObject{Name: o.Name, Kind: o.Kind, Source: in.Source, Evidence: e})
			break
		}
		if len(objects) > 0 {
			break
		}
	}
	if len(objects) == 0 {
		e := in.Evidence[0]
		objects = []state.MemoryObject{{Name: e.Author, Kind: "person", Source: in.Source, Evidence: e}}
		if e.Role == "tool" {
			objects[0].Name, objects[0].Kind = e.Tool.Name, "tool"
		}
	}
	return objectmemory.Extraction{Objects: objects, Audit: json.RawMessage(`{"policy":"test-object-extraction","status":"identified"}`)}, nil
}

type objectFunc func(context.Context, objectmemory.Input) (objectmemory.Extraction, error)

func (f objectFunc) Extract(ctx context.Context, in objectmemory.Input) (objectmemory.Extraction, error) {
	return f(ctx, in)
}

func TestMissingObjectsBlocksNativePublicationWithoutReplay(t *testing.T) {
	h, runner := testHost(t)
	h.Objects = nil
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) { return saveStatement(), nil })
	r, err := h.Run(t.Context(), newSession(t, h), "Use concise answers.")
	if err == nil || !strings.Contains(err.Error(), "object extraction provider") || r.ExecutionStatus != "completed" || r.MemoryStatus != "failed" {
		t.Fatal(r, err)
	}
	h.Objects = fixtureObjects{}
	r, err = h.RetryMemory(t.Context(), r.ID)
	if err != nil || r.MemoryStatus != "saved" || len(runner.requests) != 1 {
		t.Fatal("retry replayed native work", r, err, len(runner.requests))
	}
	items, err := h.Store.Memories(t.Context(), h.Config.Scope)
	if err != nil || len(items) != 1 || len(items[0].Objects) != 1 {
		t.Fatal(items, err)
	}
	context, err := h.Store.Retrieve(t.Context(), h.Config.Scope, "What should I do next?", 10)
	if err != nil || len(context.Persistent) != 1 {
		t.Fatal("personal subject lost global preference", context, err)
	}
}

func TestMissingAndUnresolvedObjectsBlockExternalPublication(t *testing.T) {
	for _, procedure := range []bool{false, true} {
		h, c := externalHost(t)
		h.Objects = nil
		r := processedExternal(t, h, submitExternal(t, h, c, "missing-provider", procedure))
		if r.Results[0].Status != "failed" || r.Results[0].MemoryID != "" {
			t.Fatal(r.Results)
		}
		h.Objects = objectFunc(func(context.Context, objectmemory.Input) (objectmemory.Extraction, error) {
			return objectmemory.Extraction{}, errors.New("object_unresolved: no supported subject")
		})
		r = processedExternal(t, h, submitExternal(t, h, c, "unresolved", procedure))
		if r.Results[0].Status != "failed" || !strings.Contains(r.Results[0].Reason, "object_unresolved") {
			t.Fatal(r.Results)
		}
	}
}

func TestNativeProcedureRequiresObjectExtractionBeforeAcceptance(t *testing.T) {
	h, runner, _ := experienceHost(t)
	h.Objects = nil
	r := runOK(t, h, newSession(t, h), "Fix the fixture test")
	c := onlyCandidate(t, h, r.ID)
	if _, err := h.AcceptExperience(t.Context(), c.ID, "publish", nil); err == nil || !strings.Contains(err.Error(), "object extraction provider") {
		t.Fatal("missing object provider accepted", err)
	}
	c, err := h.Store.GetCandidate(t.Context(), c.ID)
	if err != nil || c.Status != "pending" {
		t.Fatal("failed extraction changed candidate", c, err)
	}
	h.Objects = fixtureObjects{}
	m, err := h.AcceptExperience(t.Context(), c.ID, "publish", nil)
	if err != nil || len(m.Objects) == 0 || len(runner.requests) != 1 {
		t.Fatal(m, err, len(runner.requests))
	}
}
