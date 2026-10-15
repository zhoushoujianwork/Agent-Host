package cli

import (
	"context"
	"encoding/json"

	"agent-host/internal/objectmemory"
	"agent-host/internal/state"
)

type fixtureObjects struct{}

func (fixtureObjects) Extract(_ context.Context, in objectmemory.Input) (objectmemory.Extraction, error) {
	e := in.Evidence[0]
	return objectmemory.Extraction{Objects: []state.MemoryObject{{Name: e.Author, Kind: "person", Evidence: e, Source: in.Source}}, Audit: json.RawMessage(`{"policy":"test-object-extraction","status":"identified"}`)}, nil
}
