// Package approval queues a bounded, group-shareable proposal for Host review.
// It cannot grant approval, identify an owner, send messages, or execute work.
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-host/internal/harness"
	"agent-host/internal/mcpstdio"
)

// Proposal describes one requested action without credentials or executable data.
type Proposal struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Impact    string `json:"impact"`
	Shareable bool   `json:"shareable"`
}

// Validate rejects incomplete proposals and text unsafe for a shared chat message.
// Shareable is an assertion by the caller; Host must still apply audience rules.
func (p Proposal) Validate() error {
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"operation", p.Operation, 256}, {"target", p.Target, 512}, {"impact", p.Impact, 1500},
	} {
		if !utf8.ValidString(field.value) || strings.TrimSpace(field.value) == "" || len(field.value) > field.limit {
			return fmt.Errorf("%s must contain nonempty valid UTF-8 text of at most %d bytes", field.name, field.limit)
		}
		for _, r := range field.value {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
				return fmt.Errorf("%s must not contain control or formatting characters", field.name)
			}
		}
	}
	if !p.Shareable {
		return errors.New("proposal must be safe to share in the current group; private details are not permitted")
	}
	return nil
}

// Server supplies the only approval capability available to a restricted run.
// Host owns and validates directory before the subprocess is started.
func Server(executable, directory string) harness.MCPServer {
	return harness.MCPServer{Name: "approval", Command: executable, Args: []string{"approval-mcp", "--directory", directory}}
}

func validateDirectory(directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("approval directory must be absolute")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("approval directory must be an existing private 0700 directory, not a symlink")
	}
	return nil
}

// Read returns a queued proposal. An absent proposal returns os.ErrNotExist.
// It never follows a proposal symlink or accepts a partial/oversized contract.
func Read(directory string) (Proposal, error) {
	var p Proposal
	if err := validateDirectory(directory); err != nil {
		return p, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return p, err
	}
	defer root.Close()
	info, err := root.Lstat("proposal.json")
	if err != nil {
		return p, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return p, errors.New("approval proposal must be a private regular file of at most 4 KiB")
	}
	f, err := root.Open("proposal.json")
	if err != nil {
		return p, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return p, err
	}
	if len(data) > 4096 {
		return p, errors.New("approval proposal exceeds 4 KiB")
	}
	if err := mcpstdio.Decode(data, &p); err != nil {
		return p, errors.New("invalid approval proposal JSON")
	}
	return p, p.Validate()
}

func queue(directory string, p Proposal) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := validateDirectory(directory); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return errors.New("encoded approval proposal exceeds 4 KiB")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile("proposal.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := Read(directory)
		if readErr != nil {
			return readErr
		}
		if existing == p {
			return nil
		}
		return errors.New("this run already queued a different proposal")
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// Serve exposes only request_approval. Host delivers the owner mention after the
// run completes; successful tool output means queued, never approved or notified.
func Serve(ctx context.Context, directory string, in io.Reader, out io.Writer) error {
	if err := validateDirectory(directory); err != nil {
		return err
	}
	tool := mcpstdio.Tool{
		Name:        "request_approval",
		Description: "Queue one concrete action, exact target and impact for the configured owner to review (limits: 256/512/1500 UTF-8 bytes respectively). All text must be safe for the current shared chat. This does not notify the owner or authorize execution: Host delivers the request after this run. Repeat the identical proposal safely; changing it requires a new run. Never include private context, credentials, commands, paths for output files, or owner IDs as extra fields.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"operation", "target", "impact", "shareable"}, "properties": map[string]any{
			"operation": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			"target":    map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
			"impact":    map[string]any{"type": "string", "minLength": 1, "maxLength": 1500},
			"shareable": map[string]any{"type": "boolean", "const": true},
		}},
		Annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
	return mcpstdio.Serve(ctx, "approval", []mcpstdio.Tool{tool}, func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var p Proposal
		if err := mcpstdio.Decode(args, &p); err != nil {
			return nil, errors.New("invalid approval arguments")
		}
		if err := queue(directory, p); err != nil {
			return nil, err
		}
		return map[string]any{"status": "queued", "proposal": p, "owner_notified": false, "message": "Proposal queued. The owner has not yet been notified; Host delivers the request after this run. No execution is authorized."}, nil
	}, in, out)
}
