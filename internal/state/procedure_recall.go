package state

import (
	"context"
	"strings"
	"unicode/utf8"
)

// ProcedureRecallHint is a navigation reference from a recent task in the
// current authenticated conversation, never an additional memory grant.
type ProcedureRecallHint struct {
	RunID    string `json:"run_id"`
	MemoryID string `json:"memory_id"`
}

// RetrieveWithProcedureHints gives short requests one bounded fallback when
// their own words find no memory. Both preselection and dispatch use this path.
func (s *Store) RetrieveWithProcedureHints(ctx context.Context, scope, text string, hints []ProcedureRecallHint, limit int) (Retrieval, []ProcedureRecallHint, error) {
	result, err := s.Retrieve(ctx, scope, text, limit)
	if err != nil || len(result.Matches) > 0 || utf8.RuneCountInString(text) > 80 || len(hints) == 0 {
		return result, nil, err
	}
	query := text
	var used []ProcedureRecallHint
	seen := map[string]bool{}
	for _, hint := range hints {
		if len(used) == 3 {
			break
		}
		if seen[hint.MemoryID] {
			continue
		}
		seen[hint.MemoryID] = true
		m, err := s.Memory(ctx, hint.MemoryID)
		if err != nil {
			return result, nil, err
		}
		if m.Scope != scope || m.Status != "active" || m.Kind != "procedure" || m.SourceType != "reviewed_experience" {
			continue
		}
		// Use only the published title as a search cue; conditions and current
		// publication eligibility are still checked by ordinary retrieval.
		title, _, _ := strings.Cut(m.Text, "\n")
		query += "\n" + title
		used = append(used, hint)
	}
	if len(used) == 0 {
		return result, nil, nil
	}
	result, err = s.Retrieve(ctx, scope, query, limit)
	return result, used, err
}
