package cli

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"

	"agent-host/internal/buildinfo"
	"agent-host/internal/state"
)

type feedbackBuild struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Modified  bool   `json:"modified"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

type feedbackDatabase struct {
	Status        string `json:"status"`
	SchemaVersion *int   `json:"schema_version"`
}

type feedbackTools struct {
	Calls   int `json:"calls"`
	Results int `json:"results"`
	Errors  int `json:"errors"`
}

type feedbackMemories struct {
	InjectedCount int            `json:"injected_count"`
	ByKind        map[string]int `json:"by_kind"`
	ByStatus      map[string]int `json:"by_status"`
	BySourceType  map[string]int `json:"by_source_type"`
}

type feedbackRun struct {
	ExecutionStatus  string             `json:"execution_status"`
	MemoryStatus     string             `json:"memory_status"`
	ExperienceStatus string             `json:"experience_status"`
	AcceptanceStatus string             `json:"acceptance_status"`
	ReplyStatus      string             `json:"reply_status"`
	Model            string             `json:"model"`
	PhaseDurationMS  map[string]float64 `json:"phase_duration_ms"`
	Tools            feedbackTools      `json:"tools"`
	Memories         feedbackMemories   `json:"memories"`
	ProgressWarning  bool               `json:"progress_warning"`
}

type feedbackReport struct {
	SchemaVersion          int              `json:"schema_version"`
	ReportID               string           `json:"report_id"`
	ReportGeneratorBuild   feedbackBuild    `json:"report_generator_build"`
	AffectedExecutionBuild string           `json:"affected_execution_build,omitempty"`
	Database               feedbackDatabase `json:"database"`
	Run                    *feedbackRun     `json:"run,omitempty"`
}

func runFeedback(ctx context.Context, args []string, out, errout io.Writer) error {
	if len(args) == 0 || args[0] != "report" {
		return errors.New("feedback requires report")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return errors.New("feedback report could not locate the default home")
	}
	flags := flag.NewFlagSet("feedback report", flag.ContinueOnError)
	flags.SetOutput(errout)
	stateHome := flags.String("home", filepath.Join(cwd, ".agent-host", "cli-state"), "existing Host state directory")
	runID := flags.String("run", "", "optional run to inspect locally")
	output := flags.String("output", "", "new local report file; existing files are never replaced")
	agent := flags.String("agent", state.DefaultAgentID, "logical Agent instance")
	audience := flags.String("audience", state.OwnerAudienceID, "conversation partition")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *output == "" || *stateHome == "" || *agent == "" || *audience == "" {
		return errors.New("feedback report requires --output and nonempty options, without positional arguments")
	}
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return errors.New("feedback report could not create an independent report ID")
	}
	info := buildinfo.Current()
	report := feedbackReport{
		SchemaVersion: 1, ReportID: hex.EncodeToString(randomID[:]),
		ReportGeneratorBuild: feedbackBuild{Version: info.Version, Revision: info.Revision, Modified: info.Modified, GoVersion: info.GoVersion, GOOS: info.GOOS, GOARCH: info.GOARCH},
		Database:             feedbackDatabase{Status: "unavailable"},
	}
	if err := readFeedbackState(ctx, *stateHome, *runID, *agent, *audience, &report); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.New("feedback report could not encode the allowed fields")
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("feedback report output already exists: %w", os.ErrExist)
		}
		return errors.New("feedback report could not create the output file")
	}
	_, writeErr := file.Write(append(payload, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return errors.New("feedback report could not finish writing the output file")
	}
	_, err = io.WriteString(out, "Created a local feedback report. Nothing was submitted.\n")
	return err
}

func readFeedbackState(ctx context.Context, home, runID, agent, audience string, report *feedbackReport) (readErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); err != nil {
		if runID == "" {
			return nil
		}
		return errors.New("feedback run report requires a readable existing state database")
	}
	owner, err := state.Open(home, true)
	if err != nil {
		if runID == "" {
			return nil
		}
		return errors.New("feedback report could not read the existing state database")
	}
	defer func() {
		if err := owner.Close(); err != nil {
			readErr = errors.Join(readErr, errors.New("feedback report could not close the state reader"))
		}
	}()
	version, err := owner.DatabaseSchemaVersion(ctx)
	if err != nil {
		return errors.New("feedback report could not read the database schema version")
	}
	report.Database = feedbackDatabase{Status: "available", SchemaVersion: &version}
	if runID == "" {
		return nil
	}
	store := owner.WithPartition(agent, audience)
	run, err := store.Run(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("feedback report run was not found")
		}
		return errors.New("feedback report could not read the run in the selected partition")
	}
	summary := projectFeedbackRun(run)
	summary.ReplyStatus = "not_applicable"
	if run.Source != nil && run.Source.ChannelMessageID != "" {
		message, err := store.ChannelMessage(ctx, run.Source.ChannelMessageID)
		if err != nil {
			return errors.New("feedback report could not read the linked reply status")
		}
		messageAgent, messageAudience := message.AgentID, message.AudienceID
		if messageAgent == "" {
			messageAgent = state.DefaultAgentID
		}
		if messageAudience == "" {
			messageAudience = state.OwnerAudienceID
		}
		if messageAgent != agent || messageAudience != audience || message.RunID != run.ID {
			return errors.New("feedback report reply does not match the selected run partition")
		}
		summary.ReplyStatus = feedbackEnum(message.ReplyStatus, "pending", "sending", "accepted", "failed", "unknown")
	}
	report.Run = &summary
	report.AffectedExecutionBuild = "unknown"
	return nil
}

// Numeric version segments permit recorded Claude names, not arbitrary metadata.
// Unknown/custom models are never replaced with the requested model.
var feedbackModel = regexp.MustCompile(`^claude-(sonnet|opus|haiku)(-[0-9]{1,2}){1,3}(-[0-9]{8})?$`)

func feedbackEnum(value string, allowed ...string) string {
	for _, item := range allowed {
		if value == item {
			return value
		}
	}
	return "unknown"
}

func projectFeedbackRun(run state.Run) feedbackRun {
	summary := feedbackRun{
		ExecutionStatus:  feedbackEnum(run.ExecutionStatus, "running", "completed", "failed", "canceled", "unknown"),
		MemoryStatus:     feedbackEnum(run.MemoryStatus, "blocked", "pending_execution", "pending", "saved", "unchanged", "failed", "skipped"),
		ExperienceStatus: feedbackEnum(run.ExperienceStatus, "pending", "empty", "review_pending", "reviewed", "failed", "skipped"),
		AcceptanceStatus: feedbackEnum(run.AcceptanceStatus, "unverified"),
		Model:            "unknown", PhaseDurationMS: map[string]float64{},
		Memories: feedbackMemories{InjectedCount: len(run.Context), ByKind: map[string]int{}, ByStatus: map[string]int{}, BySourceType: map[string]int{}},
	}
	if feedbackModel.MatchString(run.Model) {
		summary.Model = run.Model
	}
	for _, memory := range run.Context {
		summary.Memories.ByKind[feedbackEnum(memory.Kind, "preference", "constraint", "project_fact", "decision", "procedure")]++
		summary.Memories.ByStatus[feedbackEnum(memory.Status, "active", "conflicted", "invalidated")]++
		summary.Memories.BySourceType[feedbackEnum(memory.SourceType, "user_statement", "reviewed_experience")]++
	}
	calls, results := map[string]bool{}, map[string]bool{}
	for _, observation := range run.Events {
		if observation.Type == "phase.finished" {
			if text, ok := observation.Data["progress_error"].(string); ok && text != "" {
				summary.ProgressWarning = true
			}
			phase := feedbackEnum(observation.Phase, "recall", "execute", "remember", "persist", "evidence", "extract", "assess", "candidates", "review", "publish", "knowledge_search", "knowledge_read")
			if duration, ok := observation.Data["duration_ms"].(float64); ok && phase != "unknown" && duration >= 0 && !math.IsInf(duration, 0) && !math.IsNaN(duration) {
				summary.PhaseDurationMS[phase] = duration
			}
		}
		if observation.Type != "tool.activity" || observation.Phase != "execute" {
			continue
		}
		tool, ok := observation.Data["tool"].(map[string]any)
		if !ok {
			continue
		}
		id, _ := tool["id"].(string)
		stage, _ := tool["stage"].(string)
		if id == "" {
			continue
		}
		switch stage {
		case "call":
			if !calls[id] {
				calls[id] = true
				summary.Tools.Calls++
			}
		case "result":
			if !results[id] {
				results[id] = true
				summary.Tools.Results++
				if isError, _ := tool["is_error"].(bool); isError {
					summary.Tools.Errors++
				}
			}
		}
	}
	return summary
}
