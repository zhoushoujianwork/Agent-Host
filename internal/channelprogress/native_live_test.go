package channelprogress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/harness/nativepolicy"
	"agent-host/internal/offloadexec"
)

// This opt-in check exercises the outer Host-managed native loop with a real
// model and fixture state. The feedback worker records acceptance locally; no
// platform reply adapter, real machine or chat destination is involved.
func TestProgressStageNativeLive(t *testing.T) {
	if os.Getenv("AGH_PROGRESS_NATIVE_LIVE") != "1" {
		t.Skip("explicit isolated native acceptance only")
	}
	executable := os.Getenv("AGH_PROGRESS_NATIVE_EXECUTABLE")
	if executable == "" {
		t.Fatal("a freshly built agent-host executable is required")
	}
	s, binding := fixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	worker := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			p, _, err := claimProgress(ctx, s)
			if err == nil {
				if p.Text != "检查资源" {
					worker <- errors.New("model chose a different stage for the resource-check fixture")
					return
				}
				worker <- s.FinishChannelReaction(ctx, p.ReactionID, "accepted", "")
				return
			}
			if !errors.Is(err, sql.ErrNoRows) {
				worker <- err
				return
			}
			select {
			case <-ctx.Done():
				worker <- ctx.Err()
				return
			case <-ticker.C:
			}
		}
	}()
	library, err := offloadexec.DefaultLibrary("", 2*time.Minute)
	if err != nil {
		cancel()
		<-worker
		t.Fatal(err)
	}
	adapter := nativepolicy.NewWithLibrary("", library)
	calls, results := 0, 0
	chosenStage := ""
	request := harness.Request{
		CallerRunID: "agh:progress-stage-acceptance", Workspace: s.Home(), PermissionMode: "bypassPermissions",
		HostInstructions: Instructions + "\nThis is an isolated status-only acceptance. Only call the mounted channel_progress tool. Do not run shell commands, read files, inspect real resources or delegate.",
		Prompt:           "A synthetic task has finished checking permission and is now entering its VM resource-check stage. Report that current stage with the attached progress tool, then reply exactly PROGRESS_STAGE_OK. This is only a fixture; do not inspect a real machine.",
		MCPServers:       []harness.MCPServer{binding.Server(executable)},
	}
	result, err := adapter.Run(ctx, request, func(o harness.Observation) error {
		if o.Tool == nil {
			return nil
		}
		if !strings.Contains(o.Tool.Name, "channel_progress") {
			return errors.New("acceptance attempted a non-fixture tool")
		}
		if o.Tool.Stage == "call" {
			var args struct {
				Stage string `json:"stage"`
				Text  string `json:"text"`
			}
			if json.Unmarshal(o.Tool.Input, &args) != nil || args.Stage != "checking_resources" || args.Text != "" {
				return errors.New("native model did not use the advertised stage contract")
			}
			chosenStage = args.Stage
			calls++
		}
		if o.Tool.Stage == "result" {
			if o.Tool.IsError {
				return errors.New("native progress call returned an error")
			}
			results++
		}
		return nil
	})
	cancel()
	workerErr := <-worker
	if err != nil || workerErr != nil || result.Status != "completed" || !strings.Contains(result.Text, "PROGRESS_STAGE_OK") || calls != 1 || results != 1 {
		t.Fatalf("native status=%s model=%s calls=%d results=%d error=%v worker=%v", result.Status, result.Model, calls, results, err, workerErr)
	}
	t.Logf("model=%s profile=%s backend=%s stage=%s label=检查资源 calls=%d results=%d platform_sends=0", result.Model, result.Evidence.Profile, result.Evidence.Backend, chosenStage, calls, results)
}
