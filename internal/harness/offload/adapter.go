// Package offload adapts bounded Offload execution to the shared harness boundary.
// Host routing, session bindings, persistence and memory policy stay in Host.
package offload

import (
	"context"
	"errors"
	"fmt"

	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
)

// Executor owns one invocation and its cleanup. The production implementation
// is either the explicit CLI compatibility client or the Go package client;
// the adapter never retries or switches engines.
type Executor interface {
	Run(context.Context, offloadexec.Request) (offloadexec.Result, error)
}

type Adapter struct{ client Executor }

func New(client Executor) *Adapter { return &Adapter{client: client} }

var _ harness.Runner = (*Adapter)(nil)

func (a *Adapter) Identity() harness.Identity {
	transport := "exec"
	if client, ok := a.client.(interface{ Transport() string }); ok {
		transport = client.Transport()
	}
	return harness.Identity{Agent: "offload", Component: "harness.offload", Engine: "offload", Transport: transport}
}

// validate rejects controls this Host adapter has not mapped to its executors.
// In particular, read-only intent is not a substitute for restricted execution.
func validate(req harness.Request) error {
	if req.NativeProfile != "" || req.PermissionMode != "" || req.ToolsDisabled || req.RestrictedReadOnly ||
		len(req.AllowedTools) > 0 || len(req.MCPServers) > 0 || req.JSONSchema != "" || req.Extraction ||
		req.SkillPluginDir != "" || req.SkillPluginDigest != "" || req.Model != "" {
		return errors.New("Offload does not support per-request native configuration, tool controls or structured extraction")
	}
	if len(req.Images) > 0 || len(req.AdditionalDirectories) > 0 || req.ForkFromNativeSessionID != "" {
		return errors.New("Offload does not support images, additional directories or session forks")
	}
	if req.Resume {
		if req.NativeSessionID == "" || req.NativeSessionProfile == "" {
			return errors.New("Offload continuation requires the recorded native session and canonical profile")
		}
	} else if req.NativeSessionID != "" || req.NativeSessionProfile != "" {
		return errors.New("a fresh Offload invocation cannot adopt a native session binding")
	}
	return nil
}

func (a *Adapter) Run(ctx context.Context, req harness.Request, observe func(harness.Observation) error) (harness.Result, error) {
	if err := validate(req); err != nil {
		return rejected(err)
	}
	if a.client == nil {
		return rejected(errors.New("Offload requires an executor"))
	}
	if err := ctx.Err(); err != nil {
		status, kind := "failed", harness.FailureDeadline
		if errors.Is(err, context.Canceled) {
			status, kind = "canceled", harness.FailureCanceled
		}
		return harness.Result{Status: status, ExitCode: -1, Error: err.Error()}, &harness.ExecutionError{Kind: kind, Cause: err}
	}
	r, err := a.client.Run(ctx, offloadexec.Request{MonitorResumeCommand: req.MonitorResumeCommand, MonitorResumeDirectory: req.MonitorResumeDirectory, CallerRunID: req.CallerRunID,
		Prompt: req.Prompt, Instructions: req.HostInstructions, Workspace: req.Workspace,
		Session: req.NativeSessionID, ResumeProfile: req.NativeSessionProfile, Write: req.WriteIntent})
	status := "failed"
	if r.Status == offloadexec.StatusCanceled {
		status = "canceled"
	} else if r.Status == offloadexec.StatusOK && err == nil {
		status = "completed"
	}
	if err == nil && status != "completed" {
		err = fmt.Errorf("offload returned status %q without an error", r.Status)
	}
	result := harness.Result{Status: status, Text: r.Text, Model: r.Model,
		NativeSessionID: r.Session, ExitCode: r.ExitCode,
		Evidence: &harness.ExecutionEvidence{RunID: r.RunID, CallerRunID: r.CallerRunID,
			Profile: r.Profile, Backend: r.Backend, Status: r.Status, ErrorKind: r.ErrorKind,
			Usage: r.Usage, Raw: r.Raw, HistoryStatus: r.HistoryStatus, HistoryError: r.HistoryError}}
	if err != nil {
		result.Error = err.Error()
		err = &harness.ExecutionError{Kind: failureKind(r.ErrorKind, r.Status, err), Cause: err}
	}
	// This adapter currently consumes final records, not native tool/text events.
	// Raw evidence is deliberately absent from this presentation observation.
	if observe != nil {
		err = errors.Join(err, observe(harness.Observation{Type: "result", Model: result.Model,
			NativeSessionID: result.NativeSessionID, Message: result.Status}))
	}
	return result, err
}

func rejected(err error) (harness.Result, error) {
	return harness.Result{Status: "failed", ExitCode: -1, Error: err.Error()}, &harness.ExecutionError{Kind: harness.FailureStartup, Cause: err}
}

func failureKind(kind, status string, err error) harness.FailureKind {
	switch kind {
	case offloadexec.KindCanceled:
		return harness.FailureCanceled
	case offloadexec.KindDeadline, offloadexec.KindClientTimeout, offloadexec.KindOffloadTimeout:
		return harness.FailureDeadline
	case offloadexec.KindStart, offloadexec.KindInvalidRequest:
		return harness.FailureStartup
	}
	if status == offloadexec.StatusCanceled || errors.Is(err, context.Canceled) {
		return harness.FailureCanceled
	}
	if status == offloadexec.StatusTimeout || errors.Is(err, context.DeadlineExceeded) {
		return harness.FailureDeadline
	}
	return harness.FailureProcess
}
