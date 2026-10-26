package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

const maxResponseBytes = 4 << 20

// Output is private transport evidence; callers must not log either stream.
type Output struct {
	Stdout, Stderr []byte
	Started        bool
}

// Transport executes official CLI argv. Set Output.Started once a command could
// have performed a side effect, including when returning a process error.
type Transport interface {
	Run(context.Context, ...string) (Output, error)
}

// CLITransport runs lark-cli without a shell. Calls have a 30-second deadline
// and bounded output; cancellation stops the process group on Unix.
type CLITransport struct{ Binary string }

func (c CLITransport) Run(ctx context.Context, args ...string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	binary := c.Binary
	if binary == "" {
		binary = "lark-cli"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.WaitDelay = 2 * time.Second
	if err := bindCLIProcessGroup(cmd); err != nil {
		return Output{}, err
	}
	stdout, stderr := limitedBuffer{limit: maxResponseBytes}, limitedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Start()
	if err != nil {
		return Output{}, errors.New("lark-cli could not start")
	}
	defer stopCLIProcessGroup(cmd)
	err = cmd.Wait()
	output := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Started: true}
	if stdout.overflow || stderr.overflow {
		return output, errors.New("lark-cli output exceeded its limit")
	}
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	if err != nil {
		return output, errors.New("lark-cli exited unsuccessfully")
	}
	return output, nil
}

type limitedBuffer struct {
	// Embedding bytes.Buffer promotes ReadFrom, allowing io.Copy to bypass Write.
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

type envelope struct {
	OK    *bool           `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	} `json:"error"`
}

func decodeEnvelope(out Output) (envelope, error) {
	for _, raw := range [][]byte{out.Stdout, out.Stderr} {
		var e envelope
		if len(raw) == 0 || len(raw) > maxResponseBytes {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if decoder.Decode(&e) != nil || e.OK == nil {
			continue
		}
		if decoder.Decode(new(any)) != io.EOF {
			continue
		}
		return e, nil
	}
	return envelope{}, errors.New("lark-cli returned no valid result envelope")
}

func singleJSON(raw []byte, value any) bool {
	if len(raw) == 0 || len(raw) > maxResponseBytes {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	return d.Decode(value) == nil && d.Decode(new(any)) == io.EOF
}
