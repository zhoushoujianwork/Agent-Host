package host

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"agent-host/internal/annotation"
	"agent-host/internal/approval"
	"agent-host/internal/assembly"
	"agent-host/internal/assistance"
	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/history"
	"agent-host/internal/knowledge"
	"agent-host/internal/memory"
	"agent-host/internal/objectmemory"
	"agent-host/internal/state"
	"agent-host/internal/taskfeedback"
)

type Config struct {
	PreviousSessionID     string
	PrepareFeedback       func(context.Context, state.Run, *taskfeedback.Plan) (*taskfeedback.Plan, error)
	BindFeedback          func(context.Context, state.Run) error
	RestrictedReadOnly    bool
	ApprovalExecutable    string
	RequestApproval       func(context.Context, state.Run, approval.Proposal) error
	ApprovedOperationID   string
	ProcedureRecallHints  []state.ProcedureRecallHint
	ContextFiles          []assembly.ContextFile
	UserProfile           string
	WorkspaceCache        string
	MemorySource          string
	MemoryFingerprint     string
	ExpectedSkillDigest   string
	SkillsPreselected     bool
	ContextHome           string
	ContextExecutable     string
	ContextConfig         string
	AgentID               string
	AudienceID            string
	NativeProfile         string
	PermissionMode        string
	HostInstructions      string
	Projects              []state.ProjectRef
	AdditionalDirectories []string
	Supplements           []state.UserSupplement
	MCPServers            []harness.MCPServer
	KnowledgeHints        []knowledge.Hint
	ValidateKnowledge     func() error
	HistoryHome           string
	HistoryExecutable     string
	Scope                 string
	ProjectID             string
	ProjectName           string
	Workspace             string
	RuntimeFingerprint    string
	Model                 string
	DecisionModel         string
	DecisionProvider      string
	ToolsDisabled         bool
	AllowedTools          []string
	// RuntimeRecallQuery keeps a fork's selected Skills aligned with its source runtime.
	RuntimeRecallQuery string
}

type Host struct {
	Store     *state.Store
	Runner    harness.Runner
	Decider   memory.Decider
	Extractor experience.Extractor
	Judge     experience.Judge
	Objects   objectmemory.Extractor
	Config    Config
	Sink      event.Sink
}

func (h *Host) workspace() string {
	if h.Config.Workspace != "" {
		return h.Config.Workspace
	}
	return h.Config.Scope
}

func (h *Host) matchesRuntime(s state.Session) bool {
	if partitionAgent(s.AgentID) != partitionAgent(h.Config.AgentID) || partitionAudience(s.AudienceID) != partitionAudience(h.Config.AudienceID) {
		return false
	}
	if s.Scope != h.Config.Scope || s.WorkingDirectory() != h.workspace() || s.ProjectID != h.Config.ProjectID || s.RuntimeFingerprint != h.Config.RuntimeFingerprint {
		return false
	}
	identities := func(projects []state.ProjectRef) []state.ProjectRef {
		out := append([]state.ProjectRef(nil), projects...)
		for i := range out {
			out[i].Name = ""
		}
		slices.SortFunc(out, func(a, b state.ProjectRef) int { return strings.Compare(a.ID, b.ID) })
		return out
	}
	directories := func(paths []string) []string {
		out := append([]string(nil), paths...)
		slices.Sort(out)
		return out
	}
	return slices.Equal(identities(s.Projects), identities(h.Config.Projects)) && slices.Equal(directories(s.AdditionalDirectories), directories(h.Config.AdditionalDirectories))
}

// The neutral cwd is never an extra knowledge namespace for a joint task.
func recallProjects(run state.Run) []state.ProjectRef {
	projects := run.Projects
	if len(projects) == 0 {
		return []state.ProjectRef{{ID: run.ProjectID, Name: run.ProjectName, Workspace: run.WorkingDirectory(), Scope: run.Scope}}
	}
	out := make([]state.ProjectRef, 0, len(projects))
	seen := map[string]bool{}
	for _, project := range projects {
		if !seen[project.Scope] {
			out = append(out, project)
			seen[project.Scope] = true
		}
	}
	return out
}

func runtimePrompt(run state.Run) string {
	var parts []string
	if len(run.Projects) > 0 {
		raw, _ := json.Marshal(run.Projects)
		parts = append(parts, "Selected projects (identity and directory data):\n"+string(raw)+"\nMemory records retain their own scope; do not transfer facts between projects. The primary working directory is "+run.WorkingDirectory()+".")
	}
	if run.AutomaticMemorySkipReason() != "" {
		parts = append(parts, "Automatic durable memory and experience extraction are paused because this task's new knowledge is not attributed to one project. Do not promise to save or copy knowledge to any project.")
	}
	if len(run.Supplements) > 0 {
		raw, _ := json.Marshal(run.Supplements)
		parts = append(parts, "Later user clarifications, with their original message IDs (supplement the current request):\n"+string(raw))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n\n"
}

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (h *Host) Session(ctx context.Context, id string) (state.Session, error) {
	h = h.partitioned()
	if id != "" {
		s, err := h.Store.Session(ctx, id)
		if err != nil {
			return s, err
		}
		if s.Room != nil {
			return s, errors.New("room execution sessions are immutable; send a new message to the room")
		}
		if s.Model != h.Config.Model || !h.matchesRuntime(s) {
			return s, errors.New("session project/workspace/runtime differs from this configuration; start a new session")
		}
		if err := h.Store.SessionCanResume(ctx, id); err != nil {
			return s, err
		}
		return s, nil
	}
	s := h.newSession()
	return s, h.Store.CreateSession(ctx, s)
}

func (h *Host) newSession() state.Session {
	return state.Session{AgentID: partitionAgent(h.Config.AgentID), AudienceID: partitionAudience(h.Config.AudienceID), Projects: append([]state.ProjectRef(nil), h.Config.Projects...), AdditionalDirectories: append([]string(nil), h.Config.AdditionalDirectories...), ID: ID(), Scope: h.Config.Scope, ProjectID: h.Config.ProjectID, ProjectName: h.Config.ProjectName, Workspace: h.workspace(), RuntimeFingerprint: h.Config.RuntimeFingerprint, Model: h.Config.Model, NativeSessionID: ID(), CreatedAt: time.Now().UTC()}
}

type journal struct {
	h       *Host
	run     state.Run
	start   time.Time
	mu      sync.Mutex
	sinkErr error
}

func (j *journal) emit(e event.Event) error {
	return j.emitEvent(e, false)
}

// Progress is an optional observation. Its persistence/rendering failure must
// not poison later tool evidence or cancel the native task.
func (j *journal) emitProgress(e event.Event) error {
	return j.emitEvent(e, true)
}

func (j *journal) emitEvent(e event.Event, optional bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	e.Version = event.Version
	e.At = time.Now().UTC()
	e.SessionID = j.run.SessionID
	e.RunID = j.run.ID
	if e.Agent == "" {
		switch {
		case e.Phase == "execute":
			e.Agent = j.run.Agent
		case e.Phase == "extract":
			e.Agent = "memory-extractor"
		case e.Component == "memory.jev":
			e.Agent = "decision-jev"
		default:
			e.Agent = "host"
		}
	}
	e.ElapsedMS = time.Since(j.start).Milliseconds()
	// Evidence and terminal state survive cancellation of the execution context.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := j.h.Store.AppendEvent(ctx, &e); err != nil {
		return fmt.Errorf("persist event %s: %w", e.Type, err)
	}
	if j.sinkErr != nil {
		return j.sinkErr
	}
	if j.h.Sink != nil {
		err := j.h.Sink(e)
		if !optional {
			j.sinkErr = err
		}
		return err
	}
	return j.sinkErr
}

func (j *journal) phase(parent context.Context, name, component, model string, fn func(context.Context) error) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	return j.phaseWithTicks(parent, name, component, model, fn, ticker.C)
}

func (j *journal) phaseWithTicks(parent context.Context, name, component, model string, fn func(context.Context) error, ticks <-chan time.Time) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	start := time.Now()
	if err := j.emit(event.Event{Type: "phase.started", Phase: name, Component: component, Status: "running", Model: model, Message: "started"}); err != nil {
		return err
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var tickErr error
		for {
			select {
			case <-stop:
				done <- tickErr
				return
			case _, open := <-ticks:
				if !open {
					ticks = nil
					continue
				}
				if tickErr == nil {
					tickErr = j.emitProgress(event.Event{Type: "phase.progress", Phase: name, Component: component, Status: "running", Model: model, Message: fmt.Sprintf("working for %.1fs", time.Since(start).Seconds())})
				}
			}
		}
	}()
	err := fn(ctx)
	close(stop)
	progressErr := <-done
	status := "completed"
	if err != nil {
		status = "failed"
	}
	data := map[string]any{"duration_ms": time.Since(start).Milliseconds()}
	if progressErr != nil {
		// Preserve the gap at the next mandatory boundary; if that cannot be
		// saved, its error remains fatal rather than claiming a durable result.
		data["progress_error"] = progressErr.Error()
	}
	endErr := j.emit(event.Event{Type: "phase.finished", Phase: name, Component: component, Status: status, Model: model, Message: status, Data: data})
	return errors.Join(err, endErr)
}

func (h *Host) Run(ctx context.Context, s state.Session, input string) (state.Run, error) {
	return h.run(ctx, s, input, "", nil)
}

// RunChannel preserves the ordinary host loop while atomically linking its run
// to a claimed, authenticated channel message before any native execution.
func (h *Host) RunChannel(ctx context.Context, s state.Session, input, messageID string) (state.Run, error) {
	if messageID == "" {
		return state.Run{}, errors.New("channel message ID is required")
	}
	return h.run(ctx, s, input, messageID, nil)
}

func (h *Host) run(ctx context.Context, s state.Session, input, channelMessageID string, preparation *state.PreparationSource) (state.Run, error) {
	h = h.partitioned()
	var previousFeedback *taskfeedback.Plan
	if h.Config.PrepareFeedback != nil {
		previous, e := h.Store.LatestSessionRun(ctx, s.ID)
		if e == nil {
			previousFeedback = previous.Feedback
		} else if !errors.Is(e, sql.ErrNoRows) {
			return state.Run{}, e
		}
	}
	resume, err := h.Store.SessionHasRun(ctx, s.ID)
	if err != nil {
		return state.Run{}, err
	}
	if resume {
		if err = h.Store.SessionCanResume(ctx, s.ID); err != nil {
			return state.Run{}, err
		}
	}
	if !h.matchesRuntime(s) {
		return state.Run{}, errors.New("session does not match the assembled runtime")
	}
	execution := harness.RunnerIdentity(h.Runner)
	r := state.Run{AgentID: s.AgentID, AudienceID: s.AudienceID, Projects: append([]state.ProjectRef(nil), s.Projects...), AdditionalDirectories: append([]string(nil), s.AdditionalDirectories...), Supplements: append([]state.UserSupplement(nil), h.Config.Supplements...), ID: ID(), SessionID: s.ID, Scope: s.Scope, ProjectID: s.ProjectID, ProjectName: s.ProjectName, Workspace: s.Workspace, RuntimeFingerprint: s.RuntimeFingerprint, Input: input, Agent: execution.Agent, RequestedModel: h.Config.Model, NativeSessionID: s.NativeSessionID, ExecutionStatus: "running", MemoryStatus: "blocked", AcceptanceStatus: "unverified", CreatedAt: time.Now().UTC()}
	r.Preparation = preparation
	r.Room = s.Room
	if preparation != nil {
		var sources []string
		sources, err = assistance.SelectedSources(ctx, h.Store, h.Config.ContextConfig, s.ProjectID)
		if err == nil {
			err = h.Store.CreatePreparationRun(ctx, r, sources)
		}
	} else if channelMessageID == "" {
		err = h.Store.CreateRun(ctx, r)
	} else {
		err = h.Store.CreateChannelRun(ctx, r, channelMessageID)
	}
	if err != nil {
		return r, err
	}
	j := &journal{h: h, run: r, start: time.Now()}
	if channelMessageID != "" {
		stored, readErr := h.Store.Run(ctx, r.ID)
		if readErr != nil {
			return h.failBeforeExecution(j, readErr)
		}
		r, j.run = stored, stored
	}
	if h.Config.ApprovedOperationID != "" {
		if err := h.Store.BindOperationApprovalRun(ctx, h.Config.ApprovedOperationID, channelMessageID, r.ID); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	if h.Config.PrepareFeedback != nil {
		plan, prepareErr := h.Config.PrepareFeedback(ctx, r, previousFeedback)
		if prepareErr != nil {
			return h.failBeforeExecution(j, prepareErr)
		}
		if err = h.Store.BindRunFeedback(ctx, r.ID, plan); err != nil {
			return h.failBeforeExecution(j, err)
		}
		r.Feedback, j.run.Feedback = plan, plan
		if h.Config.BindFeedback != nil {
			if err = h.Config.BindFeedback(ctx, r); err != nil {
				return h.failBeforeExecution(j, err)
			}
		}
		if err = j.emit(event.Event{Type: "feedback.bound", Phase: "request", Component: "host.feedback", Status: plan.Mode, Data: map[string]any{"feedback": plan}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	if err := j.emit(event.Event{Type: "run.started", Phase: "request", Component: "host", Status: "running", Message: fmt.Sprintf("run=%s session=%s", r.ID, r.SessionID), Data: map[string]any{"agent_id": r.AgentID, "audience_id": r.AudienceID, "workspace": r.WorkingDirectory(), "scope": r.Scope, "project_id": r.ProjectID, "project_name": r.ProjectName, "runtime_fingerprint": r.RuntimeFingerprint, "projects": r.Projects, "additional_directories": r.AdditionalDirectories, "supplements": r.Supplements, "input_bytes": len(input)}}); err != nil {
		return h.failBeforeExecution(j, err)
	}
	var selected []state.Memory
	if h.Config.PreviousSessionID != "" {
		if err := j.emit(event.Event{Type: "conversation.runtime_renewed", Phase: "execute", Component: "host", Status: "completed", Message: "selected capabilities changed; started a fresh native conversation before dispatch", Data: map[string]any{"previous_session_id": h.Config.PreviousSessionID, "native_history_restored": false}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	var notices []string
	err = j.phase(ctx, "recall", "memory.retrieval", "", func(ctx context.Context) error {
		var matches, persistent []state.Memory
		query := input
		for _, supplement := range r.Supplements {
			query += "\n" + supplement.Text
		}
		if h.Config.RuntimeRecallQuery != "" {
			query = h.Config.RuntimeRecallQuery
		}
		for _, project := range recallProjects(r) {
			if !h.Store.FileMemoryEnabled() {
				all, err := h.Store.Memories(ctx, project.Scope)
				if err != nil {
					return err
				}
				if len(all) > memory.MaxExisting {
					return fmt.Errorf("scope %s has %d memories; exceeds MVP comparison limit %d", project.Scope, len(all), memory.MaxExisting)
				}
			}
			retrieval, hints, err := h.Store.RetrieveWithProcedureHints(ctx, project.Scope, query, h.Config.ProcedureRecallHints, 16)
			if err != nil {
				return err
			}
			if len(hints) > 0 {
				if err := j.emit(event.Event{Type: "memory.procedure_hints.applied", Phase: "recall", Component: "memory.retrieval", Status: "completed", Message: "used recent same-conversation procedure titles for a short request with no direct matches", Data: map[string]any{"scope": project.Scope, "hints": hints, "matches": len(retrieval.Matches)}}); err != nil {
					return err
				}
			}
			if err := j.emit(event.Event{Type: "memory.query.analyzed", Phase: "recall", Component: "memory.annotation", Status: "completed", Message: fmt.Sprintf("scope=%s resolved objects=%d ambiguous=%v unknown=%v", project.Scope, len(retrieval.Query.ResolvedIDs), retrieval.Query.Ambiguous, retrieval.Query.Unknown), Data: map[string]any{"scope": project.Scope, "project_id": project.ID, "query": retrieval.Query, "annotation_version": annotation.Version}}); err != nil {
				return err
			}
			excluded := 0
			for _, decision := range retrieval.Decisions {
				if !decision.Accepted {
					excluded++
				}
			}
			if err := j.emit(event.Event{Type: "memory.retrieval.evaluated", Phase: "recall", Component: "memory.retrieval", Status: "completed", Message: fmt.Sprintf("scope=%s candidates=%d; excluded=%d; search matches=%d; persistent=%d", project.Scope, len(retrieval.Decisions), excluded, len(retrieval.Matches), len(retrieval.Persistent)), Data: map[string]any{"scope": project.Scope, "project_id": project.ID, "decisions": retrieval.Decisions, "lexical_ids": retrieval.LexicalIDs}}); err != nil {
				return err
			}
			matches = append(matches, retrieval.Matches...)
			persistent = append(persistent, retrieval.Persistent...)
		}
		selected = assembly.Select(matches, persistent, 12*1024)
		previous, err := h.Store.SessionMemories(ctx, s.ID)
		if err != nil {
			return err
		}
		for _, old := range previous {
			current, err := h.Store.Memory(ctx, old.ID)
			if err != nil {
				return err
			}
			if current.Revision != old.Revision || current.Status != "active" || (current.ExpiresAt != nil && !current.ExpiresAt.After(time.Now())) {
				notices = append(notices, assembly.RevisionNotice(old, current))
			}
		}
		if err := h.Store.SaveContext(ctx, r.ID, selected); err != nil {
			return err
		}
		return j.emit(event.Event{Type: "memory.recalled", Phase: "recall", Component: h.memoryComponent(), Status: "completed", Message: fmt.Sprintf("selected %d memories; %d revision notices", len(selected), len(notices)), Data: map[string]any{"memories": selected, "revision_notices": notices, "selection": "scoped preferences/constraints + object/topic/FTS candidates with applicability checks; max 8 records, 12KiB text"}})
	})
	if err != nil {
		return h.failBeforeExecution(j, err)
	}
	material, images, inputDirectories, err := channelInput(r, h.Config.ToolsDisabled)
	if err != nil {
		return h.failBeforeExecution(j, err)
	}
	if r.Preparation != nil {
		context, err := h.preparationContext(ctx, r)
		if err != nil {
			return h.failBeforeExecution(j, err)
		}
		material = context + material
	}
	recoveryContext, recovery, err := h.channelRecoveryContext(ctx, r)
	if err != nil {
		return h.failBeforeExecution(j, fmt.Errorf("assemble linked conversation recovery: %w", err))
	}
	if recovery != nil {
		if err := j.emit(event.Event{Type: "conversation.recovery.assembled", Phase: "execute", Component: "assembly.conversation", Status: "completed", Message: "supplied the linked previous task for conversation recovery", Data: map[string]any{"previous_session_id": recovery.SessionID, "previous_run_id": recovery.RunID, "previous_message_id": recovery.MessageID, "input_truncated": recovery.InputTruncated, "reply_truncated": recovery.ReplyTruncated, "quote_context_included": true}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	var recentRequestsContext string
	if recovery == nil && s.Room == nil {
		var requests []state.ChannelRequestContext
		recentRequestsContext, requests, err = h.channelRecentRequestsContext(ctx, r, resume)
		if err != nil {
			return h.failBeforeExecution(j, fmt.Errorf("assemble recent conversation requests: %w", err))
		}
		if len(requests) > 0 {
			ids := make([]string, 0, len(requests))
			for _, request := range requests {
				ids = append(ids, request.MessageID)
			}
			if err := j.emit(event.Event{Type: "conversation.requests.assembled", Phase: "execute", Component: "assembly.conversation", Status: "completed", Message: "supplied recent user requests for a fresh native conversation", Data: map[string]any{"message_ids": ids, "request_count": len(requests)}}); err != nil {
				return h.failBeforeExecution(j, err)
			}
		}
	}
	var result harness.Result
	mcpServers := append([]harness.MCPServer(nil), h.Config.MCPServers...)
	instructions := h.Config.HostInstructions
	if h.Config.ContextHome != "" && !h.Config.ToolsDisabled && r.AudienceID == state.OwnerAudienceID && r.ProjectID != "" && len(r.Projects) == 1 && (r.Source == nil || r.Source.ConversationType != "2") {
		binding, err := assistance.Resolve(ctx, h.Config.ContextHome, h.Config.ContextConfig, r.ID)
		if err != nil {
			return h.failBeforeExecution(j, fmt.Errorf("assemble owner context: %w", err))
		}
		mcpServers = append(mcpServers, binding.Server(h.Config.ContextExecutable))
		instructions += "\n\n" + assistance.Instructions
		if err := j.emit(event.Event{Type: "context.assembled", Phase: "execute", Component: "assembly.context", Status: "completed", Message: "configured scoped read-only matters, memory and citations", Data: map[string]any{"binding_fingerprint": binding.Fingerprint, "selected_sources": len(binding.Sources)}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	if channelMessageID != "" && h.Config.HistoryHome != "" && !h.Config.ToolsDisabled && !h.Config.RestrictedReadOnly {
		binding, err := history.Resolve(h.Config.HistoryHome, channelMessageID)
		if err != nil {
			return h.failBeforeExecution(j, fmt.Errorf("assemble conversation history: %w", err))
		}
		mcpServers = append(mcpServers, binding.Server(h.Config.HistoryExecutable))
		instructions += "\n\n" + history.Instructions
		if err := j.emit(event.Event{Type: "history.assembled", Phase: "execute", Component: "assembly.history", Status: "completed", Message: "configured read-only Agent conversation history; no transcript prefetched", Data: map[string]any{"binding_fingerprint": binding.Fingerprint}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	if h.Config.ValidateKnowledge != nil {
		if err := h.Config.ValidateKnowledge(); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	if len(h.Config.KnowledgeHints) > 0 {
		if err := j.emit(event.Event{Type: "knowledge.assembled", Phase: "execute", Component: "assembly.knowledge", Status: "completed", Message: "configured on-demand knowledge tools; no documents prefetched", Data: map[string]any{"sources": h.Config.KnowledgeHints}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	var skillPackage assembly.SkillPackage
	promptMemories := selected
	if h.Config.WorkspaceCache != "" {
		view, err := assembly.MaterializeWorkspace(h.Config.WorkspaceCache, assembly.WorkspaceInput{Run: r, Files: h.Config.ContextFiles, Memories: selected, UserProfile: h.Config.UserProfile})
		if err != nil {
			return h.failBeforeExecution(j, err)
		}
		if h.Store.FileMemoryEnabled() && !h.Config.ToolsDisabled && !h.Config.RestrictedReadOnly {
			identity := assembly.SkillIdentity{SourceIdentity: h.Config.MemorySource, AgentID: r.AgentID, AudienceID: r.AudienceID}
			for _, p := range recallProjects(r) {
				identity.Scopes = append(identity.Scopes, p.Scope)
			}
			skillPackage, err = assembly.BuildSkillPackage(h.Config.WorkspaceCache, r.ID, selected, identity, time.Now())
			if err != nil {
				return h.failBeforeExecution(j, err)
			}
			mounted := map[string]bool{}
			for _, source := range skillPackage.Manifest.Sources {
				mounted[source.MemoryID] = true
			}
			promptMemories = nil
			for _, m := range selected {
				if !mounted[m.ID] {
					promptMemories = append(promptMemories, m)
				}
			}
		}
		if err := assembly.VerifyWorkspace(view); err != nil {
			return h.failBeforeExecution(j, err)
		}
		if h.Config.SkillsPreselected && h.Config.ExpectedSkillDigest != skillPackage.Digest {
			return h.failBeforeExecution(j, errors.New("selected skill set changed before native execution"))
		}
		if err := j.emit(event.Event{Type: "workspace.assembled", Phase: "execute", Component: "assembly.workspace", Status: "completed", Message: "materialized scoped context without changing execution cwd", Data: map[string]any{"view": view, "skill_manifest": skillPackage.Manifest}}); err != nil {
			return h.failBeforeExecution(j, err)
		}
	}
	if h.Store.FileMemoryEnabled() && h.Config.MemoryFingerprint != "" {
		var scopes []string
		for _, p := range recallProjects(r) {
			scopes = append(scopes, p.Scope)
		}
		fingerprint, err := h.Store.MemoryFingerprint(ctx, scopes)
		if err != nil {
			return h.failBeforeExecution(j, err)
		}
		if fingerprint != h.Config.MemoryFingerprint {
			return h.failBeforeExecution(j, errors.New("published memory changed before execution; a new task context is required"))
		}
	}
	var proposalDirectory string
	if h.Config.RestrictedReadOnly {
		mcpServers = nil
		if h.Config.RequestApproval != nil && h.Config.ApprovalExecutable != "" {
			proposalDirectory, err = os.MkdirTemp("", "agh-owner-approval-")
			if err != nil {
				return h.failBeforeExecution(j, err)
			}
			defer os.RemoveAll(proposalDirectory)
			mcpServers = []harness.MCPServer{approval.Server(h.Config.ApprovalExecutable, proposalDirectory)}
		}
	}
	err = j.phase(ctx, "execute", execution.Component, h.Config.Model, func(ctx context.Context) error {
		if err := j.emit(event.Event{Type: "agent.dispatched", Phase: "execute", Component: "host", Status: "running", Model: h.Config.Model, Message: "dispatch " + execution.Agent + " through " + execution.Transport, Data: map[string]any{"execution": execution, "native_session_id": s.NativeSessionID, "resume": resume, "requested_model": h.Config.Model, "tools_disabled": h.Config.ToolsDisabled, "allowed_tools": h.Config.AllowedTools, "native_profile": h.Config.NativeProfile, "permission_mode": h.Config.PermissionMode, "restricted_read_only": h.Config.RestrictedReadOnly}}); err != nil {
			return err
		}
		var runErr error
		activity := newRuntimePhases(j.emit)
		activity.identity = execution
		directories := append(append([]string(nil), s.AdditionalDirectories...), inputDirectories...)
		if h.Config.RestrictedReadOnly {
			directories = nil
		}
		result, runErr = h.Runner.Run(ctx, harness.Request{RestrictedReadOnly: h.Config.RestrictedReadOnly, SkillPluginDir: skillPackage.Path, SkillPluginDigest: skillPackage.Digest, NativeProfile: h.Config.NativeProfile, PermissionMode: h.Config.PermissionMode, HostInstructions: invocationInstructions(instructions, r), Images: images, ForkFromNativeSessionID: roomFork(s.Room), Prompt: roomPrompt(s.Room) + assembly.UserProfileContext(h.Config.UserProfile) + assembly.KnowledgeContext(h.Config.KnowledgeHints) + runtimePrompt(r) + recoveryContext + recentRequestsContext + material + assembly.NativeSkillContext(skillPackage.Manifest, selected) + assembly.Prompt(input, promptMemories, notices), Workspace: s.WorkingDirectory(), AdditionalDirectories: directories, Model: h.Config.Model, NativeSessionID: s.NativeSessionID, Resume: resume, ToolsDisabled: h.Config.ToolsDisabled, AllowedTools: h.Config.AllowedTools, MCPServers: mcpServers}, func(o harness.Observation) error {
			e := event.Event{Type: "native.evidence", Phase: "execute", Component: execution.Component, Status: "running", Model: o.Model, Message: o.Message, Data: map[string]any{"native_type": o.Type, "native_session_id": o.NativeSessionID, "raw": o.Raw}}
			if o.Tool != nil {
				e.Data["tool"] = o.Tool
			}
			switch o.Type {
			case "text_delta":
				e.Type = "text.delta"
				e.Delta = o.Delta
			case "init":
				e.Type = "agent.started"
			case "tool_start", "tool_update", "tool_result":
				e.Type = "tool.activity"
			case "result":
				e.Type = "agent.result"
			}
			return activity.observe(e, o.Tool)
		})
		return errors.Join(runErr, activity.finish(result.Status))
	})
	if result.Status == "" {
		result.Status = "unknown"
	}
	if proposalDirectory != "" && result.Status == "completed" && err == nil {
		proposal, proposalErr := approval.Read(proposalDirectory)
		if proposalErr == nil {
			proposalErr = h.Config.RequestApproval(ctx, r, proposal)
			if proposalErr == nil {
				result.Text += "\n\nThe configured owner was mentioned for this specific approval request. No blocked action has executed."
			}
		}
		if proposalErr != nil && !errors.Is(proposalErr, os.ErrNotExist) {
			result.Text += "\n\nOwner approval could not be requested: " + proposalErr.Error() + ". No blocked action has executed."
		}
	}
	errText := result.Error
	if err != nil && result.Status != "completed" {
		errText = err.Error()
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if saveErr := h.Store.CompleteRun(finishCtx, r.ID, result.Status, result.Text, result.Model, errText); saveErr != nil {
		return r, errors.Join(err, saveErr)
	}
	if err != nil || result.Status != "completed" {
		if result.Status == "completed" {
			if reason := r.AutomaticMemorySkipReason(); reason != "" {
				err = errors.Join(err, h.Store.SkipAutomaticMemory(finishCtx, r.ID, reason))
			} else {
				err = errors.Join(err, h.Store.MemoryFailure(finishCtx, r.ID, err.Error()))
			}
		}
		if err == nil {
			err = fmt.Errorf("native execution %s: %s", result.Status, result.Error)
		}
		return h.receipt(j, err)
	}
	if reason := r.AutomaticMemorySkipReason(); reason != "" {
		err = h.Store.SkipAutomaticMemory(finishCtx, r.ID, reason)
		if err == nil {
			err = j.emit(event.Event{Type: "memory.skipped", Phase: "remember", Component: "host", Status: "skipped", Message: "automatic memory and experience are paused for this run", Data: map[string]any{"reason": reason, "projects": r.Projects, "memory_attribution": r.MemoryAttribution}})
		}
		return h.receipt(j, err)
	}
	err = h.finalize(ctx, j)
	if err == nil {
		err = h.finalizeExperience(ctx, j)
	}
	return h.receipt(j, err)
}

func (h *Host) failBeforeExecution(j *journal, cause error) (state.Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Store.CompleteRun(ctx, j.run.ID, "failed", "", "", cause.Error()); err != nil {
		return j.run, errors.Join(cause, err)
	}
	return h.receipt(j, cause)
}

func (h *Host) finalize(ctx context.Context, j *journal) error {
	if j.run.AutomaticMemorySkipReason() != "" {
		return errors.New("automatic memory is paused until project attribution is established")
	}
	var updates []state.MemoryUpdate
	var raw json.RawMessage
	var generation string
	err := j.phase(ctx, "remember", "memory.jev", h.Config.DecisionModel, func(ctx context.Context) error {
		r, err := h.Store.Run(ctx, j.run.ID)
		if err != nil {
			return err
		}
		if h.Store.FileMemoryEnabled() {
			generation, err = h.Store.MemoryFingerprint(ctx, []string{r.Scope})
			if err != nil {
				return err
			}
		}
		existing, disabled, err := h.comparisonMemories(ctx, r.Scope, r.Input)
		if err != nil {
			return err
		}
		if err := j.emit(event.Event{Type: "decision.requested", Phase: "remember", Component: "memory.jev", Status: "running", Model: h.Config.DecisionModel, Message: "classify source statement and check conflicts", Data: map[string]any{"provider": h.Config.DecisionProvider, "source_run_id": r.ID, "source_role": "user"}}); err != nil {
			return err
		}
		decisionCtx := ctx
		if r.Source != nil {
			decisionCtx = memory.WithActor(ctx, r.Source.SenderID)
		}
		d, err := h.Decider.Decide(decisionCtx, r.Input, existing)
		if err != nil {
			emitErr := j.emit(event.Event{Type: "decision.failed", Phase: "remember", Component: "memory.jev", Status: "failed", Model: d.Model, Message: err.Error(), Data: map[string]any{"decision": d}})
			return errors.Join(err, emitErr)
		}
		raw, err = json.Marshal(d)
		if err != nil {
			return err
		}
		if err := j.emit(event.Event{Type: "decision.completed", Phase: "remember", Component: "memory.jev", Status: "completed", Model: d.Model, Message: fmt.Sprintf("kind=%s store=%t conflicts=%d", d.Kind, d.Store, len(d.ConflictIDs)), Data: map[string]any{"decision": d}}); err != nil {
			return err
		}
		for _, id := range append(append([]string(nil), d.ConflictIDs...), d.DuplicateIDs...) {
			if disabled[id] {
				raw, err = json.Marshal(map[string]any{"decision": d, "reason": "disabled_or_unresolved_memory_relation", "excluded_memory_id": id})
				return err
			}
		}
		if d.Store || len(d.DuplicateIDs) > 0 {
			objectInput := nativeObjectInput(r, r.Input, d.Kind, nil)
			objects, err := h.identifyObjects(ctx, objectInput, nil)
			if emitErr := j.emit(event.Event{Type: "memory.objects.extracted", Phase: "remember", Component: "memory.objects", Data: map[string]any{"extraction": objects, "error": errorText(err)}}); emitErr != nil {
				return errors.Join(err, emitErr)
			}
			if err != nil {
				return err
			}
			updates = []state.MemoryUpdate{{Memory: state.Memory{ID: ID(), Scope: r.Scope, Kind: d.Kind, Text: r.Input, Status: "active", Revision: 1, SourceRunID: r.ID, CreatedAt: time.Now().UTC()}, ConflictIDs: d.ConflictIDs}}
			updates[0].Memory.Objects, updates[0].Memory.ObjectAudit = objects.Objects, objects.Audit
			if len(d.ConflictIDs) == 0 {
				updates[0].DuplicateIDs = d.DuplicateIDs
			}
		}
		return nil
	})
	if err == nil {
		err = j.phase(ctx, "persist", h.memoryComponent(), "", func(ctx context.Context) error {
			return h.Store.FinalizeMemory(ctx, j.run.ID, updates, raw, generation)
		})
	}
	if err != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		current, readErr := h.Store.Run(persistCtx, j.run.ID)
		if readErr != nil {
			return errors.Join(err, readErr)
		}
		if current.MemoryStatus == "saved" || current.MemoryStatus == "unchanged" {
			return err
		}
		return errors.Join(err, h.Store.MemoryFailure(persistCtx, j.run.ID, err.Error()))
	}
	return nil
}

func (h *Host) receipt(j *journal, cause error) (state.Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := h.Store.Run(ctx, j.run.ID)
	if err != nil {
		return j.run, errors.Join(cause, err)
	}
	status := "completed"
	if cause != nil {
		status = "failed"
	}
	disposition := r.MemoryStatus
	if r.MemoryStatus == "saved" {
		var d memory.Decision
		if err := json.Unmarshal(r.Decision, &d); err != nil {
			return r, errors.Join(cause, fmt.Errorf("read saved decision: %w", err))
		}
		disposition = "active_user_statement"
		if len(d.ConflictIDs) > 0 {
			disposition = "quarantined_excluded_from_recall"
		}
	}
	var progressErrors []map[string]any
	for _, e := range r.Events {
		if message, ok := e.Data["progress_error"].(string); e.Type == "phase.finished" && ok && message != "" {
			progressErrors = append(progressErrors, map[string]any{"phase": e.Phase, "source_event_seq": e.Seq, "error": message})
		}
	}
	emitErr := j.emit(event.Event{Type: "run.finished", Phase: "receipt", Component: "host", Status: status, Model: r.Model, Message: fmt.Sprintf("run=%s execution=%s acceptance=%s memory=%s disposition=%s experience=%s", r.ID, r.ExecutionStatus, r.AcceptanceStatus, r.MemoryStatus, disposition, r.ExperienceStatus), Data: map[string]any{"execution_status": r.ExecutionStatus, "acceptance_status": r.AcceptanceStatus, "memory_status": r.MemoryStatus, "memory_disposition": disposition, "experience_status": r.ExperienceStatus, "experience_error": r.ExperienceError, "error": r.Error, "memory_error": r.MemoryError, "progress_errors": progressErrors}})
	return r, errors.Join(cause, emitErr)
}

func (h *Host) RetryMemory(ctx context.Context, id string) (state.Run, error) {
	h = h.partitioned()
	r, err := h.Store.Run(ctx, id)
	if err != nil {
		return r, err
	}
	if r.AutomaticMemorySkipReason() != "" {
		return r, errors.New("automatic memory retry is paused until project attribution is established")
	}
	if r.Scope != h.Config.Scope {
		return r, errors.New("run belongs to another workspace")
	}
	if r.ExecutionStatus != "completed" || (r.MemoryStatus != "pending" && r.MemoryStatus != "failed") {
		return r, fmt.Errorf("run is not eligible for memory retry: execution=%s memory=%s", r.ExecutionStatus, r.MemoryStatus)
	}
	j := &journal{h: h, run: r, start: time.Now()}
	err = h.finalize(ctx, j)
	if err == nil && (r.ExperienceStatus == "pending" || r.ExperienceStatus == "failed") {
		err = h.finalizeExperience(ctx, j)
	}
	return h.receipt(j, err)
}
