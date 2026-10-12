package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/evaluation"
	"agent-host/internal/event"
)

func evalFixture(t *testing.T, pass bool) string {
	t.Helper()
	c := evaluation.Case{ID: "case-one", Category: "recall.basic", Title: "Recall", Rationale: "Exact lexical match", Origin: evaluation.Origin{Kind: "synthetic"}, Kind: "recall", Scope: "project", Input: "sqlite", Memories: []evaluation.SeedMemory{{ID: "m1", Scope: "project", Kind: "project_fact", Text: "SQLite stores this project's state", Status: "active"}}, Expected: evaluation.Expected{RecallIDs: []string{"m1"}}}
	if !pass {
		c.Expected.RecallIDs = []string{}
	}
	suite := evaluation.Suite{Version: 1, ID: "cli-test", Description: "CLI regression fixture", Cases: []evaluation.Case{c}}
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvalRegressionEventsReportAndNoHostState(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := evalFixture(t, true)
	output := filepath.Join(t.TempDir(), "report.json")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"eval", "--suite", path, "--format", "json", "--output", output}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var events []event.Event
	dec := json.NewDecoder(&out)
	for {
		var e event.Event
		err := dec.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 3 || events[0].Type != "eval.case.started" || events[1].Status != "passed" || events[2].Type != "eval.finished" {
		t.Fatalf("unexpected events: %+v", events)
	}
	for i, e := range events {
		if e.Version != event.Version || e.Seq != int64(i+1) || e.RunID == "" || e.RunID != events[0].RunID || e.Phase != "evaluate" {
			t.Fatalf("bad envelope: %+v", e)
		}
	}
	report := readEvalReport(t, output)
	if !report.Success || report.Totals.Passed != 1 || report.Provider != "" || len(report.SuiteSHA256) != 64 || len(report.BinarySHA256) != 64 {
		t.Fatalf("unexpected report: %+v", report)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("created user host state: %v %v", entries, err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe report permissions: %v %v", info, err)
	}
}

func readEvalReport(t *testing.T, path string) evaluation.Report {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report evaluation.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestEvalMismatchIsErrorAndRetainsReport(t *testing.T) {
	output := filepath.Join(t.TempDir(), "failed.json")
	err := Run(context.Background(), []string{"eval", "--suite", evalFixture(t, false), "--output", output}, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("mismatch reported success")
	}
	report := readEvalReport(t, output)
	if report.Success || report.Totals.Failed != 1 || len(report.Cases[0].Actual.UnexpectedIDs) != 1 {
		t.Fatalf("lost mismatch: %+v", report)
	}
}

func TestEvalInvalidSelectionAndOutputNeverOverwrite(t *testing.T) {
	path := evalFixture(t, true)
	t.Setenv("TYPESAFE_API_KEY", "")
	for _, args := range [][]string{
		{"--mode", "unknown"}, {"--category", "missing"}, {"--case", "missing"}, {"--mode", "live"},
		{"--format", "web"}, {"--timeout", "0s"}, {"--jev-provider", "auto"}, {"--jev-provider", "custom"}, {"positional"},
	} {
		if err := Run(context.Background(), append([]string{"eval", "--suite", path}, args...), nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"eval", "--suite", path, "--output", path}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("overwrote suite")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("suite changed")
	}
	var help bytes.Buffer
	if err := Run(context.Background(), []string{"eval", "--help"}, nil, io.Discard, &help); err != nil || !strings.Contains(help.String(), "suite") {
		t.Fatalf("help: %v %s", err, help.String())
	}
}

type evalBrokenWriter struct{}

func (evalBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("test output failure") }
func TestEvalOutputFailureIsNotSuccessfulReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output-error.json")
	err := Run(context.Background(), []string{"eval", "--suite", evalFixture(t, true), "--output", path}, nil, evalBrokenWriter{}, io.Discard)
	if err == nil {
		t.Fatal("ignored output failure")
	}
	report := readEvalReport(t, path)
	if report.Success || report.Error == "" || report.Totals.NotRun != 1 {
		t.Fatalf("lost output failure: %+v", report)
	}
}

func TestEvalCancellationRetainsNotRunReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "canceled.json")
	err := Run(ctx, []string{"eval", "--suite", evalFixture(t, true), "--output", path}, nil, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	report := readEvalReport(t, path)
	if report.Success || report.Totals.NotRun != 1 || report.Totals.Passed != 0 {
		t.Fatalf("canceled case became pass: %+v", report)
	}
}

// A consumer can disconnect only at the final event; the saved report must
// then preserve that output failure despite all case assertions passing.
type evalFinalBrokenWriter struct{}

func (evalFinalBrokenWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"type":"eval.finished"`)) {
		return 0, errors.New("test summary output failure")
	}
	return len(p), nil
}
func TestEvalFinalEventFailureUpdatesSavedReport(t *testing.T) {
	output := filepath.Join(t.TempDir(), "summary-error.json")
	err := Run(context.Background(), []string{"eval", "--suite", evalFixture(t, true), "--format", "json", "--output", output}, nil, evalFinalBrokenWriter{}, io.Discard)
	if err == nil {
		t.Fatal("ignored summary failure")
	}
	report := readEvalReport(t, output)
	if report.Success || !strings.Contains(report.Error, "summary output failure") || report.Totals.Passed != 1 {
		t.Fatalf("lost final stream failure: %+v", report)
	}
}
