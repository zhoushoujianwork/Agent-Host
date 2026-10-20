package host

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness"
)

type runtimeActivity struct {
	agent                   string
	phase, source, tool, id string
	started                 time.Time
}

// runtimePhases derives child phases from observed tool calls/results only.
// It never parses assistant text or performs tools itself.
type runtimePhases struct {
	identity harness.Identity
	mu       sync.Mutex
	emit     event.Sink
	active   map[string]runtimeActivity
	order    []string
	resume   bool
}

func newRuntimePhases(emit event.Sink) *runtimePhases {
	return &runtimePhases{emit: emit, active: map[string]runtimeActivity{}, identity: harness.RunnerIdentity(nil)}
}

func knowledgeActivity(name string) (phase, source string) {
	rest, ok := strings.CutPrefix(name, "mcp__knowledge_")
	if !ok {
		return "", ""
	}
	source, tool, ok := strings.Cut(rest, "__")
	if !ok || source == "" {
		return "", ""
	}
	switch tool {
	case "list_dokki_workspaces", "search_dokki", "search_confluence":
		return "knowledge_search", source
	case "read_dokki_resource", "read_confluence_page":
		return "knowledge_read", source
	}
	return "", ""
}

func (p *runtimePhases) observe(e event.Event, tool *harness.ToolObservation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Persist the originating observation before its derived phase.
	if err := p.emit(e); err != nil {
		return err
	}
	if tool == nil {
		if p.resume && e.Type == "text.delta" {
			p.resume = false
			return p.emit(event.Event{Type: "phase.resumed", Phase: "execute", Component: p.identity.Component, Agent: p.identity.Agent, Status: "running", Message: "native agent continued after knowledge lookup"})
		}
		return nil
	}
	if tool.Stage == "call" {
		phase, source := knowledgeActivity(tool.Name)
		if phase == "" {
			if p.resume {
				p.resume = false
				return p.emit(event.Event{Type: "phase.resumed", Phase: "execute", Component: p.identity.Component, Agent: p.identity.Agent, Status: "running", Message: "native agent continued with another tool"})
			}
			return nil
		}
		if _, exists := p.active[tool.ID]; exists {
			return errors.New("duplicate runtime activity call ID")
		}
		a := runtimeActivity{agent: p.identity.Agent, phase: phase, source: source, tool: tool.Name, id: tool.ID, started: time.Now()}
		p.active[tool.ID] = a
		p.order = append(p.order, tool.ID)
		p.resume = false
		return p.emit(a.event("phase.started", "running"))
	}
	if tool.Stage != "result" {
		return nil
	}
	a, exists := p.active[tool.ID]
	if !exists {
		return nil
	}
	status := "completed"
	if tool.IsError {
		status = "failed"
	}
	if err := p.emit(a.event("phase.finished", status)); err != nil {
		return err
	}
	delete(p.active, tool.ID)
	for i := len(p.order) - 1; i >= 0; i-- {
		if pending, ok := p.active[p.order[i]]; ok {
			return p.emit(pending.event("phase.resumed", "running"))
		}
	}
	p.resume = true
	return nil
}

func (p *runtimePhases) finish(status string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if status != "failed" && status != "canceled" {
		status = "unknown"
	}
	var errs []error
	for _, id := range p.order {
		if a, ok := p.active[id]; ok {
			errs = append(errs, p.emit(a.event("phase.finished", status)))
			delete(p.active, id)
		}
	}
	return errors.Join(errs...)
}

func (a runtimeActivity) event(kind, status string) event.Event {
	data := map[string]any{"parent_phase": "execute", "tool_call_id": a.id, "tool_name": a.tool, "source": a.source}
	if definition, ok := event.Phase(a.phase); ok {
		data["phase_label"] = definition.Label
	}
	if kind == "phase.finished" {
		data["duration_ms"] = time.Since(a.started).Milliseconds()
	}
	return event.Event{Type: kind, Phase: a.phase, Component: "knowledge.mcp", Agent: a.agent, Status: status, Message: fmt.Sprintf("source=%s tool=%s %s", a.source, a.tool, status), Data: data}
}
