// Package stats reads retained Host evidence without opening a Host or migrating state.
package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/harness/usage"
	"agent-host/internal/state"
	_ "modernc.org/sqlite"
)

type Options struct {
	Root                     string
	Homes                    []string
	Since, Until, Now        time.Time
	Agent, Channel, TaskKind string
	StaleAfter               time.Duration
	Top                      int
	Compare                  bool
}
type Source struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Runs   *int   `json:"runs"`
	Reason string `json:"reason,omitempty"`
}
type Window struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}
type Count struct {
	Name string `json:"name"`
	Runs int    `json:"runs"`
}
type ErrorClass struct {
	Class    string   `json:"class"`
	Runs     int      `json:"runs"`
	Examples []string `json:"example_runs"`
}
type Attention struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
}
type Daily struct {
	Date      string `json:"date"`
	Completed int    `json:"completed"`
	Canceled  int    `json:"canceled"`
	Failed    int    `json:"failed"`
	Other     int    `json:"other"`
}
type Duration struct {
	Count  int      `json:"count"`
	Median *float64 `json:"median"`
	P90    *float64 `json:"p90"`
	Max    *float64 `json:"max"`
}
type Tools struct {
	Runs    int     `json:"runs_with_tool_activity"`
	Named   []Count `json:"named"`
	Unnamed int     `json:"unnamed_runs"`
}
type Usage struct {
	Runs        int      `json:"runs_with_usage"`
	Input       *int64   `json:"input_tokens"`
	Output      *int64   `json:"output_tokens"`
	Cost        *float64 `json:"cost_usd"`
	MissingCost int      `json:"runs_missing_cost"`
}
type KindAcceptance struct {
	Name        string         `json:"name"`
	Runs        int            `json:"runs"`
	Outcomes    map[string]int `json:"outcomes"`
	Coverage    [2]int         `json:"coverage"`
	SuccessRate *float64       `json:"success_rate"`
}

func acceptanceRate(outcomes map[string]int) (*float64, int) {
	judged := outcomes["resolved"] + outcomes["partial"] + outcomes["unresolved"]
	if judged == 0 {
		return nil, 0
	}
	rate := float64(outcomes["resolved"]) / float64(judged)
	return &rate, judged
}

type Report struct {
	ByTaskKind    []KindAcceptance  `json:"by_task_kind"`
	SuccessRate   *float64          `json:"success_rate"`
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Window        Window            `json:"window"`
	Sources       []Source          `json:"sources"`
	Totals        map[string]int    `json:"totals"`
	Outcomes      map[string]int    `json:"outcomes"`
	Acceptance    map[string]int    `json:"acceptance"`
	Memory        map[string]int    `json:"memory"`
	Daily         []Daily           `json:"daily"`
	ByAgent       []Count           `json:"by_agent"`
	ByChannel     []Count           `json:"by_channel"`
	ByModel       []Count           `json:"by_model"`
	Duration      Duration          `json:"duration_seconds"`
	Errors        []ErrorClass      `json:"error_classes"`
	Tools         Tools             `json:"tools"`
	Usage         Usage             `json:"usage"`
	Attention     []Attention       `json:"attention"`
	Coverage      map[string][2]int `json:"coverage"`
	Previous      *Report           `json:"previous"`
}
type storeRef struct{ name, home, reason string }

func discover(o Options) ([]storeRef, error) {
	if (o.Root == "") == (len(o.Homes) == 0) {
		return nil, errors.New("exactly one of --root or --home is required")
	}
	var refs []storeRef
	if o.Root != "" {
		if !filepath.IsAbs(o.Root) {
			return nil, errors.New("--root must be absolute")
		}
		refs = append(refs, storeRef{"cli", filepath.Join(o.Root, "cli-state"), ""})
		entries, err := os.ReadDir(filepath.Join(o.Root, "channels"))
		if err != nil && !os.IsNotExist(err) {
			refs = append(refs, storeRef{"channels", "", "cannot discover channel state directories"})
		}
		for _, entry := range entries {
			home := filepath.Join(o.Root, "channels", entry.Name(), "state")
			if _, err := os.Stat(filepath.Join(home, "state.db")); !os.IsNotExist(err) {
				refs = append(refs, storeRef{entry.Name(), home, ""})
			}
		}
	} else {
		for i, h := range o.Homes {
			name := fmt.Sprintf("store-%d", i+1)
			if filepath.Base(h) == "cli-state" {
				name = "cli"
			} else if filepath.Base(h) == "state" {
				name = filepath.Base(filepath.Dir(h))
			}
			refs = append(refs, storeRef{name, h, ""})
		}
	}
	seen := map[string]bool{}
	result := []storeRef{}
	for _, r := range refs {
		if r.reason != "" {
			result = append(result, r)
			continue
		}
		p, err := filepath.Abs(r.home)
		if err != nil {
			return nil, errors.New("invalid home")
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		if !seen[p] {
			seen[p] = true
			r.home = p
			result = append(result, r)
		}
	}
	return result, nil
}

func runCount(r *Report) *int {
	n := r.Totals["runs"]
	return &n
}

// Collect uses one read transaction per store, shared by both comparison windows.
func Collect(ctx context.Context, o Options) (*Report, error) {
	if !o.Since.Before(o.Until) || o.StaleAfter <= 0 || o.Top < 0 {
		return nil, errors.New("invalid window, stale-after or top")
	}
	if o.TaskKind != "" && !regexp.MustCompile(`^[a-z0-9-]{1,32}$`).MatchString(o.TaskKind) {
		return nil, errors.New("invalid task kind")
	}
	refs, err := discover(o)
	if err != nil {
		return nil, err
	}
	a := newAggregate(o.Since, o.Until, o.Now)
	var prev *aggregate
	if o.Compare {
		prev = newAggregate(o.Since.Add(-o.Until.Sub(o.Since)), o.Since, o.Now)
	}
	readable := 0
	for _, ref := range refs {
		cur, old, err := readStore(ctx, ref, o, prev != nil)
		if err != nil {
			s := Source{Name: ref.name, Status: "unreadable", Reason: err.Error()}
			a.r.Sources = append(a.r.Sources, s)
			if prev != nil {
				prev.r.Sources = append(prev.r.Sources, s)
			}
			continue
		}
		readable++
		a.merge(cur)
		a.r.Sources = append(a.r.Sources, Source{Name: ref.name, Status: "ok", Runs: runCount(cur.r)})
		if prev != nil {
			prev.merge(old)
			prev.r.Sources = append(prev.r.Sources, Source{Name: ref.name, Status: "ok", Runs: runCount(old.r)})
		}
	}
	a.finish(o.Top)
	if prev != nil {
		prev.finish(o.Top)
		a.r.Previous = prev.r
	}
	if readable == 0 {
		return a.r, errors.New("no readable state stores; expected cli-state/state.db or channels/NAME/state/state.db under --root, or state.db inside --home")
	}
	return a.r, nil
}

func readStore(ctx context.Context, ref storeRef, o Options, compare bool) (*aggregate, *aggregate, error) {
	if ref.reason != "" {
		return nil, nil, errors.New(ref.reason)
	}
	path := filepath.Join(ref.home, "state.db")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("missing or inaccessible state.db")
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(200)", "query_only(1)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, nil, errors.New("cannot open store read-only")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, errors.New("cannot read store (locked or inaccessible)")
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, nil, errors.New("cannot read schema (locked or invalid database)")
	}
	if version != state.SchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema version %d", version)
	}
	a := newAggregate(o.Since, o.Until, o.Now)
	b := newAggregate(o.Since.Add(-o.Until.Sub(o.Since)), o.Since, o.Now)
	since := o.Since
	if compare {
		since = b.r.Window.Since
	}
	// Widen SQLite's millisecond-precision prefilter; apply exact nanosecond bounds
	// below before reading events. Both historical RFC3339 and UTC offsets work.
	rows, err := tx.QueryContext(ctx, `SELECT id,execution_status,memory_status,
 json_extract(payload,'$.agent_id'),json_extract(payload,'$.model'),json_extract(payload,'$.acceptance_status'),json_extract(payload,'$.created_at'),json_extract(payload,'$.completed_at'),json_extract(payload,'$.source.channel'),json_extract(payload,'$.error'),json_extract(payload,'$.task_kind')
 FROM runs WHERE julianday(created_at)>=julianday(?) AND julianday(created_at)<julianday(?) ORDER BY created_at,id`, since.Add(-time.Second).Format(time.RFC3339Nano), o.Until.Add(time.Second).Format(time.RFC3339Nano))
	if err != nil {
		return nil, nil, errors.New("cannot read runs (locked or invalid schema)")
	}
	type record struct {
		id, status, memory                                               string
		agent, model, accept, created, completed, channel, message, kind sql.NullString
	}
	var records []record
	for rows.Next() {
		var r record
		if err = rows.Scan(&r.id, &r.status, &r.memory, &r.agent, &r.model, &r.accept, &r.created, &r.completed, &r.channel, &r.message, &r.kind); err != nil {
			break
		}
		records = append(records, r)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil || readErr != nil {
		return nil, nil, errors.New("invalid run records")
	}
	for _, r := range records {
		created, err := time.Parse(time.RFC3339Nano, r.created.String)
		if err != nil {
			return nil, nil, errors.New("invalid run timestamp")
		}
		if created.Before(since) || !created.Before(o.Until) {
			continue
		}
		channel := r.channel.String
		if channel == "" {
			channel = ref.name
		}
		if o.Agent != "" && r.agent.String != o.Agent || o.Channel != "" && channel != o.Channel {
			continue
		}
		kind := r.kind.String
		if kind == "" {
			kind = "unlabeled"
		}
		if o.TaskKind != "" && kind != o.TaskKind {
			continue
		}
		events, err := readEvents(ctx, tx, r.id)
		if err != nil {
			return nil, nil, err
		}
		target := a
		if created.Before(o.Since) {
			target = b
		}
		target.addKind(kind, r.accept.String)
		target.add(r.id, r.status, r.memory, r.agent.String, r.model.String, r.accept.String, channel, r.message.String, created, r.completed.String, events, o)
	}
	return a, b, nil
}
func readEvents(ctx context.Context, tx *sql.Tx, id string) ([]event.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT json_object('run_id',run_id,'type',json_extract(payload,'$.type'),'phase',json_extract(payload,'$.phase'),'component',json_extract(payload,'$.component'),'data',json_object('raw',json_extract(payload,'$.data.raw'))) FROM events WHERE run_id=? AND (json_extract(payload,'$.type')='tool.activity' OR (json_extract(payload,'$.component') IN ('harness.claude','harness.claude.extractor') AND json_extract(payload,'$.data.raw.type')='result') OR (json_extract(payload,'$.component')='harness.acp' AND json_extract(payload,'$.type')='agent.result')) ORDER BY seq`, id)
	if err != nil {
		return nil, errors.New("cannot read events")
	}
	defer rows.Close()
	events := []event.Event{}
	for rows.Next() {
		var raw []byte
		var e event.Event
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &e) != nil {
			return nil, errors.New("invalid event records")
		}
		events = append(events, e)
	}
	if rows.Err() != nil {
		return nil, errors.New("cannot read events")
	}
	return events, nil
}

type aggregate struct {
	kinds                           map[string]*KindAcceptance
	r                               *Report
	durations                       []float64
	agents, channels, models, tools map[string]int
	days                            map[string]*Daily
	errors                          map[string]*ErrorClass
}

func zeros(keys ...string) map[string]int {
	m := map[string]int{}
	for _, k := range keys {
		m[k] = 0
	}
	return m
}
func newAggregate(s, u, n time.Time) *aggregate {
	return &aggregate{
		kinds: map[string]*KindAcceptance{},
		r: &Report{
			SchemaVersion: 1, GeneratedAt: n.UTC(), Window: Window{s.UTC(), u.UTC()},
			Sources: []Source{}, Totals: zeros("runs"),
			Outcomes:   zeros("completed", "canceled", "failed", "running", "stale_running", "unrecorded"),
			Acceptance: zeros("unverified", "accepted", "rejected", "resolved", "partial", "unresolved"),
			Memory:     zeros("saved", "unchanged", "skipped", "failed", "other"),
			Daily:      []Daily{}, Errors: []ErrorClass{}, Attention: []Attention{},
			Tools: Tools{Named: []Count{}}, Coverage: map[string][2]int{},
		},
		agents: map[string]int{}, channels: map[string]int{}, models: map[string]int{},
		tools: map[string]int{}, days: map[string]*Daily{}, errors: map[string]*ErrorClass{},
	}
}

var dependencyHTTP = regexp.MustCompile(`(?i)\bHTTP (400|402)\b`)

func errorClass(s string) string {
	if match := dependencyHTTP.FindStringSubmatch(s); match != nil {
		return "dependency_http_" + match[1]
	}
	if strings.Contains(s, "unsupported message type") {
		return "unsupported_input"
	}
	if strings.Contains(s, "experience ") && strings.Contains(s, "exceeds") {
		return "experience_limit"
	}
	// Caller loss is more specific than its appended context-canceled cause.
	for _, p := range [][2]string{{"caller_lost", "caller exited or became unverifiable"}, {"canceled_context", "context canceled"}, {"canceled_deadline", "context deadline exceeded"}, {"protocol_malformed_event", "malformed event"}, {"process_wait_delay", "WaitDelay expired"}, {"process_exit", "exit status"}, {"persist_event", "persist event"}} {
		if strings.Contains(s, p[1]) {
			return p[0]
		}
	}
	return "other"
}
func key(s string) string {
	if s == "" {
		return "unrecorded"
	}
	return s
}
func addInt(p **int64, v *int64) {
	if v != nil {
		if *p == nil {
			*p = new(int64)
		}
		**p += *v
	}
}
func addFloat(p **float64, v *float64) {
	if v != nil {
		if *p == nil {
			*p = new(float64)
		}
		**p += *v
	}
}
func (a *aggregate) cover(k string, n, d int) {
	v := a.r.Coverage[k]
	v[0] += n
	v[1] += d
	a.r.Coverage[k] = v
}
func (a *aggregate) add(id, status, memory, agent, model, accept, channel, message string, created time.Time, completed string, events []event.Event, o Options) {
	r := a.r
	r.Totals["runs"]++
	if _, ok := r.Outcomes[status]; !ok {
		status = "unrecorded"
	}
	if status == "running" && o.Now.Sub(created) > o.StaleAfter {
		status = "stale_running"
	}
	r.Outcomes[status]++
	if _, ok := r.Acceptance[accept]; !ok {
		accept = "unverified"
	}
	r.Acceptance[accept]++
	if _, ok := r.Memory[memory]; !ok {
		memory = "other"
	}
	r.Memory[memory]++
	a.agents[key(agent)]++
	a.models[key(model)]++
	a.channels[channel]++
	date := created.UTC().Format("2006-01-02")
	if a.days[date] == nil {
		a.days[date] = &Daily{Date: date}
	}
	d := a.days[date]
	switch status {
	case "completed":
		d.Completed++
	case "canceled":
		d.Canceled++
	case "failed":
		d.Failed++
	default:
		d.Other++
	}
	if end, err := time.Parse(time.RFC3339Nano, completed); err == nil && !end.Before(created) {
		a.durations = append(a.durations, end.Sub(created).Seconds())
	}
	if message != "" || status == "failed" || status == "canceled" {
		class := errorClass(message)
		if a.errors[class] == nil {
			a.errors[class] = &ErrorClass{Class: class, Examples: []string{}}
		}
		e := a.errors[class]
		e.Runs++
		if len(e.Examples) < o.Top {
			e.Examples = append(e.Examples, id)
		}
	}
	if (status == "stale_running" || status == "unrecorded" || status == "failed" || memory == "failed") && len(r.Attention) < o.Top {
		reason := status
		if memory == "failed" {
			reason = "memory_failed"
		}
		r.Attention = append(r.Attention, Attention{id, reason})
	}
	names := map[string]bool{}
	activity, unnamed := false, false
	for _, e := range events {
		if e.Type != "tool.activity" {
			continue
		}
		activity = true
		raw, _ := e.Data["raw"].(map[string]any)
		meta, _ := raw["_meta"].(map[string]any)
		claude, _ := meta["claudeCode"].(map[string]any)
		name, _ := claude["toolName"].(string)
		if name == "" {
			unnamed = true
		} else {
			names[name] = true
		}
	}
	if activity {
		r.Tools.Runs++
		if unnamed {
			r.Tools.Unnamed++
		}
		covered := 0
		if !unnamed {
			covered = 1
		}
		a.cover("tools_named", covered, 1)
	}
	for n := range names {
		a.tools[n]++
	}
	summary := usage.Summarize(events)
	has := 0
	if summary.CostUSD != nil || summary.Tokens["input_tokens"] != nil || summary.Tokens["output_tokens"] != nil {
		r.Usage.Runs++
		has = 1
	}
	a.cover("usage", has, 1)
	if summary.Calls == 0 || summary.MissingCosts > 0 || summary.CostUSD == nil {
		r.Usage.MissingCost++
	}
	addFloat(&r.Usage.Cost, summary.CostUSD)
	addInt(&r.Usage.Input, summary.Tokens["input_tokens"])
	addInt(&r.Usage.Output, summary.Tokens["output_tokens"])
	for _, f := range []string{"input_tokens", "output_tokens", "cost_usd"} {
		covered := 0
		if f == "cost_usd" {
			if summary.CostUSD != nil && summary.MissingCosts == 0 {
				covered = 1
			}
		} else if summary.Tokens[f] != nil && summary.MissingTokens[f] == 0 {
			covered = 1
		}
		a.cover(f, covered, 1)
	}
}
func (a *aggregate) addKind(kind, outcome string) {
	if a.kinds[kind] == nil {
		a.kinds[kind] = &KindAcceptance{Name: kind, Outcomes: zeros("unverified", "accepted", "rejected", "resolved", "partial", "unresolved")}
	}
	k := a.kinds[kind]
	if _, ok := k.Outcomes[outcome]; !ok {
		outcome = "unverified"
	}
	k.Runs++
	k.Outcomes[outcome]++
}
func (a *aggregate) merge(b *aggregate) {
	for name, k := range b.kinds {
		if a.kinds[name] == nil {
			a.kinds[name] = &KindAcceptance{Name: name, Outcomes: map[string]int{}}
		}
		a.kinds[name].Runs += k.Runs
		for outcome, n := range k.Outcomes {
			a.kinds[name].Outcomes[outcome] += n
		}
	}

	for _, pair := range [][2]map[string]int{{a.r.Totals, b.r.Totals}, {a.r.Outcomes, b.r.Outcomes}, {a.r.Acceptance, b.r.Acceptance}, {a.r.Memory, b.r.Memory}, {a.agents, b.agents}, {a.channels, b.channels}, {a.models, b.models}, {a.tools, b.tools}} {
		for k, v := range pair[1] {
			pair[0][k] += v
		}
	}
	a.durations = append(a.durations, b.durations...)
	for k, d := range b.days {
		if a.days[k] == nil {
			a.days[k] = &Daily{Date: k}
		}
		v := a.days[k]
		v.Completed += d.Completed
		v.Canceled += d.Canceled
		v.Failed += d.Failed
		v.Other += d.Other
	}
	for k, e := range b.errors {
		if a.errors[k] == nil {
			a.errors[k] = &ErrorClass{Class: k, Examples: []string{}}
		}
		v := a.errors[k]
		v.Runs += e.Runs
		v.Examples = append(v.Examples, e.Examples...)
	}
	a.r.Attention = append(a.r.Attention, b.r.Attention...)
	a.r.Tools.Runs += b.r.Tools.Runs
	a.r.Tools.Unnamed += b.r.Tools.Unnamed
	a.r.Usage.Runs += b.r.Usage.Runs
	a.r.Usage.MissingCost += b.r.Usage.MissingCost
	addFloat(&a.r.Usage.Cost, b.r.Usage.Cost)
	addInt(&a.r.Usage.Input, b.r.Usage.Input)
	addInt(&a.r.Usage.Output, b.r.Usage.Output)
	for k, v := range b.r.Coverage {
		a.cover(k, v[0], v[1])
	}
}
func counts(m map[string]int) []Count {
	out := []Count{}
	for k, v := range m {
		out = append(out, Count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs == out[j].Runs {
			return out[i].Name < out[j].Name
		}
		return out[i].Runs > out[j].Runs
	})
	return out
}
func (a *aggregate) finish(top int) {
	r := a.r
	r.ByTaskKind = []KindAcceptance{}
	var judged int
	r.SuccessRate, judged = acceptanceRate(r.Acceptance)
	r.Coverage["acceptance"] = [2]int{judged, r.Totals["runs"]}
	for _, k := range a.kinds {
		k.SuccessRate, judged = acceptanceRate(k.Outcomes)
		k.Coverage = [2]int{judged, k.Runs}
		r.ByTaskKind = append(r.ByTaskKind, *k)
	}
	sort.Slice(r.ByTaskKind, func(i, j int) bool { return r.ByTaskKind[i].Name < r.ByTaskKind[j].Name })
	r.ByAgent = counts(a.agents)
	r.ByChannel = counts(a.channels)
	r.ByModel = counts(a.models)
	r.Tools.Named = counts(a.tools)
	sort.Float64s(a.durations)
	n := len(a.durations)
	r.Duration.Count = n
	if n > 0 {
		median := a.durations[n/2]
		if n%2 == 0 {
			median = (a.durations[n/2-1] + median) / 2
		}
		p90 := a.durations[int(math.Ceil(float64(n)*.9))-1]
		max := a.durations[n-1]
		r.Duration.Median = &median
		r.Duration.P90 = &p90
		r.Duration.Max = &max
	}
	r.Coverage["duration"] = [2]int{n, r.Totals["runs"]}
	for _, k := range []string{"tools_named", "usage", "input_tokens", "output_tokens", "cost_usd"} {
		a.cover(k, 0, 0)
	}
	for _, d := range a.days {
		r.Daily = append(r.Daily, *d)
	}
	sort.Slice(r.Daily, func(i, j int) bool { return r.Daily[i].Date < r.Daily[j].Date })
	for _, e := range a.errors {
		sort.Strings(e.Examples)
		if len(e.Examples) > top {
			e.Examples = e.Examples[:top]
		}
		r.Errors = append(r.Errors, *e)
	}
	sort.Slice(r.Errors, func(i, j int) bool {
		if r.Errors[i].Runs == r.Errors[j].Runs {
			return r.Errors[i].Class < r.Errors[j].Class
		}
		return r.Errors[i].Runs > r.Errors[j].Runs
	})
	if len(r.Attention) > top {
		r.Attention = r.Attention[:top]
	}
}
