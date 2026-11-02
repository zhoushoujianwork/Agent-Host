package offloadexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusTimeout  = "timeout"
	StatusCanceled = "canceled"
)

// Result.ErrorKind values.
const (
	KindInvalidRequest = "invalid_request"
	KindStart          = "start_failed"
	KindOffloadError   = "offload_error"
	KindOffloadTimeout = "offload_timeout"
	KindInvalidOutput  = "invalid_output"
	KindOutputLimit    = "output_limit"
	KindCanceled       = "canceled"
	KindDeadline       = "context_deadline"
	KindClientTimeout  = "client_timeout"
	KindExit           = "exit_failure"
	KindPipeHeld       = "output_held_open"
)

// Request describes one Offload run.
type Request struct {
	// CallerRunID optionally correlates this invocation with its owning task.
	// It is metadata, never a native session or a permission grant.
	CallerRunID string
	// Prompt is the task text. It is sent on stdin, never argv.
	Prompt string
	// Instructions is optional internal context sent through a private file.
	// It is never concatenated with the visible task on stdin.
	Instructions string
	// Workspace is an absolute path to an existing directory (-d).
	Workspace string
	// Session resumes an earlier Offload session (-r) when non-empty.
	Session string
	// ResumeProfile is the canonical Profile returned by the prior run of
	// Session. It is allowed only with Session and replaces Config.Profile.
	ResumeProfile string
	// Write sets the task write intent (-w). Offload adds no sandbox.
	Write bool
}

// Result retains typed package execution and private raw evidence.
type Result struct {
	RunID         string
	CallerRunID   string
	Status        string
	Text          string
	Session       string
	Profile       string
	Model         string
	Backend       string
	ErrorKind     string
	ExitCode      int
	Usage         json.RawMessage
	Raw           json.RawMessage
	HistoryStatus string
	HistoryError  string
}

// RunError describes a failed run. Its message never contains stderr, stdout
// or the Offload result text.
type RunError struct {
	Kind     string
	Status   string
	ExitCode int
	msg      string
	cause    error
}

func (e *RunError) Error() string { return "offloadexec: " + e.msg }
func (e *RunError) Unwrap() error { return e.cause }

// Executor is the single package execution boundary, injectable in tests.
type Executor interface {
	Run(context.Context, Request) (Result, error)
}

func fail(kind, status string, code int, cause error, format string, args ...any) (Result, error) {
	res := Result{Status: status, ErrorKind: kind, ExitCode: code}
	return res, &RunError{Kind: kind, Status: status, ExitCode: code, msg: fmt.Sprintf(format, args...), cause: cause}
}

func validateRequest(req Request, configuredProfile string) (stdin []byte, profile string, err error) {
	if req.CallerRunID != "" && !correlationIDPattern.MatchString(req.CallerRunID) {
		return nil, "", errors.New("offloadexec: invalid CallerRunID")
	}
	if err := checkText("Prompt", req.Prompt, MaxInputBytes, true); err != nil {
		return nil, "", err
	}
	if len(req.Instructions) > 0 {
		if err := checkText("Instructions", req.Instructions, MaxInstructionsBytes, true); err != nil {
			return nil, "", err
		}
	}
	if err := checkText("Workspace", req.Workspace, maxPathLen, false); err != nil {
		return nil, "", err
	}
	if !filepath.IsAbs(req.Workspace) {
		return nil, "", errors.New("offloadexec: Workspace must be an absolute path")
	}
	fi, statErr := os.Stat(req.Workspace)
	if statErr != nil || !fi.IsDir() {
		return nil, "", errors.New("offloadexec: Workspace must be an existing directory")
	}
	profile = configuredProfile
	if req.Session == "" {
		if req.ResumeProfile != "" {
			return nil, "", errors.New("offloadexec: ResumeProfile requires Session")
		}
	} else {
		if err := checkText("Session", req.Session, maxNameLen, false); err != nil {
			return nil, "", err
		}
		if req.ResumeProfile != "" {
			if err := checkText("ResumeProfile", req.ResumeProfile, maxNameLen, false); err != nil {
				return nil, "", err
			}
			profile = req.ResumeProfile
		}
	}
	stdin = []byte(req.Prompt)
	if len(stdin)+len(req.Instructions) > MaxInputBytes {
		return nil, "", fmt.Errorf("offloadexec: combined input exceeds %d bytes", MaxInputBytes)
	}
	return stdin, profile, nil
}

func ctxFailure(parent, run context.Context) (Result, error) {
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return fail(KindCanceled, StatusCanceled, -1, errors.Join(context.Canceled, context.Cause(parent)), "run canceled")
	case errors.Is(parent.Err(), context.DeadlineExceeded):
		return fail(KindDeadline, StatusTimeout, -1, errors.Join(context.DeadlineExceeded, context.Cause(parent)), "context deadline exceeded")
	default:
		return fail(KindClientTimeout, StatusTimeout, -1, errors.Join(context.DeadlineExceeded, context.Cause(run)), "offload did not finish within the configured timeout")
	}
}

func cleanName(s *string) string {
	if s == nil || *s == "" {
		return ""
	}
	if checkText("value", *s, maxNameLen, false) != nil {
		return ""
	}
	return *s
}
