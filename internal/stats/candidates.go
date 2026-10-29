package stats

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

type CandidateReview struct {
	ID              string   `json:"id"`
	RunID           string   `json:"source_run_id"`
	ScopeRef        string   `json:"scope_ref"`
	PartitionRef    string   `json:"partition_ref"`
	Status          string   `json:"review_state"`
	CreatedAt       string   `json:"created_at"`
	AgeSeconds      *float64 `json:"age_seconds"`
	ReviewedAt      string   `json:"reviewed_at,omitempty"`
	HasReviewNote   bool     `json:"has_review_note"`
	EvidenceCount   int      `json:"source_evidence_count"`
	ExperienceState string   `json:"run_experience_state"`
	Publication     string   `json:"publication"`
}

type CandidateReport struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Status        string            `json:"status_filter"`
	MinAgeSeconds float64           `json:"min_age_seconds"`
	Limit         int               `json:"limit"`
	Truncated     bool              `json:"truncated"`
	Candidates    []CandidateReview `json:"candidates"`
	Unverified    []string          `json:"unverified"`
}

func reviewRef(s string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(s))) }

func candidateReport(ctx context.Context, home, status string, minAge time.Duration, limit int, now time.Time) (CandidateReport, error) {
	r := CandidateReport{SchemaVersion: 1, GeneratedAt: now, Status: status, MinAgeSeconds: minAge.Seconds(), Limit: limit, Candidates: []CandidateReview{}, Unverified: []string{"review_owner", "supersession", "intentional_withholding", "extraction_completion", "publication"}}
	db, err := statusDB(ctx, filepath.Join(home, "state.db"), true)
	if err != nil {
		return r, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT c.id,c.run_id,c.scope,c.agent_id,c.audience_id,c.status,
 COALESCE(json_extract(c.payload,'$.created_at'),''), COALESCE(json_extract(c.payload,'$.reviewed_at'),''),
 COALESCE(json_extract(c.payload,'$.review_note'),'')!='', COALESCE(json_array_length(c.payload,'$.draft.evidence_ids'),0),
 COALESCE(json_extract(r.payload,'$.experience_status'),'unknown')
 FROM candidates c LEFT JOIN runs r ON r.id=c.run_id
 WHERE (?='all' OR c.status=?) AND
 (julianday(json_extract(c.payload,'$.created_at')) IS NULL OR julianday(json_extract(c.payload,'$.created_at'))<=julianday(?) OR julianday(json_extract(c.payload,'$.created_at'))>julianday(?))
 ORDER BY julianday(json_extract(c.payload,'$.created_at')),c.id LIMIT ?`, status, status, now.Add(-minAge).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), limit+1)
	if err != nil {
		return r, errors.New("cannot read candidate review metadata")
	}
	defer rows.Close()
	for rows.Next() {
		var c CandidateReview
		var scope, agent, audience string
		if err := rows.Scan(&c.ID, &c.RunID, &scope, &agent, &audience, &c.Status, &c.CreatedAt, &c.ReviewedAt, &c.HasReviewNote, &c.EvidenceCount, &c.ExperienceState); err != nil {
			return r, errors.New("invalid candidate review metadata")
		}
		if len(r.Candidates) == limit {
			r.Truncated = true
			break
		}
		c.ScopeRef = reviewRef(scope)
		c.PartitionRef = reviewRef(agent + "\x00" + audience)
		c.AgeSeconds = age(now, c.CreatedAt)
		c.Publication = "unverified"
		r.Candidates = append(r.Candidates, c)
	}
	if rows.Err() != nil {
		return r, errors.New("candidate review read failed")
	}
	return r, nil
}

// RunCandidateReview bypasses Host initialization and all mutation paths.
func RunCandidateReview(ctx context.Context, args []string, out, errout io.Writer) error {
	fs := flag.NewFlagSet("memory review-report", flag.ContinueOnError)
	fs.SetOutput(errout)
	home := fs.String("home", "", "explicit state directory")
	status := fs.String("status", "pending", "pending, accepted, rejected or all")
	minAge := fs.Duration("min-age", 7*24*time.Hour, "minimum candidate age; invalid ages remain visible")
	limit := fs.Int("limit", 20, "maximum rows, 1–100")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *home == "" || fs.NArg() != 0 || *limit < 1 || *limit > 100 || *minAge < 0 {
		return errors.New("requires --home, nonnegative --min-age and --limit 1–100; no positional arguments")
	}
	if *status != "pending" && *status != "accepted" && *status != "rejected" && *status != "all" {
		return errors.New("invalid review status")
	}
	absolute, err := filepath.Abs(*home)
	if err != nil {
		return err
	}
	report, err := candidateReport(ctx, absolute, *status, *minAge, *limit, time.Now().UTC())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
