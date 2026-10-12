// Package evaluation runs labeled cases against the existing memory components.
// Fixture setup and regression never use a user's host database or native agent.
package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"agent-host/internal/experience"
	"agent-host/internal/state"
)

type Suite struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Description string `json:"description"`
	Cases       []Case `json:"cases"`
}

type Origin struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference,omitempty"`
}

type Case struct {
	ID        string                 `json:"id"`
	Category  string                 `json:"category"`
	Title     string                 `json:"title"`
	Rationale string                 `json:"rationale"`
	Origin    Origin                 `json:"origin"`
	Kind      string                 `json:"kind"`
	Input     string                 `json:"input,omitempty"`
	Scope     string                 `json:"scope"`
	Memories  []SeedMemory           `json:"memories,omitempty"`
	Draft     *state.ExperienceDraft `json:"draft,omitempty"`
	Evidence  []state.ToolEvidence   `json:"evidence,omitempty"`
	Expected  Expected               `json:"expected"`
}

type SeedMemory struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type Expected struct {
	RecallIDs    []string `json:"recall_ids"`
	Store        *bool    `json:"store,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	ConflictIDs  []string `json:"conflict_ids"`
	DuplicateIDs []string `json:"duplicate_ids"`
	Eligible     *bool    `json:"eligible,omitempty"`
}

const maxSuiteBytes = 4 << 20

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)
var category = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,79}$`)

func Load(path string) (Suite, string, error) {
	var s Suite
	f, err := os.Open(path)
	if err != nil {
		return s, "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSuiteBytes+1))
	if err != nil {
		return s, "", err
	}
	if len(raw) > maxSuiteBytes {
		return s, "", errors.New("evaluation suite exceeds 4 MiB")
	}
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return s, "", fmt.Errorf("evaluation suite JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&s); err != nil {
		return s, "", fmt.Errorf("decode evaluation suite: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return s, "", errors.New("evaluation suite must contain exactly one JSON object")
	}
	if err = Validate(s); err != nil {
		return s, "", err
	}
	sum := sha256.Sum256(raw)
	return s, hex.EncodeToString(sum[:]), nil
}

func Validate(s Suite) error {
	if s.Version != 1 || !slug.MatchString(s.ID) || strings.TrimSpace(s.Description) == "" || len(s.Cases) == 0 || len(s.Cases) > 200 {
		return errors.New("suite requires version 1, slug ID, description and 1 to 200 cases")
	}
	ids := map[string]bool{}
	for _, c := range s.Cases {
		if !slug.MatchString(c.ID) || ids[c.ID] {
			return fmt.Errorf("invalid or duplicate case ID %q", c.ID)
		}
		ids[c.ID] = true
		if err := validateCase(c); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	return nil
}

func validateCase(c Case) error {
	if !category.MatchString(c.Category) || !slug.MatchString(c.Scope) || strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Rationale) == "" {
		return errors.New("category/scope must be slugs and title/rationale must be nonempty")
	}
	if c.Origin.Kind != "synthetic" && c.Origin.Kind != "incident" {
		return errors.New("origin kind must be synthetic or incident")
	}
	if c.Origin.Kind == "incident" && strings.TrimSpace(c.Origin.Reference) == "" {
		return errors.New("incident requires a source reference")
	}
	if len(c.Memories) > 100 || len(c.Input) > 16<<10 {
		return errors.New("fixture exceeds memory or input bounds")
	}
	known := map[string]SeedMemory{}
	texts := map[string]bool{}
	for _, m := range c.Memories {
		if !slug.MatchString(m.ID) || !slug.MatchString(m.Scope) || strings.TrimSpace(m.Text) == "" || len(m.Text) > 4<<10 {
			return errors.New("invalid fixture memory ID, scope or text")
		}
		if _, ok := known[m.ID]; ok {
			return errors.New("duplicate fixture memory ID")
		}
		key := m.Scope + "\x00" + m.Text
		if texts[key] {
			return errors.New("duplicate fixture memory text in same scope")
		}
		texts[key] = true
		if !statementKind(m.Kind) {
			return errors.New("fixture memories must be user-statement kinds")
		}
		if m.Status != "active" && m.Status != "conflicted" && m.Status != "invalidated" {
			return errors.New("fixture memory status must be active, conflicted or invalidated")
		}
		known[m.ID] = m
	}
	e := c.Expected
	refs := func(values []string, activeOnly bool) error {
		seen := map[string]bool{}
		for _, id := range values {
			m, ok := known[id]
			if !ok || seen[id] || m.Scope != c.Scope || m.Status == "invalidated" || activeOnly && m.Status != "active" {
				return fmt.Errorf("expectation refers to duplicate or ineligible memory %q", id)
			}
			seen[id] = true
		}
		return nil
	}
	switch c.Kind {
	case "recall":
		if strings.TrimSpace(c.Input) == "" || e.RecallIDs == nil || e.Store != nil || e.Eligible != nil || e.Kind != "" || e.ConflictIDs != nil || e.DuplicateIDs != nil || c.Draft != nil || len(c.Evidence) > 0 {
			return errors.New("recall requires input and explicit recall_ids only")
		}
		if len(e.RecallIDs) > 8 {
			return errors.New("recall expectation exceeds context record budget")
		}
		return refs(e.RecallIDs, true)
	case "statement":
		if strings.TrimSpace(c.Input) == "" || e.Store == nil || e.ConflictIDs == nil || e.DuplicateIDs == nil || e.RecallIDs != nil || e.Eligible != nil || c.Draft != nil || len(c.Evidence) > 0 {
			return errors.New("statement requires input and explicit store, kind, conflict_ids and duplicate_ids")
		}
		if !statementKind(e.Kind) && e.Kind != "none" {
			return errors.New("invalid expected statement kind")
		}
		if e.Kind == "none" && (*e.Store || len(e.ConflictIDs) > 0 || len(e.DuplicateIDs) > 0) {
			return errors.New("none expectation cannot store or relate memory")
		}
		if *e.Store && e.Kind == "none" || len(e.ConflictIDs) > 0 && !*e.Store {
			return errors.New("inconsistent statement storage expectation")
		}
		if err := refs(e.ConflictIDs, false); err != nil {
			return err
		}
		return refs(e.DuplicateIDs, true)
	case "experience":
		if c.Draft == nil || e.Eligible == nil || e.Kind != "procedure" || e.Store != nil || e.RecallIDs != nil || e.ConflictIDs != nil || e.DuplicateIDs != nil || len(c.Memories) > 0 || c.Input != "" {
			return errors.New("experience requires draft, evidence and explicit eligible/kind only")
		}
		return experience.ValidateDraft(*c.Draft, c.Evidence)
	default:
		return errors.New("unsupported case kind")
	}
}

func statementKind(s string) bool {
	return s == "preference" || s == "constraint" || s == "project_fact" || s == "decision"
}

// Duplicate labels are ambiguous even when encoding/json accepts the last one.
func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return errors.New("object key must be a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
