package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"agent-host/internal/annotation"
	"agent-host/internal/assembly"
	"agent-host/internal/experience"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type Options struct {
	Mode, Category, CaseID, SuiteHash, Provider, Model string
	Timeout                                            time.Duration // Per case, including fixture setup and provider decisions.
	Decider                                            memory.Decider
	Judge                                              experience.Judge
}

type Actual struct {
	Query         *annotation.Query         `json:"query,omitempty"`
	Retrieval     []state.RetrievalDecision `json:"retrieval,omitempty"`
	RecallIDs     []string                  `json:"recall_ids,omitempty"`
	MatchedIDs    []string                  `json:"matched_ids,omitempty"`
	MissingIDs    []string                  `json:"missing_ids,omitempty"`
	UnexpectedIDs []string                  `json:"unexpected_ids,omitempty"`
	Decision      *memory.Decision          `json:"decision,omitempty"`
	Assessment    *experience.Assessment    `json:"assessment,omitempty"`
}

type CaseResult struct {
	ID         string   `json:"id"`
	Category   string   `json:"category"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Origin     Origin   `json:"origin"`
	Rationale  string   `json:"rationale"`
	Selected   bool     `json:"selected"`
	Status     string   `json:"status"` // running (event only), passed, failed, error, not_run
	Expected   Expected `json:"expected"`
	Actual     Actual   `json:"actual"`
	DurationMS int64    `json:"duration_ms"`
	Model      string   `json:"model,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

type Counts struct {
	Total    int `json:"total"`
	Selected int `json:"selected"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Errors   int `json:"errors"`
	NotRun   int `json:"not_run"`
}

type Report struct {
	Version        int               `json:"version"`
	SuiteID        string            `json:"suite_id"`
	SuiteSHA256    string            `json:"suite_sha256"`
	Mode           string            `json:"mode"`
	Category       string            `json:"category,omitempty"`
	CaseID         string            `json:"case_id,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	RequestedModel string            `json:"requested_model,omitempty"`
	BinarySHA256   string            `json:"binary_sha256,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	DurationMS     int64             `json:"duration_ms"`
	Success        bool              `json:"success"`
	Error          string            `json:"error,omitempty"`
	Totals         Counts            `json:"totals"`
	ByCategory     map[string]Counts `json:"by_category"`
	Cases          []CaseResult      `json:"cases"`
}

func Run(ctx context.Context, s Suite, o Options, emit func(CaseResult) error) (report Report, runErr error) {
	start := time.Now()
	report = Report{Version: 1, SuiteID: s.ID, SuiteSHA256: o.SuiteHash, Mode: o.Mode, Category: o.Category, CaseID: o.CaseID, StartedAt: start.UTC(), ByCategory: map[string]Counts{}, Cases: []CaseResult{}}
	defer func() {
		report.DurationMS = time.Since(start).Milliseconds()
		if runErr != nil {
			report.Error = runErr.Error()
		}
		report.Success = runErr == nil && report.Totals.Selected > 0 && report.Totals.Passed == report.Totals.Selected
	}()
	if err := Validate(s); err != nil {
		return report, err
	}
	if o.Mode != "regression" && o.Mode != "live" {
		return report, errors.New("evaluation mode must be regression or live")
	}
	if o.Timeout <= 0 {
		return report, errors.New("evaluation case timeout must be positive")
	}
	if report.SuiteSHA256 == "" {
		raw, err := json.Marshal(s)
		if err != nil {
			return report, err
		}
		sum := sha256.Sum256(raw)
		report.SuiteSHA256 = hex.EncodeToString(sum[:])
	}
	if o.Mode == "live" {
		report.Provider = o.Provider
		report.RequestedModel = o.Model
	}
	selected := func(c Case) bool {
		return (o.Category == "" || o.Category == c.Category) && (o.CaseID == "" || o.CaseID == c.ID) && (o.Mode == "regression" && c.Kind == "recall" || o.Mode == "live" && c.Kind != "recall")
	}
	count := 0
	for _, c := range s.Cases {
		if selected(c) {
			count++
			if c.Kind == "statement" && o.Decider == nil || c.Kind == "experience" && o.Judge == nil {
				return report, errors.New("selected live cases require real configured decision components")
			}
		}
	}
	if count == 0 {
		return report, errors.New("evaluation filters/mode select no applicable cases")
	}
	var stopped error
	outputFailed := false
	for _, c := range s.Cases {
		r := CaseResult{ID: c.ID, Category: c.Category, Title: c.Title, Kind: c.Kind, Origin: c.Origin, Rationale: c.Rationale, Expected: c.Expected, Selected: selected(c), Status: "not_run", Reason: "excluded by mode or filter"}
		if r.Selected {
			if stopped == nil {
				stopped = ctx.Err()
			}
			if stopped != nil {
				r.Reason = "run stopped: " + stopped.Error()
			} else {
				caseStart := time.Now()
				r.Status = "running"
				r.Reason = ""
				if emit != nil {
					stopped = emit(r)
					outputFailed = stopped != nil
				}
				if stopped != nil {
					r.Status = "not_run"
					r.Reason = "case-start output failed: " + stopped.Error()
				} else {
					caseCtx, cancel := context.WithTimeout(ctx, o.Timeout)
					r.Actual, stopped = execute(caseCtx, c, o)
					cancel()
					r.DurationMS = time.Since(caseStart).Milliseconds()
					if r.Actual.Decision != nil {
						r.Model = r.Actual.Decision.Model
					}
					if r.Actual.Assessment != nil {
						r.Model = r.Actual.Assessment.Model
					}
					if stopped != nil {
						r.Status = "error"
						r.Reason = stopped.Error()
					} else {
						r.Status = "passed"
						if !matches(c.Expected, r.Actual, c.Kind) {
							r.Status = "failed"
							r.Reason = "actual result differs from the reviewed expectation"
						}
					}
				}
			}
		}
		report.Cases = append(report.Cases, r)
		report.Totals = addCount(report.Totals, r)
		report.ByCategory[r.Category] = addCount(report.ByCategory[r.Category], r)
		if emit != nil && !outputFailed {
			if err := emit(r); err != nil {
				stopped = errors.Join(stopped, err)
				outputFailed = true
			}
		}
		// An operational error stops dependent provider work; a labeled quality
		// mismatch does not stop independent cases. Neither becomes success.
	}
	return report, stopped
}

func addCount(n Counts, r CaseResult) Counts {
	n.Total++
	if r.Selected {
		n.Selected++
	}
	switch r.Status {
	case "passed":
		n.Passed++
	case "failed":
		n.Failed++
	case "error":
		n.Errors++
	case "not_run":
		n.NotRun++
	}
	return n
}

func matches(e Expected, a Actual, kind string) bool {
	switch kind {
	case "recall":
		return equalSet(e.RecallIDs, a.RecallIDs)
	case "statement":
		return a.Decision != nil && a.Decision.Store == *e.Store && a.Decision.Kind == e.Kind && equalSet(a.Decision.ConflictIDs, e.ConflictIDs) && equalSet(a.Decision.DuplicateIDs, e.DuplicateIDs)
	case "experience":
		return a.Assessment != nil && a.Assessment.Eligible == *e.Eligible && a.Assessment.Kind == e.Kind
	}
	return false
}
func equalSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
func difference(a, b []string) []string {
	out := []string{}
	for _, id := range a {
		if !slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	return out
}

func execute(ctx context.Context, c Case, o Options) (Actual, error) {
	switch c.Kind {
	case "recall":
		return recall(ctx, c)
	case "statement":
		existing := []state.Memory{}
		for _, m := range c.Memories {
			if m.Scope == c.Scope && m.Status != "invalidated" {
				existing = append(existing, state.Memory{ID: m.ID, Scope: m.Scope, Kind: m.Kind, Text: m.Text, Status: m.Status, Revision: 1, SourceType: "user_statement"})
			}
		}
		d, err := o.Decider.Decide(ctx, c.Input, existing)
		return Actual{Decision: &d}, err
	case "experience":
		a, err := o.Judge.Assess(ctx, *c.Draft, c.Evidence)
		return Actual{Assessment: &a}, err
	}
	return Actual{}, errors.New("unsupported evaluation kind")
}

// recall seeds through production state transitions, then runs the same FTS and
// selection functions as Host.Run. Expectations never influence fixture state.
func recall(ctx context.Context, c Case) (actual Actual, runErr error) {
	root, err := os.MkdirTemp("", "agent-host-eval-")
	if err != nil {
		return actual, err
	}
	defer func() { runErr = errors.Join(runErr, os.RemoveAll(root)) }()
	release, err := state.Acquire(root)
	if err != nil {
		return actual, err
	}
	defer func() { runErr = errors.Join(runErr, release()) }()
	store, err := state.Open(root, false)
	if err != nil {
		return actual, err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	scopePath := func(label string) string { return filepath.Join(root, "workspaces", label) }
	sessions := map[string]state.Session{}
	ensureSession := func(label string) (state.Session, error) {
		if s, ok := sessions[label]; ok {
			return s, nil
		}
		path := scopePath(label)
		if err := os.MkdirAll(path, 0700); err != nil {
			return state.Session{}, err
		}
		s := state.Session{ID: "session-" + label, NativeSessionID: "native-" + label, Scope: path, Model: "fixture"}
		if err := store.CreateSession(ctx, s); err != nil {
			return s, err
		}
		sessions[label] = s
		return s, nil
	}
	for index, m := range c.Memories {
		session, err := ensureSession(m.Scope)
		if err != nil {
			return actual, err
		}
		finalize := func(suffix string, conflicts []string) error {
			runID := fmt.Sprintf("seed-%d%s", index, suffix)
			r := state.Run{ID: runID, SessionID: session.ID, NativeSessionID: session.NativeSessionID, Scope: session.Scope, Input: m.Text}
			if err := store.CreateRun(ctx, r); err != nil {
				return err
			}
			if err := store.CompleteRun(ctx, runID, "completed", "synthetic fixture setup", "fixture", ""); err != nil {
				return err
			}
			record := state.Memory{ID: m.ID + suffix, SourceRunID: runID, Scope: session.Scope, Kind: m.Kind, Text: m.Text, Status: "active", Revision: 1}
			return store.FinalizeMemory(ctx, runID, []state.MemoryUpdate{{Memory: record, ConflictIDs: conflicts}}, json.RawMessage(`{"fixture_setup":true}`))
		}
		if err := finalize("", nil); err != nil {
			return actual, fmt.Errorf("seed memory %s: %w", m.ID, err)
		}
		if m.Status == "conflicted" {
			if err := finalize("-conflict", []string{m.ID}); err != nil {
				return actual, err
			}
		}
		if m.Status == "invalidated" {
			if err := store.InvalidateMemory(ctx, m.ID, "synthetic evaluation fixture"); err != nil {
				return actual, err
			}
		}
	}
	scope := scopePath(c.Scope)
	retrieval, err := store.Retrieve(ctx, scope, c.Input, 16)
	if err != nil {
		return actual, err
	}
	selected := assembly.Select(retrieval.Matches, retrieval.Persistent, 12*1024)
	actual.Query = &retrieval.Query
	actual.Retrieval = retrieval.Decisions
	actual.RecallIDs = []string{}
	actual.MatchedIDs = []string{}
	for _, m := range selected {
		actual.RecallIDs = append(actual.RecallIDs, m.ID)
	}
	actual.MatchedIDs = append(actual.MatchedIDs, retrieval.LexicalIDs...)
	actual.MissingIDs = difference(c.Expected.RecallIDs, actual.RecallIDs)
	actual.UnexpectedIDs = difference(actual.RecallIDs, c.Expected.RecallIDs)
	return actual, nil
}
