package stats

import (
	"context"
	"database/sql"
	"time"
)

// healthWindow projects only stage metadata. It never decodes raw message bodies.
func (r *StatusReport) healthWindow(ctx context.Context, db *sql.DB, scope string, o Options) {
	r.healthCounts(ctx, db, scope, "runs.stages", `SELECT created_at, memory_status, json_extract(payload,'$.experience_status'), json_extract(payload,'$.memory_error'), json_extract(payload,'$.experience_error') FROM runs`, o, false)
	r.healthCounts(ctx, db, scope, "intake.window", `SELECT received_at,status,json_extract(payload,'$.reply_status'),json_extract(payload,'$.error'),json_extract(payload,'$.reply_error') FROM channel_messages`, o, true)
	r.phaseLatency(ctx, db, scope, o)
}

func (r *StatusReport) healthCounts(ctx context.Context, db *sql.DB, scope, id, query string, o Options, messages bool) {
	column := "created_at"
	if messages {
		column = "received_at"
	}
	rows, err := db.QueryContext(ctx, query+" WHERE julianday("+column+")>=julianday(?) AND julianday("+column+")<julianday(?)", o.Since.Add(-time.Second).Format(time.RFC3339Nano), o.Until.Add(time.Second).Format(time.RFC3339Nano))
	if err != nil {
		r.add(id, scope, "unknown", "Stage evidence unavailable", nil)
		return
	}
	defer rows.Close()
	first, second := zeros("unknown"), zeros("unknown")
	if messages {
		first = zeros("completed", "failed", "queued", "claimed", "unknown")
		second = zeros("accepted", "failed", "pending", "sending", "unknown")
	} else {
		first = zeros("saved", "unchanged", "skipped", "pending", "failed", "unknown")
		second = zeros("reviewed", "review_pending", "empty", "skipped", "pending", "failed", "unknown")
	}
	failures := map[string]int{}
	total := 0
	valid := true
	for rows.Next() {
		var at string
		var a, b, c, d sql.NullString
		if rows.Scan(&at, &a, &b, &c, &d) != nil {
			valid = false
			break
		}
		instant, e := time.Parse(time.RFC3339Nano, at)
		if e != nil {
			valid = false
			break
		}
		if instant.Before(o.Since) || !instant.Before(o.Until) {
			continue
		}
		total++
		for _, item := range []struct {
			counts map[string]int
			value  string
		}{{first, a.String}, {second, b.String}} {
			value := item.value
			if _, ok := item.counts[value]; !ok {
				value = "unknown"
			}
			item.counts[value]++
		}
		// Error counts are observations, not unique failed runs or a reliability rate.
		for _, message := range []string{c.String, d.String} {
			if message != "" {
				failures[errorClass(message)]++
			}
		}
	}
	if !valid || rows.Err() != nil {
		r.add(id, scope, "unknown", "Invalid or unavailable stage evidence", nil)
		return
	}
	a, b, unit := "memory", "experience", "runs"
	if messages {
		a, b, unit = "processing", "reply_platform", "messages"
	}
	status := "passed"
	if first["failed"]+second["failed"] > 0 {
		status = "warning"
	} else if first["unknown"]+second["unknown"] > 0 {
		status = "unknown"
	}
	r.add(id, scope, status, "Independent recorded outcomes; reply platform acceptance is not a read receipt or business acceptance", map[string]any{"window": Window{o.Since, o.Until}, "denominator": total, "unit": unit, a: first, b: second, "error_observations": failures})
}

func (r *StatusReport) phaseLatency(ctx context.Context, db *sql.DB, scope string, o Options) {
	rows, err := db.QueryContext(ctx, `SELECT runs.created_at,json_extract(events.payload,'$.phase'),json_extract(events.payload,'$.data.duration_ms') FROM events JOIN runs ON runs.id=events.run_id WHERE json_extract(events.payload,'$.type')='phase.finished' AND julianday(runs.created_at)>=julianday(?) AND julianday(runs.created_at)<julianday(?)`, o.Since.Add(-time.Second).Format(time.RFC3339Nano), o.Until.Add(time.Second).Format(time.RFC3339Nano))
	if err != nil {
		r.add("stages.latency", scope, "unknown", "Stage latency unavailable", nil)
		return
	}
	defer rows.Close()
	type measurement struct {
		Observations int      `json:"observations"`
		Timed        int      `json:"timed_observations"`
		Total        *float64 `json:"total_seconds"`
		Max          *float64 `json:"max_seconds"`
	}
	stages := map[string]*measurement{}
	valid := true
	for rows.Next() {
		var at string
		var phase sql.NullString
		var duration sql.NullFloat64
		if rows.Scan(&at, &phase, &duration) != nil {
			valid = false
			break
		}
		instant, e := time.Parse(time.RFC3339Nano, at)
		if e != nil {
			valid = false
			break
		}
		if instant.Before(o.Since) || !instant.Before(o.Until) {
			continue
		}
		switch phase.String {
		case "recall", "execute", "remember", "persist", "evidence", "extract", "assess", "candidates", "review", "publish":
		default:
			continue
		}
		m := stages[phase.String]
		if m == nil {
			m = &measurement{}
			stages[phase.String] = m
		}
		m.Observations++
		if duration.Valid && duration.Float64 >= 0 {
			seconds := duration.Float64 / 1000
			if m.Total == nil {
				m.Total = new(float64)
				m.Max = new(float64)
			}
			m.Timed++
			*m.Total += seconds
			if seconds > *m.Max {
				*m.Max = seconds
			}
		}
	}
	if !valid || rows.Err() != nil {
		r.add("stages.latency", scope, "unknown", "Invalid or unavailable stage timing", nil)
		return
	}
	status := "passed"
	for _, m := range stages {
		if m.Timed != m.Observations {
			status = "unknown"
		}
	}
	if len(stages) == 0 {
		status = "unknown"
	}
	r.add("stages.latency", scope, status, "Recorded phase.finished observations for runs created in the window; repeated phases count separately; missing timings are not zero", map[string]any{"window": Window{o.Since, o.Until}, "stages": stages})
}
