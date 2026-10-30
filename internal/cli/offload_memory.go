package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
)

// Recovery opens only the selected partition and never launches native work.
// The channel service must release its writer lock before an offline retry.
func retryOffloadMemory(ctx context.Context, o options, id string, out io.Writer) (runErr error) {
	if o.offloadMemory == nil || o.offloadMemory.Decider == nil {
		return errors.New("Offload memory processing is not enabled")
	}
	release, err := state.Acquire(o.home)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, release()) }()
	store, err := state.Open(o.home, false)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	store = store.WithPartition(o.agentID, o.audienceID)
	run, err := store.Run(ctx, id)
	if err != nil {
		return err
	}
	if run.OffloadMemoryMode != state.OffloadMemoryProcess {
		return errors.New("run was not created with Offload memory processing")
	}
	h := &offloadchannel.Host{Store: store, Memory: o.offloadMemory}
	result, processErr := h.RetryMemory(ctx, id)
	return errors.Join(processErr, json.NewEncoder(out).Encode(result))
}
