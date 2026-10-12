package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
)

func TestFailureReplyCategorySurvivesHostAndKeepsLocalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, status, category string
		kind                   harness.FailureKind
		group                  bool
	}{
		{"owner provider", "failed", "模型服务未能完成", harness.FailureProvider, false},
		{"group process", "failed", "进程异常退出", harness.FailureProcess, true},
		{"group timeout", "canceled", "处理已超时", harness.FailureDeadline, true},
		{"owner cancel", "canceled", "请求已取消", harness.FailureCanceled, false},
		{"group unknown", "unknown", "结果未确认", harness.FailureUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConcurrentFixture(t)
			const diagnostic = "private-error /Users/private/work token=secret"
			f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
				cause := &harness.ExecutionError{Kind: tc.kind, Cause: errors.New(diagnostic)}
				return harness.Result{Status: tc.status, Text: diagnostic, Error: diagnostic, NativeSessionID: req.NativeSessionID}, cause
			}
			input := f.message(t, "failed", "Do this task", "text", "owner-dm")
			if tc.group {
				input = groupInput(t, f.serviceFixture, "failed", "alice", "group-a", "@Helper Do this task")
			}
			message := f.process(t, input)
			run, err := f.service.Store.WithPartition(message.AgentID, message.AudienceID).Run(context.Background(), message.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if message.ReplyStatus != "accepted" || !strings.Contains(message.ReplyText, tc.category) || !strings.Contains(message.ReplyText, "部分操作完成") || !strings.Contains(message.ReplyText, "旧任务没有自动重做") {
				t.Fatalf("category or outcome lost: message=%+v run=%+v", message, run)
			}
			if strings.Contains(message.ReplyText, "private-error") || strings.Contains(message.ReplyText, "token=secret") || strings.Contains(message.ReplyText, "/retry") {
				t.Fatal("private diagnostic or unsupported command displayed", message.ReplyText)
			}
			if run.ExecutionStatus != tc.status || run.Output != diagnostic || !strings.Contains(run.Error, diagnostic) || len(f.runner.snapshot()) != 1 {
				t.Fatalf("stored outcome changed or execution repeated: %+v", run)
			}
			finished := false
			for _, e := range run.Events {
				finished = finished || e.Type == "run.finished"
			}
			if !finished {
				t.Fatal("terminal evidence missing", run)
			}
		})
	}
}

func TestTimedOutTurnRetainsTerminalEvidenceAndRecoversWithoutReplay(t *testing.T) {
	f := newServiceFixture(t)
	f.service.Timeout = time.Second
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		<-ctx.Done()
		return harness.Result{Status: "canceled", Error: ctx.Err().Error(), NativeSessionID: req.NativeSessionID}, ctx.Err()
	}
	first := f.process(t, f.message(t, "timeout", "Task that reaches its deadline", "text", "owner-dm"))
	if first.RunID == "" {
		t.Fatalf("fixture did not reach native execution: %+v", first)
	}
	run := f.run(t, first)
	if first.ReplyStatus != "accepted" || run.ExecutionStatus != "canceled" || !strings.Contains(first.ReplyText, "处理已超时") || !strings.Contains(run.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("deadline outcome lost: message=%+v run=%+v", first, run)
	}
	for _, kind := range []string{"agent.result", "phase.finished", "run.finished"} {
		found := false
		for _, e := range run.Events {
			found = found || e.Type == kind
		}
		if !found {
			t.Fatalf("canceled execution context prevented %s persistence", kind)
		}
	}
	f.runner.run = nil
	f.service.Timeout = time.Minute
	next := f.process(t, f.message(t, "next", "What happened in the previous task?", "text", "owner-dm"))
	if next.Status != "completed" || next.SessionID == first.SessionID || len(f.runner.snapshot()) != 2 || f.runner.snapshot()[1].Resume || !strings.Contains(next.ReplyText, "旧任务没有自动重做") {
		t.Fatal("deadline recovery replayed or retained the failed session", next)
	}
	old := f.run(t, first)
	if old.ExecutionStatus != run.ExecutionStatus || old.Error != run.Error || len(old.Events) != len(run.Events) {
		t.Fatal("recovery changed original outcome", old)
	}
}
