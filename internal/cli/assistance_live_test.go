package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/assistance"
	"agent-host/internal/discovery"
	"agent-host/internal/harness"
	"agent-host/internal/harness/claude"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

// The native retrieval probe has no prefetched facts, native history or supplied
// record IDs. It verifies paired tool results and the final cited answer.
func probeNativeContext(t *testing.T, ctx context.Context, store *state.Store, o options, prompt, expectedMemory, expectedMatter, expectedText string) (harness.Result, map[string]bool) {
	t.Helper()
	session := state.Session{ID: host.ID(), Scope: o.scope, ProjectID: o.projectID, ProjectName: o.projectName, Workspace: o.workspace, Projects: o.projects, NativeSessionID: host.ID(), RuntimeFingerprint: o.runtimeFingerprint, Model: o.model, AgentID: o.agentID, AudienceID: o.audienceID}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: host.ID(), SessionID: session.ID, Scope: session.Scope, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Workspace: session.Workspace, Projects: session.Projects, NativeSessionID: session.NativeSessionID, RuntimeFingerprint: session.RuntimeFingerprint, AgentID: session.AgentID, AudienceID: session.AudienceID, Input: prompt}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	binding, err := assistance.Resolve(ctx, o.home, o.discoveryConfig, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	tools := []string{"mcp__host_context__search_context", "mcp__host_context__read_context", "mcp__host_context__read_context_evidence", "mcp__host_context__list_private_suggestions"}
	calls := map[string]string{}
	proof := map[string]bool{}
	trace := []harness.ToolObservation{}
	result, err := claude.New(o.binary).Run(ctx, harness.Request{Workspace: o.workspace, Model: o.model, NativeSessionID: session.NativeSessionID, HostInstructions: assistance.Instructions, Prompt: prompt, AllowedTools: tools, MCPServers: []harness.MCPServer{binding.Server(os.Getenv("AGENT_HOST_LIVE_HOST"))}}, func(obs harness.Observation) error {
		if obs.Tool == nil {
			return nil
		}
		tool := obs.Tool
		trace = append(trace, *tool)
		if tool.Stage == "call" {
			if tool.Name != "ToolSearch" && !strings.HasPrefix(tool.Name, "mcp__host_context__") {
				return fmt.Errorf("unexpected native acceptance tool %s", tool.Name)
			}
			calls[tool.ID] = tool.Name
			return nil
		}
		if tool.IsError || calls[tool.ID] == "" {
			return errors.New("native context read failed or lacks a paired call")
		}
		text, err := nativeContextText(tool.Result)
		if err != nil {
			return err
		}
		name := calls[tool.ID]
		if strings.Contains(name, "search_context") {
			if expectedMemory != "" && strings.Contains(text, expectedMemory) {
				proof["memory_search"] = true
			}
			if expectedMatter != "" && strings.Contains(text, expectedMatter) {
				proof["matter_search"] = true
			}
		}
		if strings.Contains(name, "read_context") && !strings.Contains(name, "evidence") {
			if expectedMemory != "" && strings.Contains(text, expectedMemory) {
				proof["memory_read"] = true
				if expectedText != "" && strings.Contains(text, expectedText) && strings.Contains(text, "supporting_evidence") && strings.Contains(text, "attributed_user_statement") {
					proof["memory_evidence"] = true
				}
			}
			if expectedMatter != "" && strings.Contains(text, expectedMatter) {
				proof["matter_read"] = true
			}
		}
		if strings.Contains(name, "read_context_evidence") {
			if expectedText != "" && strings.Contains(text, expectedText) {
				proof["memory_evidence"] = true
			}
			if strings.Contains(text, "participant_report") && strings.Contains(text, "message_id") {
				proof["matter_evidence"] = true
			}
		}
		return nil
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("native context retrieval failed: status=%s error=%v", result.Status, err)
	}
	writeLiveAssistanceReport(t, "probe-"+run.ID+".json", map[string]any{"prompt": prompt, "result": result, "proof": proof, "tools": trace})
	for _, kind := range []string{"memory", "matter"} {
		if kind == "memory" && expectedMemory == "" || kind == "matter" && expectedMatter == "" {
			continue
		}
		for _, step := range []string{"search", "evidence"} {
			if !proof[kind+"_"+step] {
				t.Fatalf("native retrieval missing %s %s; proof=%v", kind, step, proof)
			}
		}
	}
	if expectedText != "" && !strings.Contains(result.Text, expectedText) {
		t.Fatal("native answer did not use the source-only readiness token")
	}
	if err := store.CompleteRun(ctx, run.ID, "completed", result.Text, result.Model, ""); err != nil {
		t.Fatal(err)
	}
	return result, proof
}

func nativeContextText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", errors.New("native tool result is not text content")
	}
	for _, block := range blocks {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text, nil
}

func liveAssistanceOptions(t *testing.T, home, project, path string) options {
	t.Helper()
	binary := os.Getenv("AGENT_HOST_LIVE_CLAUDE")
	if binary == "" {
		binary = "claude"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("AGENT_HOST_LIVE_MODEL")
	if model == "" {
		model = "sonnet"
	}
	o, pos, err := parse("chat", []string{"--home", home, "--project", project, "--discovery-config", path, "--claude", resolved, "--model", model}, io.Discard)
	if err != nil || len(pos) != 0 {
		t.Fatal(err)
	}
	return o
}

func seedLiveContextMemory(t *testing.T, store *state.Store, p state.Project, text, id string) {
	t.Helper()
	ctx := t.Context()
	refs := []state.ProjectRef{{ID: p.ID, Name: p.Name, Scope: p.Scope, Workspace: p.Workspace}}
	s := state.Session{ID: "seed-session-" + id, Scope: p.Scope, ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, Projects: refs, NativeSessionID: "seed-native-" + id, Model: "fixture"}
	if err := store.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	r := state.Run{ID: "seed-run-" + id, SessionID: s.ID, Scope: p.Scope, ProjectID: p.ID, ProjectName: p.Name, Workspace: p.Workspace, Projects: refs, NativeSessionID: s.NativeSessionID, Input: text}
	if err := store.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, r.ID, "completed", "Recorded declaration.", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeMemory(ctx, r.ID, []state.MemoryUpdate{{Memory: state.Memory{ID: id, Scope: r.Scope, Kind: "project_fact", Text: r.Input, SourceRunID: r.ID}}}, json.RawMessage(`{"fixture":"seeded_user_statement"}`)); err != nil {
		t.Fatal(err)
	}
}

func writeLiveAssistanceReport(t *testing.T, name string, value any) {
	t.Helper()
	root := os.Getenv("AGENT_HOST_LIVE_PRIVATE_ROOT")
	if root == "" {
		return
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAssistanceRetrievalAndPreparationLive(t *testing.T) {
	if os.Getenv("AGENT_HOST_LIVE_ASSISTANCE") != "1" {
		t.Skip("opt-in real-native memory and matter acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	started := time.Now()
	home, path, _, _ := assistanceCLIFixture(t)
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := store.Project(ctx, "registered-app")
	if err != nil {
		t.Fatal(err)
	}
	o := liveAssistanceOptions(t, home, p.ID, path)
	if err := resolveOptions(ctx, store, &o, true); err != nil {
		t.Fatal(err)
	}
	marker := "READY-" + host.ID()
	seedLiveContextMemory(t, store, p, "The deployment readiness endpoint is /readyz. Release may proceed only when it returns "+marker+".", "saved-readiness")
	seedLiveContextMemory(t, store, p, "The billing liveness endpoint is /billing-livez; it is unrelated to deployment readiness.", "billing-distractor")
	seedLiveContextMemory(t, store, p, "The old deployment readiness endpoint is /obsolete-readyz.", "disabled-readiness")
	if err := store.InvalidateMemory(ctx, "disabled-readiness", "superseded"); err != nil {
		t.Fatal(err)
	}
	cfg, err := discovery.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	source := cfg.Sources[0].Attribution(cfg)
	evidence, err := store.DiscoveryEvidence(ctx, source.ID, 200)
	if err != nil {
		t.Fatal(err)
	}
	// Force a genuine new complete collection with an interleaved commitment.
	e2 := evidence[0]
	e2.MessageID = "billing-commitment"
	e2.Text = "I will prepare a separate billing migration checklist. There is no deployment decision in this message."
	e2.CreatedAt = e2.CreatedAt.Add(time.Second)
	e2 = state.PrepareDiscoveryEvidence(e2)
	evidence = append(evidence, e2)
	current, _ := store.DiscoverySource(ctx, source.ID)
	c := *current.LastCollection
	c.Count = len(evidence)
	c.At = time.Now().UTC()
	if _, err = store.SaveDiscoveryCollection(ctx, source, c, evidence); err != nil {
		t.Fatal(err)
	}
	current, _ = store.DiscoverySource(ctx, source.ID)
	processor := &assistance.Processor{Store: store, Runner: claude.New(o.binary), Model: o.model}
	if err = processor.Reconcile(ctx, current, evidence); err != nil {
		t.Fatal("real native matter reconciliation", err)
	}
	matters, err := store.SearchMatters(ctx, p.ID, []string{source.ID}, "readiness", 1)
	if err != nil || len(matters) != 1 || matters[0].Matter.Draft.Kind != "question" {
		t.Fatal("real model confused interleaved matters", matters, err)
	}
	all, _ := store.SourceMatters(ctx, source.ID)
	if len(all) != 2 {
		t.Fatal("interleaved commitment lost or duplicated", len(all))
	}
	answer, proof := probeNativeContext(t, ctx, store, o, "What still needs an answer about deployment readiness, and which readiness endpoint and success token were previously agreed? Cite original sources and distinguish the collected question from saved project knowledge.", "saved-readiness", matters[0].Matter.ID, marker)
	second, secondProof := probeNativeContext(t, ctx, store, o, "部署前还有什么问题没答复？之前约定的就绪检查地址和成功标记是什么？请查阅原始依据，并区分当前事项与已保存的知识。", "saved-readiness", matters[0].Matter.ID, marker)
	writeLiveAssistanceReport(t, "synthetic-native-answers.json", map[string]any{"english": answer, "chinese": second})
	endpoint := os.Getenv("AGENT_HOST_LIVE_JEV_ENDPOINT")
	provider := "typesafe"
	if endpoint != "" {
		provider = "custom"
	}
	client, err := newJevClient(provider, endpoint, os.Getenv("AGENT_HOST_LIVE_JEV_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	h := composeHost(store, o, client, nil)
	// This acceptance runs inside go test; production composition runs inside
	// agent-host. Pin the real subprocess entry point rather than the test binary.
	h.Config.ContextExecutable = os.Getenv("AGENT_HOST_LIVE_HOST")
	session, err := h.Session(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.Suggestions(ctx, p.ID, []string{source.ID})
	if err != nil {
		t.Fatal(err)
	}
	var choice state.Suggestion
	for _, r := range list {
		if r.MatterID == matters[0].Matter.ID {
			choice = r
		}
	}
	if choice.ID == "" {
		t.Fatal("real native matter lacks preparation choice")
	}
	run, err := h.RunPreparation(ctx, session, choice.ID, choice.Revision)
	if err != nil || run.ExecutionStatus != "completed" || !strings.Contains(run.Output, marker) || !strings.Contains(run.Output, "/readyz") {
		t.Fatalf("real preparation did not use saved guidance: status=%s error=%v", run.ExecutionStatus, err)
	}
	if _, err = h.RunPreparation(ctx, session, choice.ID, choice.Revision); err == nil {
		t.Fatal("native preparation replayed")
	}
	writeLiveAssistanceReport(t, "synthetic-preparation.json", run)
	// New answer must reconcile into the same matter, preserving the other issue.
	e3 := evidence[0]
	e3.MessageID = "readiness-answer"
	e3.Text = "The deployment readiness question is answered: use /readyz and the recorded success token. No deployment test has been run."
	e3.CreatedAt = e3.CreatedAt.Add(2 * time.Second)
	e3 = state.PrepareDiscoveryEvidence(e3)
	evidence = append(evidence, e3)
	e4 := e2
	e4.MessageID = "billing-cancellation"
	e4.Text = "I withdraw my request to prepare the billing migration checklist. Do not redo or follow up on that task."
	e4.CreatedAt = e3.CreatedAt.Add(time.Second)
	e4 = state.PrepareDiscoveryEvidence(e4)
	evidence = append(evidence, e4)
	c.At = time.Now().UTC()
	c.Count = len(evidence)
	if _, err = store.SaveDiscoveryCollection(ctx, source, c, evidence); err != nil {
		t.Fatal(err)
	}
	current, _ = store.DiscoverySource(ctx, source.ID)
	if err = processor.Reconcile(ctx, current, evidence); err != nil {
		t.Fatal(err)
	}
	updated, err := store.MatterView(ctx, p.ID, []string{source.ID}, matters[0].Matter.ID)
	if err != nil || updated.Matter.Draft.Status != "reported_resolved" {
		t.Fatal("native update did not close answered question", updated, err)
	}
	for _, m := range all {
		if m.Draft.Kind != "commitment" {
			continue
		}
		cancelled, err := store.MatterView(ctx, p.ID, []string{source.ID}, m.ID)
		if err != nil || cancelled.Matter.Draft.Status != "closed" {
			t.Fatal("explicitly cancelled commitment remains active", cancelled, err)
		}
	}
	active, err := store.Suggestions(ctx, p.ID, []string{source.ID})
	if err != nil || len(active) != 0 {
		t.Fatal("answered/cancelled work still suggested", active, err)
	}
	before := updated.Matter.Revision
	if err = processor.Reconcile(ctx, current, evidence); err != nil {
		t.Fatal(err)
	}
	updated, _ = store.MatterView(ctx, p.ID, []string{source.ID}, matters[0].Matter.ID)
	if updated.Matter.Revision != before {
		t.Fatal("unchanged overlap repeated matter update")
	}
	writeLiveAssistanceReport(t, "native-report.json", map[string]any{"passed": true, "method": "real_native_synthetic_sources_no_prefetch", "fresh_conversations": 2, "queries": []string{"English", "Chinese"}, "english_proof": proof, "chinese_proof": secondProof, "source_only_token_recovered": true, "native_matter_progress": true, "requester_cancellation_closed_matter": true, "stale_suggestions_suppressed": true, "interleaved_matters": 2, "unchanged_window_deduplicated": true, "preparation_completed": true, "preparation_replay_blocked": true, "publication": "existing policy; seeded retrieval records do not measure memory admission", "elapsed_ms": time.Since(started).Milliseconds(), "messages_sent": 0})
}

func TestSelectedDWSAssistancePilotLive(t *testing.T) {
	path := os.Getenv("AGENT_HOST_ASSISTANCE_DWS_CONFIG")
	if path == "" {
		t.Skip("opt-in already selected-source DWS pilot")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	started := time.Now()
	cfg, err := discovery.LoadConfig(path)
	if err != nil || len(cfg.Sources) != 1 {
		t.Fatal("pilot requires exactly one selected source", err)
	}
	home := os.Getenv("AGENT_HOST_DWS_PILOT_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("AGENT_HOST_LIVE_PRIVATE_ROOT"), "dws-state")
	}
	if !filepath.IsAbs(home) {
		t.Fatal("pilot requires private acceptance root")
	}
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := store.Project(ctx, cfg.Sources[0].ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		p, err = store.RegisterProject(ctx, cfg.Sources[0].ProjectID, "Selected-source pilot", t.TempDir())
	} else if err == nil {
		p, err = store.UpdateProject(ctx, p.ID, p.Name, t.TempDir())
	}
	if err != nil {
		t.Fatal(err)
	}
	o := liveAssistanceOptions(t, home, p.ID, path)
	if err = resolveOptions(ctx, store, &o, true); err != nil {
		t.Fatal(err)
	}
	collector, err := composeDiscovery(ctx, store, cfg, o.binary)
	if err != nil {
		t.Fatal(err)
	}
	report, err := collector.Collect(ctx)
	if err != nil {
		writeLiveAssistanceReport(t, "dws-failed-report.json", report)
		t.Fatal("selected DWS intake or native analysis failed; see private report", err)
	}
	source := cfg.Sources[0].Attribution(cfg)
	all, err := store.SourceMatters(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.DiscoveryEvidence(ctx, source.ID, 200)
	if err != nil || len(evidence) == 0 {
		t.Fatal("selected DWS pilot has no collected evidence", err)
	}
	if len(all) == 0 {
		writeLiveAssistanceReport(t, "dws-report.json", map[string]any{"passed": true, "eligible_matters": 0, "abstained": true, "collected_messages": len(evidence), "source_kind": "real_selected_DWS", "messages_sent": 0})
		return
	}
	for _, m := range all {
		citations, err := store.MatterEvidence(ctx, p.ID, []string{source.ID}, m.ID)
		if err != nil || len(citations) == 0 {
			t.Fatal("real matter missing exact available sources", err)
		}
	}
	answer, proof := probeNativeContext(t, ctx, store, o, "What questions or commitments are present in the selected conversation, and what remains open in the collected window? Read the original sources, cite their authors and observation times, and distinguish participant reports from verified outcomes. Do not assume silence proves a blocker.", "", all[0].ID, "")
	writeLiveAssistanceReport(t, "dws-native-answer.json", answer)
	statuses := map[string]int{}
	for _, m := range all {
		statuses[m.Draft.Status]++
	}
	choices, err := store.Suggestions(ctx, p.ID, []string{source.ID})
	if err != nil {
		t.Fatal(err)
	}
	writeLiveAssistanceReport(t, "dws-report.json", map[string]any{"passed": true, "method": "real_selected_DWS_intake_native_fresh_retrieval", "collected_messages": len(evidence), "eligible_matters": len(all), "proof": proof, "matter_statuses": statuses, "active_suggestions": len(choices), "collection": report, "source_kind": "real_selected_DWS", "messages_sent": 0, "memory_published_by_discovery": false, "elapsed_ms": time.Since(started).Milliseconds(), "unmeasured": []string{"account-wide recall", "long-term recommendation usefulness", "upstream deletion event coverage"}})
}
