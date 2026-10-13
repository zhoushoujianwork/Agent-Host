package dingtalkworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"agent-host/internal/mcpstdio"
)

type Runner func(context.Context, Request) (json.RawMessage, error)

type CommandFailure struct{ Code int }

func (e *CommandFailure) Error() string {
	return fmt.Sprintf("official DingTalk command failed (exit %d); inspect its result and platform permissions", e.Code)
}

func Process(executable string) Runner {
	return ProcessWithLimit(executable, 1<<20)
}

// ProcessWithLimit supplies the separately bounded selected-source collector.
func ProcessWithLimit(executable string, limit int) Runner {
	return func(ctx context.Context, request Request) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, WorkerFlag)
		cmd.WaitDelay = 2 * time.Second
		cmd.Stdin = bytes.NewReader(encodeRequest(request))
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "DWS_") {
				cmd.Env = append(cmd.Env, item)
			}
		}
		out, stderr := &boundedOutput{limit: limit}, &boundedOutput{limit: 64 << 10}
		cmd.Stdout, cmd.Stderr = out, stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil, errors.New("DingTalk worker interrupted; execution outcome may be unknown")
		}
		if out.exceeded || stderr.exceeded {
			return nil, errors.New("DingTalk worker output exceeded its limit; execution outcome may be unknown")
		}
		if err != nil {
			// Keep the official structured failure when present; never forward raw
			// stderr, which may include paths, credentials or arbitrary source text.
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				if json.Valid(out.Bytes()) {
					return out.Bytes(), &CommandFailure{Code: exit.ExitCode()}
				}
				return nil, fmt.Errorf("official DingTalk command failed (exit %d); check login and platform permissions", exit.ExitCode())
			}
			return nil, errors.New("cannot start embedded DingTalk worker")
		}
		var value any
		if mcpstdio.Decode(out.Bytes(), &value) != nil {
			return nil, errors.New("official DingTalk worker returned invalid JSON")
		}
		return out.Bytes(), nil
	}
}

type boundedOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// Login inherits terminal streams for the official browser/device-flow prompt.
// Business calls always use Process and never inherit terminal stdin.
func Login(ctx context.Context, executable, path, fp string, device bool, out, errout io.Writer) error {
	args := []string{"auth", "login"}
	if device {
		args = append(args, "--device")
	}
	cmd := exec.CommandContext(ctx, executable, WorkerFlag)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "DWS_") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	// The worker consumes only the request. Login never needs arbitrary commands
	// from terminal stdin; official authorization continues in the browser.
	cmd.Stdin = bytes.NewReader(encodeRequest(Request{Config: path, Fingerprint: fp, Args: args}))
	cmd.Stdout, cmd.Stderr = out, errout
	if err := cmd.Run(); err != nil {
		return errors.New("DingTalk login failed; inspect the official authorization result")
	}
	c, current, err := Load(path)
	if err != nil || current != fp {
		return errors.New("DingTalk configuration changed during login")
	}
	return verifyIdentity(ctx, Process(executable), Request{Config: path, Fingerprint: fp}, c)
}
