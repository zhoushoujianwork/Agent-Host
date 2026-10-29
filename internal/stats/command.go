package stats

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type homes []string

func (h *homes) String() string { return strings.Join(*h, ",") }
func (h *homes) Set(s string) error {
	if s == "" {
		return errors.New("empty home")
	}
	*h = append(*h, s)
	return nil
}
func parseTime(s string, now time.Time, relative bool) (time.Time, error) {
	if relative {
		duration := s
		if strings.HasSuffix(s, "d") {
			n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
			if err != nil || n <= 0 || n > 36500 {
				return time.Time{}, errors.New("invalid day duration")
			}
			duration = fmt.Sprintf("%dh", n*24)
		}
		if d, err := time.ParseDuration(duration); err == nil && d > 0 {
			return now.Add(-d), nil
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid time: use RFC3339, YYYY-MM-DD or a positive --since duration")
}

// Run is the read-only command boundary. It emits partial diagnostics even when all stores fail.
func Run(ctx context.Context, args []string, out, errout io.Writer) error {
	now := time.Now().UTC()
	o := Options{Now: now}
	var h homes
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(errout)
	fs.StringVar(&o.Root, "root", "", "absolute runtime root (exclusive with --home)")
	fs.Var(&h, "home", "state directory; repeatable; no default")
	since := fs.String("since", "7d", "duration, RFC3339 or UTC date")
	until := fs.String("until", "", "RFC3339 or UTC date (default now)")
	fs.StringVar(&o.Agent, "agent", "", "filter agent ID")
	fs.StringVar(&o.TaskKind, "task-kind", "", "filter AI task kind (unlabeled for missing)")
	fs.StringVar(&o.Channel, "channel", "", "filter channel")
	fs.BoolVar(&o.Compare, "compare", false, "include preceding equal window")
	fs.DurationVar(&o.StaleAfter, "stale-after", 2*time.Hour, "running age threshold")
	fs.IntVar(&o.Top, "top", 10, "maximum attention entries and example run IDs per class")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("stats does not accept positional arguments")
	}
	if *format != "text" && *format != "json" {
		return errors.New("format must be text or json")
	}
	o.Homes = h
	o.Until = now
	var err error
	if *until != "" {
		o.Until, err = parseTime(*until, now, false)
		if err != nil {
			return err
		}
	}
	o.Since, err = parseTime(*since, o.Until, true)
	if err != nil {
		return err
	}
	report, collectErr := Collect(ctx, o)
	if report == nil {
		return collectErr
	}
	if *format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(report)
	} else {
		err = WriteText(out, report)
	}
	return errors.Join(collectErr, err)
}

// WriteText renders only the sanitized report, preserving nulls and coverage.
func WriteText(out io.Writer, r *Report) error {
	if _, err := fmt.Fprintln(out, "Run statistics — execution, acceptance and memory are independent.\nUnverified acceptance is not a success rate. Unknown measurements are null; coverage is [covered, total]."); err != nil {
		return err
	}
	return writeWindow(out, r)
}
func writeWindow(out io.Writer, r *Report) error {
	if _, err := fmt.Fprintf(out, "Generated: %s\nWindow: %s to %s (exclusive)\n", r.GeneratedAt.Format(time.RFC3339Nano), r.Window.Since.Format(time.RFC3339Nano), r.Window.Until.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	sections := []struct {
		name  string
		value any
	}{
		{"Sources", r.Sources}, {"Totals", r.Totals}, {"Execution", r.Outcomes}, {"Acceptance", r.Acceptance}, {"Success rate", r.SuccessRate}, {"By task kind", r.ByTaskKind}, {"Memory", r.Memory}, {"Daily (UTC)", r.Daily}, {"By agent", r.ByAgent}, {"By channel", r.ByChannel}, {"By model", r.ByModel}, {"Duration (seconds)", r.Duration}, {"Error classes", r.Errors}, {"Tools", r.Tools}, {"Usage (observed subtotals)", r.Usage}, {"Coverage", r.Coverage}, {"Attention", r.Attention},
	}
	for _, section := range sections {
		raw, err := json.Marshal(section.value)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(out, "%s: %s\n", section.name, raw); err != nil {
			return err
		}
	}
	if r.Previous != nil {
		if _, err := fmt.Fprintln(out, "Previous period:"); err != nil {
			return err
		}
		return writeWindow(out, r.Previous)
	}
	return nil
}
