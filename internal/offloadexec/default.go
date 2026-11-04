package offloadexec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/zhoushoujianwork/offload/pkg/execution"
)

const Caller = "agh"

// DefaultLibrary resolves the owner's configured Offload default, not a guessed
// provider. It reads no credentials into logs and does not start native work.
func DefaultLibrary(model string, timeout time.Duration) (*Library, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(home, ".config")
	}
	path := filepath.Join(root, "offload", "config.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, errors.New("configure a private Offload config.json and default profile before execution")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read the Offload default configuration")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("Offload configuration changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("cannot read the bounded Offload default configuration")
	}
	var config struct {
		Default string `json:"default"`
	}
	if json.Unmarshal(raw, &config) != nil || config.Default == "" {
		return nil, errors.New("Offload configuration requires an explicit default profile")
	}
	client, err := NewLibrary(path, config.Default, model, timeout)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("Offload default configuration changed while loading")
	}
	return client, nil
}

// RunNative is the same package execution boundary for full Host requests.
// Attribution is owned by AGH, never copied from source content or native output.
func (c *Library) RunNative(ctx context.Context, req execution.Request) (execution.Result, error) {
	req.Caller = Caller
	if req.CallerRunID == "" {
		req.CallerRunID = "agh:internal:" + uuid.NewString()
	}
	req.RecordHistory = true
	if req.Monitor == nil {
		req.Monitor = &execution.MonitorOptions{CaptureTranscript: true}
	}
	if req.Model == "" {
		req.Model = c.model
	}
	if req.Timeout == nil {
		req.Timeout = &c.timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.client.Run(runCtx, req)
}

func (c *Library) Backend() string { return c.backend }
