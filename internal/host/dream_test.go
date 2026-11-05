package host

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"testing"

	"agent-host/internal/dream"
	"agent-host/internal/harness"
)

type codexDreamRunner struct{ calls int }

func (r *codexDreamRunner) Identity() harness.Identity { return harness.Identity{Engine: "codex"} }
func (r *codexDreamRunner) Run(context.Context, harness.Request, func(harness.Observation) error) (harness.Result, error) {
	r.calls++
	return harness.Result{}, nil
}

func TestDreamExtractorRejectsUnsupportedBackendBeforeDispatch(t *testing.T) {
	r := &codexDreamRunner{}
	_, err := (DreamExtractor{Runner: r, Model: "codex", Backend: "codex"}).Extract(context.Background(), dream.ExtractionInput{})
	if err == nil || r.calls != 0 {
		t.Fatalf("unsupported backend dispatched: calls=%d err=%v", r.calls, err)
	}
}

type claudeDreamProbe struct{ t *testing.T }

func (r claudeDreamProbe) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	if _, err := uuid.Parse(req.NativeSessionID); err != nil {
		r.t.Fatalf("missing fresh native session: %v", err)
	}
	if req.Resume || !req.ToolsDisabled || !req.Extraction || emit == nil {
		r.t.Fatal("extraction isolation missing")
	}
	return harness.Result{Status: "completed", StructuredOutput: json.RawMessage(`{"drafts":[]}`)}, nil
}
func TestDreamExtractorSuppliesFreshIsolatedSession(t *testing.T) {
	_, err := (DreamExtractor{Runner: claudeDreamProbe{t}, Model: "test", Backend: "claude"}).Extract(context.Background(), dream.ExtractionInput{})
	if err != nil {
		t.Fatal(err)
	}
}
