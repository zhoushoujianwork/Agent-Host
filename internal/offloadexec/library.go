package offloadexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/offload/pkg/execution"
)

const LibraryVersion = "v0.8.10"

// Library snapshots an explicit native profile and calls Offload's public Go
// package. It never starts the Offload CLI or falls back to the CLI client.
type Library struct {
	path        string
	nativeModel string
	backend     string
	client      *execution.Client
	profile     string
	model       string
	timeout     time.Duration
	fingerprint string
}

var _ Executor = (*Library)(nil)

func (*Library) Transport() string     { return "go-package" }
func (c *Library) Fingerprint() string { return c.fingerprint }

// NewLibrary reads only the selected explicit config and snapshots its resolved
// profile. Credential commands and native execution are deferred to Run.
func NewLibrary(path, profile, model string, timeout time.Duration) (*Library, error) {
	if !processGroupsSupported {
		return nil, errors.New("offloadexec: Go package execution requires Unix process-group cleanup")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("offloadexec: config_path must be a clean absolute path")
	}
	if err := checkText("Profile", profile, maxNameLen, false); err != nil {
		return nil, err
	}
	if model != "" {
		if err := checkText("Model", model, maxNameLen, false); err != nil {
			return nil, err
		}
	}
	if timeout <= 0 || timeout > maxTimeout {
		return nil, errors.New("offloadexec: timeout must be positive and at most 24h")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 1<<20 || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("offloadexec: config_path must be a private regular file of at most 1 MiB, not a symlink")
	}
	if owner, ok := before.Sys().(*syscall.Stat_t); !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("offloadexec: config_path must belong to the current user")
	}
	cfg, err := execution.LoadConfig(path, profile)
	if err != nil {
		// Do not expose a native config parser's potentially sensitive input.
		return nil, &RunError{Kind: KindInvalidRequest, Status: StatusError, ExitCode: -1, cause: err,
			msg: "cannot load the explicitly selected Offload configuration/profile"}
	}
	if err := checkText("resolved Profile", cfg.ProfileName, maxNameLen, false); err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("offloadexec: native configuration changed while loading")
	}
	client, err := execution.New(cfg)
	if err != nil {
		return nil, errors.New("offloadexec: invalid selected native execution profile")
	}
	// Public credential fields deliberately do not marshal. Include them only
	// inside this digest input; never persist the config or credentials in events.
	raw, err := json.Marshal(struct {
		Version, Path, Model, Timeout string
		Config                        execution.Config
		Token, TokenCmd               string
	}{LibraryVersion, path, model, timeout.String(), cfg, cfg.Profile.Token, cfg.Profile.TokenCmd})
	if err != nil {
		return nil, errors.New("offloadexec: cannot fingerprint native profile")
	}
	sum := sha256.Sum256(raw)
	return &Library{path: path, nativeModel: cfg.Profile.Model, client: client, backend: cfg.Profile.Backend, profile: cfg.ProfileName, model: model, timeout: timeout, fingerprint: hex.EncodeToString(sum[:])}, nil
}

func (c *Library) ConfigPath() string  { return c.path }
func (c *Library) ProfileName() string { return c.profile }
func (c *Library) Model() string {
	if c.model != "" {
		return c.model
	}
	return c.nativeModel
}

func (c *Library) Run(ctx context.Context, req Request) (Result, error) {
	_, profile, err := validateRequest(req, c.profile)
	if err != nil {
		return fail(KindInvalidRequest, StatusError, -1, err, "invalid Go package request")
	}
	if profile != c.profile {
		return fail(KindInvalidRequest, StatusError, -1, nil, "resume profile differs from the resolved native profile")
	}
	if ctx.Err() != nil {
		return ctxFailure(ctx, ctx)
	}
	// The Host deadline includes admission/setup. Offload owns native timeout
	// and group cleanup; Run waits for that cleanup rather than killing the Host.
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	sdkRequest := execution.Request{Caller: Caller, CallerRunID: req.CallerRunID,
		Task: req.Prompt, Instructions: []byte(req.Instructions), Directory: req.Workspace,
		NativeSessionID: req.Session, Model: c.model, Write: req.Write, Timeout: &c.timeout, RecordHistory: true, Monitor: &execution.MonitorOptions{CaptureTranscript: true, ResumeCommand: req.MonitorResumeCommand, ResumeDirectory: req.MonitorResumeDirectory}}
	if c.backend == "" || c.backend == "claude" {
		sdkRequest.Native = &execution.NativeControls{CallerContext: true, PermissionMode: "bypassPermissions"}
	}
	if len(req.MonitorResumeCommand) > 0 {
		sdkRequest.Monitor.ResumedFrom = execution.MonitorResumeSource()
	}
	r, runErr := c.client.Run(runCtx, sdkRequest)
	result := Result{RunID: r.RunID, CallerRunID: r.CallerRunID, Status: r.Status, Text: r.Report,
		Session: r.NativeSessionID, Profile: r.Profile, Model: r.Model, Backend: r.Backend,
		ExitCode: r.ExitCode, Usage: r.Usage, Raw: r.RawRecord}
	if r.HistoryRecorded {
		result.HistoryStatus = "saved"
	} else if r.HistoryError != nil {
		result.HistoryStatus = "failed"
		// The SDK error can contain a private filesystem path. Preserve it only
		// in private evidence; public events report the independent sync status.
		result.HistoryError = r.HistoryError.Error()
	}
	if len(r.RawRecord) > MaxOutputBytes {
		// Keep the typed outcome but do not persist an oversized raw record.
		result.Raw = nil
		return libraryFailure(result, KindOutputLimit, StatusError, runErr)
	}
	if runErr != nil {
		var executionErr *execution.RunError
		kind, status := KindOffloadError, StatusError
		switch {
		case errors.Is(runErr, context.Canceled):
			kind, status = KindCanceled, StatusCanceled
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			kind, status = KindDeadline, StatusTimeout
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			kind, status = KindClientTimeout, StatusTimeout
		case errors.As(runErr, &executionErr):
			switch executionErr.Kind {
			case "configuration", "globally_disabled", "unsupported_tools", "launch":
				kind = KindStart
			case "timeout":
				kind, status = KindOffloadTimeout, StatusTimeout
			case "canceled":
				kind, status = KindCanceled, StatusCanceled
			}
		}
		return libraryFailure(result, kind, status, runErr)
	}
	if r.Status != StatusOK || r.ExitCode != 0 || len(r.RawRecord) == 0 || !json.Valid(r.RawRecord) ||
		r.Profile != c.profile || r.NativeSessionID == "" || cleanName(&r.NativeSessionID) != r.NativeSessionID ||
		r.CallerRunID != req.CallerRunID || req.Session != "" && r.NativeSessionID != req.Session {
		return libraryFailure(result, KindInvalidOutput, StatusError, nil)
	}
	return result, nil
}

func libraryFailure(result Result, kind, status string, cause error) (Result, error) {
	result.Status, result.ErrorKind = status, kind
	return result, &RunError{Kind: kind, Status: status, ExitCode: result.ExitCode, cause: cause,
		msg: fmt.Sprintf("Go package execution failed (%s)", kind)}
}
