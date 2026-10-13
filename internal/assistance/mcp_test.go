package assistance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/discovery"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func contextFixture(t *testing.T) (*state.Store, Binding, discovery.Config, []state.DiscoveryEvidence) {
	t.Helper()
	ctx := t.Context()
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project, err := store.RegisterProject(ctx, "app", "Application", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := discovery.Config{DWSExecutable: os.Args[0], Profile: "corp:owner", IntervalSeconds: 300, InitialWindowHours: 24, ReconciliationWindowHours: 6, RetentionDays: 30, PageLimit: 2, MaxMessages: 200, TimeoutSeconds: 180, Model: "fixture", Sources: []discovery.Source{{ConversationID: "selected", Type: "dm", PeerOpenDingTalkID: "peer", ProjectID: project.ID}}}
	source := cfg.Sources[0].Attribution(cfg)
	now := time.Now().UTC().Truncate(time.Millisecond)
	e := state.PrepareDiscoveryEvidence(state.DiscoveryEvidence{SourceID: source.ID, Profile: source.Profile, TenantID: source.TenantID, PrincipalID: source.PrincipalID, ConversationID: source.ConversationID, ProjectID: project.ID, MessageID: "probe-question", AuthorID: "owner", Text: "部署之前应该用哪个 readiness probe？", CreatedAt: now, Availability: "available"})
	evidence := []state.DiscoveryEvidence{e}
	c := state.DiscoveryCollection{From: now.Add(-time.Hour), To: now.Add(time.Hour), At: now, Complete: true, Count: 1}
	if _, err = store.SaveDiscoveryCollection(ctx, source, c, evidence); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveMatterExtraction(ctx, source.ID, state.AssistanceDigest(evidence), "fixture", []state.MatterDraft{{Kind: "question", Status: "open", Title: "部署 readiness probe", Summary: "Owner asked which readiness probe deployment should use; no answer was found in the collected window.", OpenItem: e.Text, EvidenceIDs: []string{e.ID}}}, json.RawMessage(`{"policy":"owner-project-context-v2"}`), ""); err != nil {
		t.Fatal(err)
	}
	refs := []state.ProjectRef{{ID: project.ID, Name: project.Name, Scope: project.Scope, Workspace: project.Workspace}}
	session := state.Session{ID: "source-session", Scope: project.Scope, ProjectID: project.ID, ProjectName: project.Name, Workspace: project.Workspace, Projects: refs, NativeSessionID: "source-native", RuntimeFingerprint: "runtime", Model: "fixture"}
	if err = store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: "memory-source", SessionID: session.ID, Scope: session.Scope, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Workspace: session.Workspace, Projects: refs, RuntimeFingerprint: session.RuntimeFingerprint, NativeSessionID: session.NativeSessionID, Input: "The deployment readiness probe is /readyz and must report READY before release."}
	if err = store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteRun(ctx, run.ID, "completed", "Acknowledged.", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err = store.FinalizeMemory(ctx, run.ID, []state.MemoryUpdate{{Memory: state.Memory{ID: "probe-memory", Scope: run.Scope, Kind: "project_fact", Text: run.Input, SourceRunID: run.ID}}}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	run.ID = "current-run"
	run.Input = "What remains open and what deployment readiness guidance is already saved?"
	if err = store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "discovery.json")
	raw, _ := json.Marshal(cfg)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(ctx, home, path, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, b, cfg, evidence
}

func encoded(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContextSearchReadCitationsAndEligibilityAreReadOnly(t *testing.T) {
	store, b, _, evidence := contextFixture(t)
	ctx := t.Context()
	reader, err := state.Open(b.Home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result, err := b.Call(ctx, reader, "search_context", json.RawMessage(`{"query":"readiness"}`))
	if err != nil {
		t.Fatal(err)
	}
	var hits struct {
		Items []struct {
			Reference string `json:"reference"`
			Role      string `json:"role"`
		} `json:"items"`
	}
	if err = json.Unmarshal(encoded(t, result), &hits); err != nil || len(hits.Items) != 2 {
		t.Fatalf("retrieval %+v %v", hits, err)
	}
	for _, hit := range hits.Items {
		if _, err = b.Call(ctx, reader, "read_context", encoded(t, map[string]any{"reference": hit.Reference})); err != nil {
			t.Fatal(err)
		}
		source, err := b.Call(ctx, reader, "read_context_evidence", encoded(t, map[string]any{"reference": hit.Reference}))
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded(t, source))
		if hit.Role == "current_matter" && (!strings.Contains(text, evidence[0].MessageID) || !strings.Contains(text, evidence[0].AuthorID)) {
			t.Fatal("matter citation lost original identity")
		}
		if hit.Role == "durable_memory" && !strings.Contains(text, "/readyz") {
			t.Fatal("memory source not read")
		}
	}
	if err = store.InvalidateMemory(ctx, "probe-memory", "obsolete"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"read_context", "read_context_evidence"} {
		if _, err = b.Call(ctx, reader, tool, json.RawMessage(`{"reference":"memory/probe-memory/1"}`)); err == nil {
			t.Fatal("disabled record exposed by", tool)
		}
	}
	result, err = b.Call(ctx, reader, "search_context", json.RawMessage(`{"query":"readiness","kind":"memory"}`))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(encoded(t, result), &hits)
	if len(hits.Items) != 0 {
		t.Fatal("invalidated memory returned in search")
	}
	matters, _ := store.SourceMatters(ctx, b.Sources[0])
	if len(matters) != 1 || matters[0].Revision != 1 {
		t.Fatal("read tools changed a matter")
	}
	run, _ := store.Run(ctx, b.RunID)
	if run.ExecutionStatus != "running" || run.Preparation != nil {
		t.Fatal("read tool selected a task")
	}
}

func TestContextBindingRejectsForeignScopeChangedGrantsAndEndedTasks(t *testing.T) {
	store, b, cfg, _ := contextFixture(t)
	ctx := t.Context()
	reader, err := state.Open(b.Home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, name := range []string{"read_context", "read_context_evidence"} {
		if _, err = b.Call(ctx, reader, name, json.RawMessage(`{"reference":"memory/foreign-memory/1"}`)); err == nil {
			t.Fatal("guessed reference accepted")
		}
	}
	if _, err = b.Call(ctx, reader, "search_context", json.RawMessage(`{"query":"readiness","project_id":"other"}`)); err == nil {
		t.Fatal("caller widened project scope")
	}
	cfg.Sources[0].ConversationID = "different-selection"
	os.WriteFile(b.ConfigPath, encoded(t, cfg), 0600)
	if _, err = b.Call(ctx, reader, "search_context", json.RawMessage(`{}`)); err == nil {
		t.Fatal("removed source still accessible")
	}
	if err = store.CompleteRun(ctx, b.RunID, "completed", "done", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = Resolve(ctx, b.Home, "", b.RunID); err == nil {
		t.Fatal("ended task binding reused")
	}
}

func TestContextMCPUsesStrictTransportWithoutMutationTools(t *testing.T) {
	_, b, _, _ := contextFixture(t)
	requests := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "publish_memory", "arguments": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "search_context", "arguments": map[string]any{"query": "部署", "kind": "matters"}}},
	}
	var in, out bytes.Buffer
	for _, r := range requests {
		in.Write(encoded(t, r))
		in.WriteByte('\n')
	}
	if err := Serve(t.Context(), b, &in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "readOnlyHint") || !strings.Contains(out.String(), "tool is not enabled") || !strings.Contains(out.String(), "current_matter") {
		t.Fatal("MCP surface lost strict/read-only behavior", out.String())
	}
}

type runnerFunc func(context.Context, harness.Request, func(harness.Observation) error) (harness.Result, error)

func (f runnerFunc) Run(ctx context.Context, r harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	return f(ctx, r, emit)
}

func TestProcessorSkipsUnchangedWindowsAndPreservesRetryableFailures(t *testing.T) {
	store, b, _, evidence := contextFixture(t)
	ctx := t.Context()
	source, _ := store.DiscoverySource(ctx, b.Sources[0])
	calls := 0
	p := Processor{Store: store, Model: "fixture", Runner: runnerFunc(func(context.Context, harness.Request, func(harness.Observation) error) (harness.Result, error) {
		calls++
		return harness.Result{}, nil
	})}
	if err := p.Reconcile(ctx, source, evidence); err != nil || calls != 0 {
		t.Fatal("unchanged evidence re-extracted", calls, err)
	}
	changed := evidence[0]
	changed.MessageID = "answer"
	changed.Text = "The readiness question is answered."
	changed.CreatedAt = changed.CreatedAt.Add(time.Second)
	changed = state.PrepareDiscoveryEvidence(changed)
	evidence = append(evidence, changed)
	c := *source.LastCollection
	c.Count = 2
	c.At = time.Now().UTC()
	store.SaveDiscoveryCollection(ctx, source, c, evidence)
	if err := p.Reconcile(ctx, source, evidence); err == nil || calls != 1 {
		t.Fatal("native incomplete result hidden", calls, err)
	}
	job, _ := store.AssistanceJob(ctx, source.ID)
	if job.Error == "" {
		t.Fatal("failed processing not retained")
	}
	list, _ := store.Suggestions(ctx, b.Run.ProjectID, b.Sources)
	if len(list) != 0 {
		t.Fatal("failed processing left suggestions")
	}
	if err := p.Reconcile(ctx, source, evidence); err == nil || calls != 2 {
		t.Fatal("failed processing not retried")
	}
}
