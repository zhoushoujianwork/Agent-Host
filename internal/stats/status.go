package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"agent-host/internal/backup"
	"agent-host/internal/state"
)

type Check struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	Evidence   any    `json:"evidence"`
	NextAction string `json:"next_action,omitempty"`
}
type StatusReport struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Status        string    `json:"status"`
	Checks        []Check   `json:"checks"`
	Unverified    []string  `json:"unverified"`
}

func (r *StatusReport) add(id, scope, status, message string, evidence any) {
	r.Checks = append(r.Checks, Check{ID: id, Scope: scope, Status: status, Message: message, Evidence: evidence})
}
func age(now time.Time, s string) *float64 {
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil || t.IsZero() || t.After(now) {
		return nil
	}
	v := now.Sub(t).Seconds()
	return &v
}

// statusDB never initializes a database or acquires Host ownership.
func statusDB(ctx context.Context, path string, host bool) (*sql.DB, error) {
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() {
		return nil, errors.New("missing or inaccessible database")
	}
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(200)", "query_only(1)"}}.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, errors.New("cannot open database read-only")
	}
	db.SetMaxOpenConns(1)
	var version int
	if e = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); e != nil {
		db.Close()
		return nil, errors.New("cannot read database schema")
	}
	if host && version != state.SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported schema version %d", version)
	}
	return db, nil
}
func fileEvidence(path string) any {
	i, e := os.Stat(path)
	if e != nil {
		return nil
	}
	return map[string]any{"bytes": i.Size(), "modified_at": i.ModTime().UTC()}
}
func (r *StatusReport) disk(scope, path string) {
	r.add("disk", scope, "passed", "File sizes and modification times only; growth is not measured", map[string]any{"database": fileEvidence(path), "wal": fileEvidence(path + "-wal")})
}
func (r *StatusReport) backlog(ctx context.Context, db *sql.DB, id, scope, query string, now time.Time) {
	var count int
	var oldest sql.NullString
	if e := db.QueryRowContext(ctx, query).Scan(&count, &oldest); e != nil {
		r.add(id, scope, "unknown", "Evidence unavailable", nil)
		return
	}
	status := "passed"
	if count > 0 {
		status = "warning"
	}
	r.add(id, scope, status, "Observed backlog; age alone does not establish staleness", map[string]any{"count": count, "oldest_age_seconds": age(now, oldest.String)})
}
func (r *StatusReport) runIDs(ctx context.Context, db *sql.DB, id, scope, condition string, args ...any) {
	var count int
	if e := db.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE "+condition, args...).Scan(&count); e != nil {
		r.add(id, scope, "unknown", "Run evidence unavailable", nil)
		return
	}
	rows, e := db.QueryContext(ctx, "SELECT id FROM runs WHERE "+condition+" ORDER BY created_at,id LIMIT 20", args...)
	if e != nil {
		r.add(id, scope, "unknown", "Run evidence unavailable", nil)
		return
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e != nil || rows.Err() != nil {
		r.add(id, scope, "unknown", "Run evidence unavailable", nil)
		return
	}
	status := "passed"
	if count > 0 {
		status = "warning"
	}
	r.add(id, scope, status, "Current retained runs; at most 20 example IDs", map[string]any{"count": count, "run_ids": ids})
}

func CollectStatus(ctx context.Context, o Options, noHTTP bool, port int) (*StatusReport, error) {
	if !o.Since.Before(o.Until) || o.StaleAfter <= 0 || port < 1 || port > 65535 {
		return nil, errors.New("invalid status window, stale-after or viewer port")
	}
	refs, e := discover(o)
	if e != nil {
		return nil, e
	}
	r := &StatusReport{SchemaVersion: 1, GeneratedAt: o.Now, Status: "ok", Checks: []Check{}, Unverified: []string{"reply_delivery", "read_receipts", "provider_reachability", "business_acceptance", "usage_and_cost_without_measurements", "schedule_evidence"}}
	summary, _ := Collect(ctx, o)
	assessed := 0
	for _, ref := range refs {
		if ref.reason != "" {
			r.add("store."+ref.name, ref.name, "failed", ref.reason, nil)
			continue
		}
		path := filepath.Join(ref.home, "state.db")
		db, e := statusDB(ctx, path, true)
		if e != nil {
			if db != nil {
				db.Close()
			}
			r.add("store."+ref.name, ref.name, "failed", e.Error(), nil)
			continue
		}
		var newest sql.NullString
		e = db.QueryRowContext(ctx, "SELECT created_at FROM runs ORDER BY julianday(created_at) DESC LIMIT 1").Scan(&newest)
		status := "passed"
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			status = "failed"
		} else {
			assessed++
		}
		r.add("store."+ref.name, ref.name, status, "Read-only supported schema; newest run age measures activity, not process liveness", map[string]any{"schema_version": state.SchemaVersion, "newest_run_age_seconds": age(o.Now, newest.String), "database": fileEvidence(path), "wal": fileEvidence(path + "-wal")})
		r.disk(ref.name, path)
		// Compare parsed instants at nanosecond precision rather than SQLite's rounded julianday.
		r.staleRuns(ctx, db, ref.name, o.Now.Add(-o.StaleAfter))
		r.runIDs(ctx, db, "runs.unrecorded", ref.name, "execution_status='unknown'")
		r.failures(ctx, db, ref.name, o)
		r.backlog(ctx, db, "intake.unresolved", ref.name, "SELECT count(*),min(received_at) FROM channel_messages WHERE status NOT IN ('completed','failed')", o.Now)
		var ambiguous int
		if e := db.QueryRowContext(ctx, "SELECT count(*) FROM channel_messages WHERE status='unknown'").Scan(&ambiguous); e == nil {
			c := &r.Checks[len(r.Checks)-1]
			if v, ok := c.Evidence.(map[string]any); ok {
				v["ambiguous_intake_count"] = ambiguous
			}
		}
		r.backlog(ctx, db, "memory.candidates", ref.name, "SELECT count(*),min(json_extract(payload,'$.created_at')) FROM candidates WHERE status='pending'", o.Now)
		r.add("memory.verification", ref.name, "unknown", "No persisted read-only verification record is defined by the current verifier", nil)
		r.Checks[len(r.Checks)-1].NextAction = "agent-host memory verify --home PATH (use this scope's state directory)"
		db.Close()
	}
	for _, id := range []string{"runs.stale_running", "runs.unrecorded", "runs.failures", "intake.unresolved", "memory.candidates", "memory.verification"} {
		found := false
		for _, c := range r.Checks {
			if c.ID == id {
				found = true
				break
			}
		}
		if !found {
			r.add(id, "platform", "unknown", "No readable Host store", nil)
		}
	}
	if summary != nil {
		status := "passed"
		for _, s := range summary.Sources {
			if s.Status != "ok" {
				status = "unknown"
			}
		}
		readable := 0
		for _, source := range summary.Sources {
			if source.Status == "ok" {
				readable++
			}
		}
		var outcomes any = summary.Outcomes
		if readable == 0 {
			outcomes = nil
		}
		r.add("runs.window", "platform", status, "Shared stats outcomes in [since, until); unreadable stores excluded", map[string]any{"window": summary.Window, "outcomes": outcomes, "sources": summary.Sources})
	}
	roots := []string{}
	if o.Root != "" {
		roots = append(roots, o.Root)
	} else {
		seen := map[string]bool{}
		for _, ref := range refs {
			root := ""
			if filepath.Base(ref.home) == "cli-state" {
				root = filepath.Dir(ref.home)
			} else if filepath.Base(ref.home) == "state" && filepath.Base(filepath.Dir(filepath.Dir(ref.home))) == "channels" {
				root = filepath.Dir(filepath.Dir(filepath.Dir(ref.home)))
			}
			if root != "" && !seen[root] {
				seen[root] = true
				roots = append(roots, root)
			}
		}
	}
	for _, root := range roots {
		channels := statusChildren(filepath.Join(root, "channels"))
		ops := []string{}
		for _, channel := range channels {
			path := filepath.Join(channel, "dws", "host-operations.db")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				ops = append(ops, path)
			}
		}
		if len(ops) == 0 {
			r.add("operations.pending", "platform", "unknown", "No operations store discovered", nil)
		}
		for _, path := range ops {
			scope := filepath.Base(filepath.Dir(filepath.Dir(path))) + ".operations"
			db, e := statusDB(ctx, path, false)
			if e != nil {
				r.add("store."+scope, scope, "failed", e.Error(), nil)
				continue
			}
			var n int
			e = db.QueryRowContext(ctx, "SELECT count(status) + count(updated) FROM operations").Scan(&n)
			if e == nil {
				assessed++
				r.add("store."+scope, scope, "passed", "Operations store opened read-only", nil)
			} else {
				r.add("store."+scope, scope, "failed", "Unsupported operations schema", nil)
			}
			r.backlog(ctx, db, "operations.pending", scope, "SELECT count(*),min(updated) FROM operations WHERE status NOT IN ('completed','failed','partial_failure')", o.Now)
			db.Close()
			r.disk(scope, path)
		}
		feedbackPath := filepath.Join(root, "feedback", "state.db")
		db, e := statusDB(ctx, feedbackPath, false)
		if e != nil {
			r.add("feedback.recipients", "platform", "unknown", "Feedback queue unavailable", nil)
		} else {
			r.backlog(ctx, db, "feedback.recipients", "platform", "SELECT count(*),min(json_extract(payload,'$.updated_at')) FROM feedback WHERE status='needs_recipient'", o.Now)
			db.Close()
			r.disk("feedback", feedbackPath)
		}
		b, e := backup.ReadStatus(root)
		if e != nil {
			r.add("backup", "platform", "unknown", "Backup status unavailable", nil)
		} else {
			status := "passed"
			if b.LastError != "" {
				status = "warning"
			}
			r.add("backup", "platform", status, "Recorded backup state", map[string]any{"enabled": b.Enabled, "last_success_age_seconds": age(o.Now, b.LastSuccess.Format(time.RFC3339Nano)), "has_last_error": b.LastError != ""})
		}
		logs := statusChildren(filepath.Join(root, "logs"))
		for _, channel := range channels {
			logs = append(logs, statusChildren(filepath.Join(channel, "logs"))...)
		}
		items := []any{}
		for _, p := range logs {
			if len(items) == 100 {
				break
			}
			if i, e := os.Stat(p); e == nil && i.Mode().IsRegular() {
				items = append(items, fileEvidence(p))
			}
		}
		if len(items) == 0 {
			r.add("disk", "service_logs", "unknown", "No service log metadata available", nil)
		} else {
			r.add("disk", "service_logs", "passed", "At most 100 log file sizes and mtimes; names and contents omitted", items)
		}
	}
	if len(roots) == 0 {
		for _, id := range []string{"operations.pending", "feedback.recipients", "backup"} {
			r.add(id, "platform", "unknown", "Runtime root unavailable with explicit state homes", nil)
		}
	}
	r.add("workbench.http", "loopback", "unknown", "HTTP check disabled", nil)
	if !noHTTP {
		c := &r.Checks[len(r.Checks)-1]
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Timeout: 2 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		resp, e := client.Do(req)
		c.Message = "Loopback GET /; response body discarded"
		if e != nil {
			c.Status = "warning"
			c.Message = "Loopback HTTP request failed"
		} else {
			resp.Body.Close()
			c.Status = "passed"
			if resp.StatusCode != 200 {
				c.Status = "warning"
			}
			c.Evidence = map[string]int{"http_status": resp.StatusCode}
		}
	}
	for _, id := range []string{"service.process", "deployment.mode"} {
		r.add(id, "platform", "unknown", "Not inferred from retained activity", nil)
		r.Checks[len(r.Checks)-1].NextAction = "Read-only process/deployment preflight tracked in issue #11"
	}
	for _, c := range r.Checks {
		if c.Status == "unknown" && r.Status == "ok" {
			r.Status = "unknown"
		}
		if c.Status == "warning" && r.Status != "critical" {
			r.Status = "attention"
		}
		if c.Status == "failed" {
			r.Status = "critical"
		}
	}
	if assessed == 0 {
		return r, fmt.Errorf("no store could be assessed: expected cli-state/state.db or channels/NAME/state/state.db under --root, or state.db inside --home; candidate state directories: %v", statusCandidates(o))
	}
	return r, nil
}

// statusChildren treats the supplied directory literally, including glob metacharacters.
func statusChildren(directory string) []string {
	entries, _ := os.ReadDir(directory)
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, filepath.Join(directory, entry.Name()))
	}
	return paths
}

func statusCandidates(o Options) []string {
	result := []string{}
	bases := o.Homes
	if o.Root != "" {
		bases = []string{o.Root}
	}
	for _, base := range bases {
		for _, root := range []string{base, filepath.Join(base, "runtime")} {
			paths := []string{filepath.Join(root, "cli-state")}
			for _, channel := range statusChildren(filepath.Join(root, "channels")) {
				paths = append(paths, filepath.Join(channel, "state"))
			}
			for _, path := range paths {
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					result = append(result, path)
				}
			}
		}
	}
	return result
}
func RunStatus(ctx context.Context, args []string, out, errout io.Writer) error {
	now := time.Now().UTC()
	o := Options{Now: now, Until: now, Top: 20}
	var h homes
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(errout)
	fs.StringVar(&o.Root, "root", "", "absolute runtime root; exclusive with --home")
	fs.Var(&h, "home", "state home; repeatable; no default")
	window := fs.Duration("window", 24*time.Hour, "run window")
	fs.DurationVar(&o.StaleAfter, "stale-after", 2*time.Hour, "running age threshold")
	port := fs.Int("viewer-port", 8765, "loopback HTTP port")
	noHTTP := fs.Bool("no-http", false, "skip HTTP probe")
	format := fs.String("format", "text", "text or json")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || *window <= 0 || o.StaleAfter <= 0 || *port < 1 || *port > 65535 || (*format != "text" && *format != "json") {
		return errors.New("invalid status arguments")
	}
	o.Homes = h
	o.Since = now.Add(-*window)
	r, e := CollectStatus(ctx, o, *noHTTP, *port)
	if r == nil {
		return e
	}
	var writeErr error
	if *format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		writeErr = enc.Encode(r)
	} else {
		writeErr = WriteStatusText(out, r)
	}
	return errors.Join(e, writeErr)
}
func WriteStatusText(out io.Writer, r *StatusReport) error {
	if _, e := fmt.Fprintf(out, "Platform status: %s\n", r.Status); e != nil {
		return e
	}
	for _, c := range r.Checks {
		b, _ := json.Marshal(c)
		if _, e := fmt.Fprintln(out, string(b)); e != nil {
			return e
		}
	}
	_, e := fmt.Fprintf(out, "Unverified: %v\n", r.Unverified)
	return e
}

func (r *StatusReport) staleRuns(ctx context.Context, db *sql.DB, scope string, cutoff time.Time) {
	rows, e := db.QueryContext(ctx, "SELECT id,created_at FROM runs WHERE execution_status='running' ORDER BY created_at,id")
	if e != nil {
		r.add("runs.stale_running", scope, "unknown", "Run evidence unavailable", nil)
		return
	}
	defer rows.Close()
	count := 0
	ids := []string{}
	valid := true
	for rows.Next() {
		var id, created string
		if rows.Scan(&id, &created) != nil {
			valid = false
			break
		}
		t, e := time.Parse(time.RFC3339Nano, created)
		if e != nil {
			valid = false
			break
		}
		if t.Before(cutoff) {
			count++
			if len(ids) < 20 {
				ids = append(ids, id)
			}
		}
	}
	if !valid || rows.Err() != nil {
		r.add("runs.stale_running", scope, "unknown", "Invalid or unavailable run timestamps", nil)
		return
	}
	status := "passed"
	if count > 0 {
		status = "warning"
	}
	r.add("runs.stale_running", scope, status, "Current running records older than threshold; at most 20 IDs", map[string]any{"count": count, "run_ids": ids})
}

func (r *StatusReport) failures(ctx context.Context, db *sql.DB, scope string, o Options) {
	rows, e := db.QueryContext(ctx, `SELECT id,created_at,json_extract(payload,'$.error') FROM runs WHERE execution_status='failed' AND julianday(created_at)>=julianday(?) AND julianday(created_at)<julianday(?) ORDER BY created_at,id`, o.Since.Add(-time.Second).Format(time.RFC3339Nano), o.Until.Add(time.Second).Format(time.RFC3339Nano))
	if e != nil {
		r.add("runs.failures", scope, "unknown", "Failure evidence unavailable", nil)
		return
	}
	defer rows.Close()
	classes := map[string]*ErrorClass{}
	valid := true
	count := 0
	for rows.Next() {
		var id, created string
		var message sql.NullString
		if rows.Scan(&id, &created, &message) != nil {
			valid = false
			break
		}
		t, e := time.Parse(time.RFC3339Nano, created)
		if e != nil {
			valid = false
			break
		}
		if t.Before(o.Since) || !t.Before(o.Until) {
			continue
		}
		count++
		class := errorClass(message.String)
		if classes[class] == nil {
			classes[class] = &ErrorClass{Class: class, Examples: []string{}}
		}
		c := classes[class]
		c.Runs++
		if len(c.Examples) < 20 {
			c.Examples = append(c.Examples, id)
		}
	}
	if !valid || rows.Err() != nil {
		r.add("runs.failures", scope, "unknown", "Invalid or unavailable failure evidence", nil)
		return
	}
	result := []ErrorClass{}
	for _, c := range classes {
		result = append(result, *c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Class < result[j].Class })
	status := "passed"
	if count > 0 {
		status = "warning"
	}
	r.add("runs.failures", scope, status, "Failed executions within the window; shared stats error classes", result)
}
