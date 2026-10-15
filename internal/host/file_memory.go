package host

import (
	"context"
	"errors"
	"time"

	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func (h *Host) memoryComponent() string {
	if h.Store.FileMemoryEnabled() {
		return "memory.files"
	}
	return "memory.sqlite"
}

// Comparison candidates are bounded independently of the retained corpus.
// Disabled records are compared as data, never made eligible for recall.
func (h *Host) comparisonMemories(ctx context.Context, scope, query string) ([]state.Memory, map[string]bool, error) {
	if !h.Store.FileMemoryEnabled() {
		items, err := h.Store.Memories(ctx, scope)
		return items, nil, err
	}
	disabledItems, err := h.Store.DisabledMemories(ctx, scope, memory.MaxExisting)
	if err != nil {
		return nil, nil, err
	}
	disabled := map[string]bool{}
	items := make([]state.Memory, 0, memory.MaxExisting)
	bytes := len(query)
	for _, m := range disabledItems {
		if m.SourceType == state.SourceNoteSource {
			continue
		}
		disabled[m.ID] = true
		// Conflicted comparison status makes equivalent content visible across
		// statement/procedure source types, without admitting it for recall.
		m.Status, m.ExpiresAt = "conflicted", nil
		items = append(items, m)
		bytes += comparisonBytes(m)
	}
	if bytes > memory.MaxDecisionStateBytes {
		return nil, nil, errors.New("disabled comparison state exceeds budget; publication excluded")
	}
	active, err := h.Store.DecisionMemories(ctx, scope, query, memory.MaxExisting)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range active {
		if m.SourceType == state.SourceNoteSource {
			continue
		}
		if len(items) >= memory.MaxExisting {
			break
		}
		if bytes+comparisonBytes(m) > memory.MaxDecisionStateBytes {
			continue
		}
		if m.ExpiresAt != nil && !m.ExpiresAt.After(time.Now()) {
			continue
		}
		items = append(items, m)
		bytes += comparisonBytes(m)
	}
	return items, disabled, nil
}

func comparisonBytes(m state.Memory) int {
	return len(m.Text) + len(m.ID) + len(m.Kind) + len(m.Scope) + len(m.SenderID) + len(m.AgentID) + len(m.AudienceID) + 128
}
