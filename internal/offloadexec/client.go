package offloadexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Result statuses. ok, error and timeout are Offload's own vocabulary.
// StatusCanceled is derived by the client when the caller's context is
// canceled.
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

const (
	// Offload stops its own worker, which runs in a separate process group,
	// with a stop grace and a kill wait, then repeats the group cleanup with
	// the same bounds: up to about 16s in total. SIGKILL to the wrapper before
	// that finishes would orphan a stubborn native worker, so the TERM grace
	// must outlast Offload's cleanup before the wrapper is forced.
	defaultTermGrace = 30 * time.Second
	// The client-side deadline is Config.Timeout plus this wait, which covers
	// Offload's own timeout handling and cleanup after its -t deadline.
	defaultWaitGrace = 30 * time.Second
	pipeDrainDelay   = 2 * time.Second
)

// Request describes one Offload run.
type Request struct {
	// CallerRunID optionally correlates this invocation with its owning task.
	// It is metadata, never a native session or a permission grant.
	CallerRunID string
	// Prompt is the task text. It is sent on stdin, never argv.
	Prompt string
	// Instructions is optional text sent before Prompt on stdin.
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

// Result is the outcome of one run. ExitCode is -1 when the process produced
// no exit status. Raw is the validated stdout record. It echoes the task text
// and must be treated as sensitive evidence.
type Result struct {
	RunID       string
	CallerRunID string
	Status      string
	Text        string
	Session     string
	Profile     string
	Model       string
	Backend     string
	ErrorKind   string
	ExitCode    int
	Usage       json.RawMessage
	Raw         json.RawMessage
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

// Client runs the Offload CLI. It is safe for concurrent use.
type Client struct {
	cfg       Config
	termGrace time.Duration
	waitGrace time.Duration
}

// New validates cfg and returns a Client. It does not touch the filesystem.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, termGrace: defaultTermGrace, waitGrace: defaultWaitGrace}, nil
}

func fail(kind, status string, code int, cause error, format string, args ...any) (Result, error) {
	res := Result{Status: status, ErrorKind: kind, ExitCode: code}
	return res, &RunError{Kind: kind, Status: status, ExitCode: code, msg: fmt.Sprintf(format, args...), cause: cause}
}

func (c *Client) validateRequest(req Request) (stdin []byte, profile string, err error) {
	if req.CallerRunID != "" && !correlationIDPattern.MatchString(req.CallerRunID) {
		return nil, "", errors.New("offloadexec: invalid CallerRunID")
	}
	if err := checkText("Prompt", req.Prompt, MaxInputBytes, true); err != nil {
		return nil, "", err
	}
	if len(req.Instructions) > 0 {
		if err := checkText("Instructions", req.Instructions, MaxInputBytes, true); err != nil {
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
	profile = c.cfg.Profile
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
	if req.Instructions != "" {
		stdin = []byte(req.Instructions + "\n\n" + req.Prompt)
	} else {
		stdin = []byte(req.Prompt)
	}
	// The limit applies to the bytes actually sent, including the separator.
	if len(stdin) > MaxInputBytes {
		return nil, "", fmt.Errorf("offloadexec: stdin input exceeds %d bytes", MaxInputBytes)
	}
	return stdin, profile, nil
}

func (c *Client) args(req Request, profile string) []string {
	a := []string{"-json", "-no-salvage", "-p", profile, "-d", filepath.Clean(req.Workspace), "-t", c.cfg.Timeout.String()}
	if req.CallerRunID != "" {
		a = append(a, "--caller-run-id", req.CallerRunID)
	}
	if c.cfg.Model != "" {
		a = append(a, "-m", c.cfg.Model)
	}
	if req.Write {
		a = append(a, "-w")
	}
	if req.Session != "" {
		a = append(a, "-r", req.Session)
	}
	return a
}

// limitWriter keeps up to max bytes (when keep is set), counts overflow and
// keeps draining so the child never blocks on a full pipe.
type limitWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	keep     bool
	max      int
	n        int
	overflow chan struct{}
	tripped  bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tripped {
		return len(p), nil
	}
	if w.n+len(p) > w.max {
		w.tripped = true
		close(w.overflow)
		return len(p), nil
	}
	w.n += len(p)
	if w.keep {
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *limitWriter) exceeded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tripped
}

// Run executes Offload once. There are no retries, replays or fallbacks.
// On failure it returns a populated Result and a *RunError.
func (c *Client) Run(ctx context.Context, req Request) (Result, error) {
	stdin, profile, err := c.validateRequest(req)
	if err != nil {
		return fail(KindInvalidRequest, StatusError, -1, nil, "%s", trimPrefix(err))
	}
	if err := ctx.Err(); err != nil {
		return ctxFailure(ctx, ctx)
	}

	runCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout+c.waitGrace)
	defer cancel()

	cmd := exec.Command(c.cfg.Command, c.args(req, profile)...)
	configureProcess(cmd)
	overflow := make(chan struct{})
	stdout := &limitWriter{keep: true, max: MaxOutputBytes, overflow: overflow}
	stderr := &limitWriter{max: MaxOutputBytes, overflow: make(chan struct{})}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = pipeDrainDelay

	if err := cmd.Start(); err != nil {
		return fail(KindStart, StatusError, -1, err, "cannot start offload command: %v", errors.Unwrap(err))
	}

	done := make(chan struct{})
	var cause string // set by the watcher before it signals the group
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		select {
		case <-done:
			return
		case <-runCtx.Done():
			cause = "context"
		case <-stdout.overflow:
			cause = "overflow"
		case <-stderr.overflow:
			cause = "overflow"
		}
		_ = terminateGroup(cmd.Process)
		t := time.NewTimer(c.termGrace)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
			killGroup(cmd.Process)
		}
	}()

	waitErr := cmd.Wait()
	close(done)
	watcher.Wait()
	// Reap descendants that outlived the direct child.
	killGroup(cmd.Process)

	switch {
	case cause == "overflow" || stdout.exceeded() || stderr.exceeded():
		return fail(KindOutputLimit, StatusError, -1, nil, "offload output exceeded %d bytes", MaxOutputBytes)
	case cause == "context":
		return ctxFailure(ctx, runCtx)
	}

	exitCode := cmd.ProcessState.ExitCode()
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		return fail(KindPipeHeld, StatusError, exitCode, nil, "a descendant process kept offload output open")
	}
	return c.interpret(req, profile, stdout.buf.Bytes(), exitCode, waitErr)
}

func trimPrefix(err error) string {
	const p = "offloadexec: "
	s := err.Error()
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

// ctxFailure maps a stopped run to a result. parent is the caller's context
// and run is the derived context that also carries the client-side limit.
func ctxFailure(parent, run context.Context) (Result, error) {
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return fail(KindCanceled, StatusCanceled, -1, context.Canceled, "run canceled")
	case errors.Is(parent.Err(), context.DeadlineExceeded):
		return fail(KindDeadline, StatusTimeout, -1, context.DeadlineExceeded, "context deadline exceeded")
	default:
		return fail(KindClientTimeout, StatusTimeout, -1, context.DeadlineExceeded, "offload did not finish within the configured timeout")
	}
}

func (c *Client) interpret(req Request, profile string, out []byte, exitCode int, waitErr error) (Result, error) {
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return fail(KindExit, StatusError, exitCode, nil, "waiting for offload failed")
	}
	rec, perr := parseRecord(out)
	if perr != nil {
		if exitErr != nil {
			return fail(KindExit, StatusError, exitCode, nil, "offload exited with code %d without a valid result", exitCode)
		}
		return fail(KindInvalidOutput, StatusError, exitCode, nil, "invalid offload output: %v", perr)
	}
	raw := json.RawMessage(bytes.TrimSpace(out))
	invalid := func(format string, args ...any) (Result, error) {
		// The record parsed but violates the contract; keep it as evidence.
		res, err := fail(KindInvalidOutput, StatusError, exitCode, nil, format, args...)
		res.Raw = raw
		return res, err
	}
	if rec.Status == nil || rec.ExitCode == nil {
		return invalid("offload result lacks status or exit_code")
	}
	if *rec.ExitCode != exitCode {
		return invalid("offload exit_code %d differs from process exit code %d", *rec.ExitCode, exitCode)
	}
	status := *rec.Status
	switch status {
	case StatusOK:
		if exitCode != 0 {
			return invalid("offload reported status ok with exit code %d", exitCode)
		}
	case StatusError, StatusTimeout:
		if exitCode == 0 {
			return invalid("offload reported status %s with exit code 0", status)
		}
	default:
		return invalid("offload reported unknown status")
	}

	for _, id := range []*string{rec.RunID, rec.CallerRunID} {
		if id != nil && !correlationIDPattern.MatchString(*id) {
			return invalid("offload result has invalid correlation identity")
		}
	}
	if req.CallerRunID != "" && rec.CallerRunID != nil && *rec.CallerRunID != req.CallerRunID {
		return invalid("offload result caller_run_id differs from request")
	}
	res := Result{
		Status:   status,
		Text:     rec.Result,
		Session:  cleanName(rec.Session),
		Profile:  cleanName(rec.Profile),
		Model:    cleanName(&rec.Model),
		Backend:  cleanName(&rec.Backend),
		ExitCode: exitCode,
		Usage:    rec.Usage,
		Raw:      raw,
	}
	if rec.RunID != nil {
		res.RunID = *rec.RunID
	}
	if rec.CallerRunID != nil {
		res.CallerRunID = *rec.CallerRunID
	}
	if status != StatusOK {
		res.ErrorKind = KindOffloadError
		if status == StatusTimeout {
			res.ErrorKind = KindOffloadTimeout
		}
		return res, &RunError{Kind: res.ErrorKind, Status: status, ExitCode: exitCode,
			msg: fmt.Sprintf("offload reported status %s (exit code %d)", status, exitCode)}
	}

	// A successful result needs a complete, bounded identity.
	if rec.Profile == nil || rec.Session == nil || res.Profile == "" || res.Session == "" ||
		res.Profile != *rec.Profile || res.Session != *rec.Session {
		return invalidResult(res, exitCode, "offload result lacks a valid profile or session")
	}
	if req.Session != "" {
		if res.Session != req.Session {
			return invalidResult(res, exitCode, "resume returned a different session")
		}
		if res.Profile != profile {
			return invalidResult(res, exitCode, "resume returned a different profile")
		}
	}
	return res, nil
}

func invalidResult(res Result, code int, msg string) (Result, error) {
	res.Status = StatusError
	res.ErrorKind = KindInvalidOutput
	res.Text, res.Session, res.Profile = "", "", ""
	return res, &RunError{Kind: KindInvalidOutput, Status: StatusError, ExitCode: code, msg: msg}
}

// cleanName returns s when it is a bounded printable identifier, else "".
func cleanName(s *string) string {
	if s == nil || *s == "" {
		return ""
	}
	if checkText("value", *s, maxNameLen, false) != nil {
		return ""
	}
	return *s
}

var _ io.Writer = (*limitWriter)(nil)
