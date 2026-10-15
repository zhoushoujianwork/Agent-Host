package channel

import (
	"agent-host/internal/objectmemory"
	"agent-host/internal/state"
	"context"
	"encoding/json"
)

type fixtureObjectExtractor struct{}

func (fixtureObjectExtractor) Extract(_ context.Context, in objectmemory.Input) (objectmemory.Extraction, error) {
	e := in.Evidence[0]
	return objectmemory.Extraction{Objects: []state.MemoryObject{{Name: e.Author, Kind: "person", Source: in.Source, Evidence: e}}, Audit: json.RawMessage(`{"policy":"test-object-extraction","status":"identified"}`)}, nil
}
