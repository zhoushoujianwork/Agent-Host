package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agent-host/internal/state"
	"agent-host/internal/taskfeedback"
)

// Reviews are operator evidence, not identity mappings or delivery permissions.
type feedbackReview struct {
	Class    string `json:"class"`
	Evidence string `json:"evidence"`
}

type feedbackTriageRow struct {
	Attribution string `json:"attribution"`
	ID          string `json:"id"`
	Age         string `json:"age"`
	AgeBasis    string `json:"age_basis"`
	Origin      string `json:"origin"`
	Owner       string `json:"owner"`
	Status      string `json:"status"`
	Class       string `json:"class"`
	Reason      string `json:"reason"`
}

func writeFeedbackTriage(ctx context.Context, items []taskfeedback.Item, reviewPath string, out io.Writer, now time.Time) error {
	reviews := map[string]feedbackReview{}
	if reviewPath != "" {
		body, err := os.ReadFile(reviewPath)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(body, &reviews); err != nil {
			return err
		}
	}
	known := map[string]bool{}
	for _, item := range items {
		known[item.ID] = item.Status == "needs_recipient"
	}
	for id, r := range reviews {
		if !known[id] || strings.TrimSpace(r.Evidence) == "" || (r.Class != "historical" && r.Class != "test" && r.Class != "terminal_only") {
			return errors.New("triage review requires an unresolved ID, evidence and historical/test/terminal_only class")
		}
	}
	counts := map[string]int{"total": len(items), "unresolved": 0, "actionable": 0, "historical_test": 0, "terminal_only": 0, "awaiting_result": 0, "needs_review": 0, "other": 0}
	rows := []feedbackTriageRow{}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := feedbackTriageRow{ID: item.ID, Status: item.Status, Origin: "cli_or_web", Owner: "unresolved", Class: "other", AgeBasis: "queue_updated_at", Reason: "not_unresolved"}
		if item.Plan.RequestFor != "" {
			row.Owner = fmt.Sprintf("owner-%x", sha256.Sum256([]byte(item.Plan.RequestFor)))[:18]
		}
		if item.Plan.Origin != nil {
			row.Origin = "other_channel"
			if c := item.Plan.Origin.Channel; c == "feishu" || c == "dingtalk" {
				row.Origin = c
			}
		}
		row.Attribution = "unspecified"
		switch item.Plan.Reason {
		case "too_many_owner_candidates", "ownership_classifier_unavailable", "ownership_classification_failed", "invalid_ownership_decision", "ownership_decision_not_confident", "demand_owner_not_resolved", "selected_owner_not_explicitly_named", "demand_owner_name_ambiguous", "notification_user_not_configured", "notification_account_missing_or_ambiguous":
			row.Attribution = item.Plan.Reason
		}
		// Failed attribution leaves RequestFor initialized to the requester; that
		// default is not evidence of demand ownership.
		switch row.Attribution {
		case "too_many_owner_candidates", "ownership_classifier_unavailable", "ownership_classification_failed", "invalid_ownership_decision", "ownership_decision_not_confident", "demand_owner_not_resolved", "selected_owner_not_explicitly_named", "demand_owner_name_ambiguous":
			row.Owner = "unresolved"
		}
		switch item.Error {
		case "verified_user_directory_unavailable", "configured_owner_binding_invalid", "notification target unavailable", "notification identity removed", "notification account binding changed":
			row.Attribution = item.Error
		}
		at := item.UpdatedAt
		if item.Status == "needs_recipient" || item.Status == "awaiting_result" {
			row.Class, row.Reason = "needs_review", "source_unavailable"
			store, err := state.OpenForProjectInspection(item.Home)
			if err == nil {
				run, readErr := store.WithPartition(item.AgentID, item.AudienceID).Run(ctx, item.RunID)
				store.Close()
				if readErr == nil {
					if !run.CreatedAt.IsZero() {
						at, row.AgeBasis = run.CreatedAt, "run_created_at"
					}
					row.Class, row.Reason = "awaiting_result", "no_final_evidence"
					ready := false
					for _, e := range run.Events {
						if e.Type == "run.finished" || e.Type == "execution.ready" {
							ready = true
						}
					}
					if ready {
						row.Class, row.Reason = "actionable", "verify_recipient_and_delivery_history"
					}
				}
			}
		}
		if item.Status == "needs_recipient" {
			counts["unresolved"]++
			if r, ok := reviews[item.ID]; ok {
				row.Class, row.Reason = r.Class, "operator_evidence_in_review_file"
				if r.Class == "historical" || r.Class == "test" {
					row.Class = "historical_test"
				}
			}
		}
		row.Age = "unknown"
		if !at.IsZero() && !at.After(now) {
			age := now.Sub(at)
			switch {
			case age < 24*time.Hour:
				row.Age = "under_1d"
			case age < 7*24*time.Hour:
				row.Age = "1_to_7d"
			case age < 30*24*time.Hour:
				row.Age = "7_to_30d"
			default:
				row.Age = "30d_plus"
			}
		}
		counts[row.Class]++
		rows = append(rows, row)
	}
	// Group keys contain only bounded labels and pseudonymous owners.
	groups := map[string]int{}
	for _, row := range rows {
		groups[row.Age+"/"+row.Origin+"/"+row.Owner+"/"+row.Class]++
	}
	return json.NewEncoder(out).Encode(struct {
		AsOf   time.Time           `json:"as_of"`
		Counts map[string]int      `json:"counts"`
		Groups map[string]int      `json:"groups"`
		Items  []feedbackTriageRow `json:"items"`
	}{now, counts, groups, rows})
}
