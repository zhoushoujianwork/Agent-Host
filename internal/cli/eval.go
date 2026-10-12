package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"agent-host/internal/evaluation"
	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/terminal"
)

func runEval(ctx context.Context, args []string, out, errout io.Writer) (runErr error) {
	var suitePath, format, output, provider, endpoint, model string
	var options evaluation.Options
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(errout)
	fs.StringVar(&suitePath, "suite", "evals/memory-v2.json", "Versioned evaluation suite JSON")
	fs.StringVar(&options.Mode, "mode", "regression", "regression (SQLite) or live (real Jev decisions)")
	fs.StringVar(&options.Category, "category", "", "Exact category filter")
	fs.StringVar(&options.CaseID, "case", "", "Exact case ID filter")
	fs.StringVar(&format, "format", "text", "text or json (NDJSON events)")
	fs.StringVar(&output, "output", "", "Save report to a new file; parent directory must exist")
	fs.StringVar(&provider, "jev-provider", "typesafe", "typesafe (official) or custom; live mode only")
	fs.StringVar(&endpoint, "jev-endpoint", "", "Third-party Jev gateway URL for custom; live mode only")
	fs.StringVar(&model, "jev-model", "", "Jev model; live mode only")
	fs.DurationVar(&options.Timeout, "timeout", 2*time.Minute, "Per-case timeout including fixture setup")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("eval accepts options only")
	}
	if format != "text" && format != "json" {
		return errors.New("format must be text or json")
	}
	if options.Mode != "regression" && options.Mode != "live" {
		return errors.New("evaluation mode must be regression or live")
	}
	if options.Timeout <= 0 {
		return errors.New("evaluation case timeout must be positive")
	}
	if err := validateJevProvider(provider, endpoint); err != nil {
		return err
	}
	suite, hash, err := evaluation.Load(suitePath)
	if err != nil {
		return err
	}
	options.SuiteHash = hash
	count := 0
	for _, c := range suite.Cases {
		if (options.Category == "" || c.Category == options.Category) && (options.CaseID == "" || c.ID == options.CaseID) && (options.Mode == "regression" && c.Kind == "recall" || options.Mode == "live" && c.Kind != "recall") {
			count++
		}
	}
	if count == 0 {
		return errors.New("evaluation filters/mode select no applicable cases")
	}
	if options.Mode == "live" {
		client, err := newJevClient(provider, endpoint, model)
		if err != nil {
			return err
		}
		options.Provider, options.Model = client.Provider(), client.Model()
		options.Decider, options.Judge = memory.New(client), experience.NewJudge(client)
	}
	binaryHash, err := executableHash()
	if err != nil {
		return err
	}
	// Reserve the output before any provider call and never overwrite a baseline.
	var reportFile *os.File
	if output != "" {
		reportFile, err = os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create evaluation report: %w", err)
		}
		defer func() { runErr = errors.Join(runErr, reportFile.Close()) }()
	}
	start := time.Now()
	runID := host.ID()
	renderer := terminal.Renderer{Out: out, JSON: format == "json"}
	var seq int64
	emit := func(e event.Event) error {
		seq++
		e.Version = event.Version
		e.Seq = seq
		e.At = time.Now().UTC()
		e.RunID = runID
		e.Phase = "evaluate"
		e.ElapsedMS = time.Since(start).Milliseconds()
		return renderer.Render(e)
	}
	report, runErr := evaluation.Run(ctx, suite, options, func(r evaluation.CaseResult) error {
		component := "evaluation.sqlite"
		if r.Kind != "recall" {
			component = "evaluation.jev"
		}
		eventType := "eval.case.finished"
		if r.Status == "running" {
			eventType = "eval.case.started"
		}
		message := r.Category + "/" + r.ID
		if r.Reason != "" {
			message += " · " + r.Reason
		}
		if len(r.Actual.MissingIDs) > 0 {
			message += fmt.Sprintf(" missing=%v", r.Actual.MissingIDs)
		}
		if len(r.Actual.UnexpectedIDs) > 0 {
			message += fmt.Sprintf(" unexpected=%v", r.Actual.UnexpectedIDs)
		}
		requested := r.Model
		if requested == "" && r.Selected && options.Mode == "live" {
			requested = options.Model
		}
		return emit(event.Event{Type: eventType, Component: component, Status: r.Status, Agent: "evaluator", Model: requested, Message: message, Data: map[string]any{"case": r, "duration_ms": r.DurationMS}})
	})
	report.BinarySHA256 = binaryHash
	// Write the requested artifact before announcing completion. Keep the opened
	// descriptor so an output failure can be recorded without reopening a path.
	save := func() error {
		if reportFile == nil {
			return nil
		}
		if _, err := reportFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := reportFile.Truncate(0); err != nil {
			return err
		}
		encoder := json.NewEncoder(reportFile)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("write evaluation report: %w", err)
		}
		if err := reportFile.Sync(); err != nil {
			return fmt.Errorf("sync evaluation report: %w", err)
		}
		return nil
	}
	markError := func(err error) {
		runErr = errors.Join(runErr, err)
		report.Success = false
		report.Error = runErr.Error()
	}
	saveErr := save()
	if saveErr != nil {
		markError(saveErr)
	}
	status := "passed"
	if !report.Success {
		status = "failed"
	}
	summaryErr := emit(event.Event{Type: "eval.finished", Component: "evaluation", Status: status, Message: fmt.Sprintf("selected=%d passed=%d failed=%d errors=%d not_run=%d", report.Totals.Selected, report.Totals.Passed, report.Totals.Failed, report.Totals.Errors, report.Totals.NotRun), Data: map[string]any{"report": report, "duration_ms": report.DurationMS}})
	if summaryErr != nil {
		markError(summaryErr)
		// Update a healthy report sink with the observed stream failure. Do not retry
		// a failed disk write or continue executing evaluation cases.
		if saveErr == nil {
			if err := save(); err != nil {
				markError(err)
			}
		}
	}
	if !report.Success {
		runErr = errors.Join(runErr, errors.New("evaluation did not pass; inspect case results"))
	}
	return runErr
}

func executableHash() (hash string, hashErr error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { hashErr = errors.Join(hashErr, file.Close()) }()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
