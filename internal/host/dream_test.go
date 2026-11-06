package host

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

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

func TestDreamPromptBoundsAndOrdersMultiMegabyteSources(t *testing.T) {
	now := time.Date(2026, 11, 5, 6, 7, 8, 9, time.UTC)
	multibyte := bytes.Repeat([]byte("界"), 3<<20)
	documents := []dream.Document{
		{Path: "zeta.log", Body: bytes.Repeat([]byte("z"), 2<<20)},
		{Path: "host-tool-evidence.json", Body: bytes.Repeat([]byte("e"), 2<<20)},
		{Path: "stdout.txt", Body: multibyte},
		{Path: "launch.json", Body: bytes.Repeat([]byte("l"), 2<<20)},
		{Path: "request.txt", Body: bytes.Repeat([]byte("r"), 2<<20)},
		{Path: "alpha.log", Body: bytes.Repeat([]byte("a"), 2<<20)},
	}
	originals := make([][]byte, len(documents))
	for i := range documents {
		originals[i] = append([]byte(nil), documents[i].Body...)
	}
	in := dream.ExtractionInput{Attribution: "host_observed", Documents: documents}
	prompt, coverage, err := buildDreamPrompt(in, now)
	if err != nil {
		t.Fatal(err)
	}
	promptAgain, _, err := buildDreamPrompt(in, now)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != promptAgain {
		t.Fatal("bounded prompt is not deterministic")
	}
	if len(prompt) > dreamPromptLimit {
		t.Fatalf("prompt size = %d, limit = %d", len(prompt), dreamPromptLimit)
	}
	if !utf8.ValidString(prompt) {
		t.Fatal("prompt is not valid UTF-8")
	}
	orderedRefs := []string{"artifact:request.txt", "artifact:launch.json", "artifact:stdout.txt", "artifact:host-tool-evidence.json", "artifact:alpha.log", "artifact:zeta.log"}
	last := -1
	for _, ref := range orderedRefs {
		position := strings.Index(prompt, "--- "+ref+" [")
		if position <= last {
			t.Fatalf("source %s out of order in prompt", ref)
		}
		last = position
		if !coverage.incompleteArtifacts[ref] {
			t.Fatalf("missing incomplete coverage for %s", ref)
		}
	}
	if !coverage.toolEvidenceMissing || !strings.Contains(prompt, "OMITTED") || !strings.Contains(prompt, "SOURCE BYTES") {
		t.Fatal("prompt lacks explicit incomplete-source coverage")
	}
	for i := range documents {
		if !bytes.Equal(documents[i].Body, originals[i]) {
			t.Fatalf("source document %s was modified", documents[i].Path)
		}
	}
}

type dreamStructuredRunner struct {
	t      *testing.T
	output json.RawMessage
}

type dreamSkillTargetProbe struct{ t *testing.T }

func (r dreamSkillTargetProbe) Run(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	for _, field := range []string{"target_skill_id", "target_skill_revision", "target_skill_digest"} {
		if !strings.Contains(req.JSONSchema, field) {
			r.t.Fatalf("dream schema omitted %s", field)
		}
	}
	if !strings.Contains(req.Prompt, "Offered-only Skills are not targets") || !strings.Contains(req.Prompt, "kind loaded or reference_read") {
		r.t.Fatal("dream prompt did not constrain Skill targets to actual completed evidence")
	}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	return harness.Result{Status: "completed", StructuredOutput: json.RawMessage(`{"drafts":[{"kind":"procedure","text":"Correct the loaded Skill.","inferred":false,"expires_at":"` + expires + `","source_refs":["artifact:canonical-skill-evidence.json"],"target_skill_id":"managed","target_skill_revision":2,"target_skill_digest":"digest"}]}`)}, nil
}

func TestDreamExtractorAllowsExactSkillRevisionTargetFields(t *testing.T) {
	drafts, err := (DreamExtractor{Runner: dreamSkillTargetProbe{t}, Model: "test", Backend: "claude"}).Extract(context.Background(), dream.ExtractionInput{})
	if err != nil || len(drafts) != 1 || drafts[0].TargetSkillID != "managed" || drafts[0].TargetSkillRevision != 2 || drafts[0].TargetSkillDigest != "digest" {
		t.Fatalf("Skill target fields were not retained: %+v %v", drafts, err)
	}
}

func (r dreamStructuredRunner) Run(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	if len(req.Prompt) > dreamPromptLimit {
		r.t.Fatalf("runner received oversized prompt: %d", len(req.Prompt))
	}
	return harness.Result{Status: "completed", StructuredOutput: r.output}, nil
}

func TestDreamExtractorExcludesProcedureCitingTruncatedArtifact(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	output := json.RawMessage(`{"drafts":[` +
		`{"kind":"procedure","text":"Treat the run as successful.","inferred":false,"expires_at":"` + expires + `","source_refs":["artifact:stdout.txt"]},` +
		`{"kind":"knowledge","text":"A partial output mentioned completion.","inferred":false,"expires_at":"` + expires + `","source_refs":["artifact:stdout.txt"]}` +
		`]}`)
	runner := dreamStructuredRunner{t: t, output: output}
	drafts, err := (DreamExtractor{Runner: runner, Model: "test", Backend: "claude"}).Extract(context.Background(), dream.ExtractionInput{Documents: []dream.Document{{Path: "stdout.txt", Body: bytes.Repeat([]byte("completed\n"), 100<<10)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].Kind != "knowledge" {
		t.Fatalf("unexpected retained drafts: %#v", drafts)
	}
}

func TestDreamExtractorExcludesToolProcedureWhenHostEvidenceTruncated(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	output := json.RawMessage(`{"drafts":[{"kind":"procedure","text":"Repeat the tool operation.","inferred":false,"expires_at":"` + expires + `","source_refs":["tool:evidence-1"]}]}`)
	runner := dreamStructuredRunner{t: t, output: output}
	drafts, err := (DreamExtractor{Runner: runner, Model: "test", Backend: "claude"}).Extract(context.Background(), dream.ExtractionInput{Documents: []dream.Document{{Path: "host-tool-evidence.json", Body: bytes.Repeat([]byte("x"), 256<<10)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Fatalf("retained procedure backed by incomplete tool evidence: %#v", drafts)
	}
}
