package channel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/harness"
)

const progressReplyWarning = "进度提示未能完整记录，本轮结果见下方。"

func TestProgressRecordWarningPreservesCompletedAnswerAndEvidence(t *testing.T) {
	run := formattedRun()
	const sourceError = "persist event phase.progress: /Users/private/state.db token=private-credential"
	run.Events[0].Data["progress_error"] = sourceError
	run.Events[1].Data["progress_error"] = sourceError
	before, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(run, nil)
	if warning, body := strings.Index(text, progressReplyWarning), strings.Index(text, run.Output); warning < 0 || warning >= body || strings.Count(text, progressReplyWarning) != 1 {
		t.Fatal("progress warning missing, repeated, or hidden behind the answer", text)
	}
	if !strings.HasSuffix(text, "\n\n---\n⏱ 执行 2.5 s · 🤖 native-actual-model") {
		t.Fatal("progress warning removed measured runtime metadata", text)
	}
	for _, hidden := range []string{sourceError, "/Users/private", "private-credential", "任务执行保存失败", "后续保存步骤失败"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("progress warning disclosed diagnostics or changed the outcome: %q in %q", hidden, text)
		}
	}
	after, err := json.Marshal(run)
	if err != nil || string(before) != string(after) {
		t.Fatal("progress warning changed source evidence", err)
	}
}

func TestProgressRecordWarningKeepsNativeFailureExplanation(t *testing.T) {
	run := formattedRun()
	const sourceError = "private progress error /Users/private/state.db credential=private-credential"
	run.ExecutionStatus, run.Output, run.Error = "failed", sourceError, sourceError
	run.Events[0].Data["progress_error"] = sourceError
	cause := &harness.ExecutionError{Kind: harness.FailureProcess, Cause: errors.New("private native error")}
	text := resultText(run, cause)
	if warning, failure := strings.Index(text, progressReplyWarning), strings.Index(text, "执行进程异常退出"); warning < 0 || warning >= failure {
		t.Fatal("progress warning replaced or followed the native failure explanation", text)
	}
	for _, want := range []string{"部分操作完成", "旧任务没有自动重做", "⏱ 执行 2.5 s · 🤖 native-actual-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("native failure guidance or metadata missing: %q in %q", want, text)
		}
	}
	for _, hidden := range []string{"private progress error", "/Users/private", "private-credential", "private native error"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("private diagnostic displayed: %q in %q", hidden, text)
		}
	}
	if run.Output != sourceError || run.Error != sourceError || run.Events[0].Data["progress_error"] != sourceError {
		t.Fatal("failure presentation changed source evidence")
	}
}

func TestProgressRecordWarningRequiresRecordedPhaseFinishError(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    event.Event
	}{
		{"missing field", event.Event{Type: "phase.finished", Data: map[string]any{"duration_ms": 1}}},
		{"missing data", event.Event{Type: "phase.finished"}},
		{"empty error", event.Event{Type: "phase.finished", Data: map[string]any{"progress_error": ""}}},
		{"invalid error type", event.Event{Type: "phase.finished", Data: map[string]any{"progress_error": true}}},
		{"progress event", event.Event{Type: "phase.progress", Data: map[string]any{"progress_error": "private detail"}}},
		{"native event", event.Event{Type: "native.evidence", Data: map[string]any{"progress_error": "private detail"}}},
		{"run finish event", event.Event{Type: "run.finished", Data: map[string]any{"progress_error": "private detail"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := formattedRun()
			want := resultText(run, nil)
			run.Events = append(run.Events, tc.e)
			if text := resultText(run, nil); text != want {
				t.Fatal("unattributed or missing progress error changed the reply", text)
			}
		})
	}
}
