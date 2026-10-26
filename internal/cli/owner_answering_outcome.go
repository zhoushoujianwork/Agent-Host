package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-host/internal/mcpstdio"
	"agent-host/internal/owneranswering"
	"agent-host/internal/state"
)

const answeringOutcomeStart = "<agh-owner-answering-outcome>"
const answeringOutcomeEnd = "</agh-owner-answering-outcome>"

// Only the one terminal marked block is machine-readable. Earlier narrated ACP
// progress remains intact in Run.Output and cannot be mistaken for final JSON.
func parseAnsweringOutcome(output string) (owneranswering.Outcome, error) {
	var o owneranswering.Outcome
	if strings.Count(output, answeringOutcomeStart) != 1 || strings.Count(output, answeringOutcomeEnd) != 1 {
		return o, errors.New("require one terminal answering outcome block")
	}
	a := strings.Index(output, answeringOutcomeStart)
	b := strings.Index(output, answeringOutcomeEnd)
	if a > 0 && output[a-1] != '\n' || b < a+len(answeringOutcomeStart) || strings.TrimSpace(output[b+len(answeringOutcomeEnd):]) != "" {
		return o, errors.New("answering outcome block must be anchored at the end")
	}
	body := output[a+len(answeringOutcomeStart) : b]
	if !strings.HasPrefix(body, "\n") || !strings.HasSuffix(body, "\n") {
		return o, errors.New("outcome markers must occupy their own lines")
	}
	if len(body) > 20000 {
		return o, errors.New("answering outcome exceeds output bound")
	}
	var fields map[string]json.RawMessage
	if err := mcpstdio.Decode([]byte(body), &fields); err != nil {
		return o, err
	}
	for _, key := range []string{"outcome", "text", "reason", "clarification"} {
		if _, ok := fields[key]; !ok {
			return o, fmt.Errorf("missing outcome field %s", key)
		}
	}
	if err := mcpstdio.Decode([]byte(body), &o); err != nil {
		return o, err
	}
	if o.Outcome != "answer" && o.Outcome != "blocked" {
		return o, errors.New("invalid answering outcome")
	}
	return o, nil
}

func answeringRunOutcome(run state.Run, runErr error) (owneranswering.Outcome, error) {
	base := owneranswering.Outcome{RunID: run.ID}
	if run.ExecutionStatus != "completed" || run.Error != "" {
		if runErr != nil {
			return base, runErr
		}
		return base, errors.New("question execution failed: " + run.Error)
	}
	o, err := parseAnsweringOutcome(run.Output)
	o.RunID = run.ID
	// Host can return a learning/finalization error after successful execution.
	// Preserve that warning independently; it does not undo the actual task result.
	var warnings []string
	if runErr != nil {
		warnings = append(warnings, runErr.Error())
	}
	if run.MemoryError != "" {
		warnings = append(warnings, run.MemoryError)
	}
	if run.ExperienceError != "" {
		warnings = append(warnings, run.ExperienceError)
	}
	o.Warning = strings.Join(warnings, "; ")
	if err != nil {
		return o, fmt.Errorf("invalid handler outcome: %w", err)
	}
	return o, nil
}
