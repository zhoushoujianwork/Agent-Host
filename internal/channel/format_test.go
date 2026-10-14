package channel

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/decision/jev"
	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func formattedRun() state.Run {
	return state.Run{
		ID: "9a5c81ed-72ac-4d90-8b98-61d1b900f076", Input: "这个项目如何保存记忆？", Output: "**答案**\n\n- 保留来源\n- 独立记录执行与记忆状态",
		ExecutionStatus: "completed", MemoryStatus: "unchanged", ExperienceStatus: "empty", Model: "native-actual-model", RequestedModel: "requested-model",
		Events: []event.Event{
			{Type: "phase.finished", Phase: "execute", Model: "requested-model", Data: map[string]any{"duration_ms": float64(2530)}},
			{Type: "phase.finished", Phase: "remember", Data: map[string]any{"duration_ms": int64(85)}},
		},
	}
}

func TestPublicReplyShowsMeasuredRuntimeMetadata(t *testing.T) {
	run := formattedRun()
	run.ProjectID, run.ProjectName, run.Workspace, run.SessionID = "internal-project", "Private project", "/Users/private/workspace", "internal-session"
	run.AdditionalDirectories = []string{"/Users/private/other"}
	before, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(run, nil)
	if !strings.Contains(text, run.Output) {
		t.Fatal("missing answer", text)
	}
	if !strings.HasSuffix(text, "\n\n---\n⏱ 执行 2.5 s · 🤖 native-actual-model") {
		t.Fatal("missing actual model or measured execution duration", text)
	}
	for _, hidden := range []string{run.ID, run.ProjectID, run.ProjectName, run.Workspace, run.SessionID, run.AdditionalDirectories[0], run.RequestedModel, "🧠", "2530"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("internal detail displayed: %s in %s", hidden, text)
		}
	}
	after, err := json.Marshal(run)
	if err != nil || string(before) != string(after) {
		t.Fatal("presentation changed stored evidence", err)
	}
}

func TestReplyFormattingDoesNotInventMissingTimingsOrActualModel(t *testing.T) {
	run := formattedRun()
	run.Model, run.Events = "", nil
	text := resultText(run, nil)
	if strings.Contains(text, "⏱") || strings.Contains(text, "🧠") || strings.Contains(text, "🤖") || strings.Contains(text, run.RequestedModel) {
		t.Fatal("metadata without evidence", text)
	}
	if !strings.HasSuffix(text, run.Output) || strings.Contains(text, run.ID) || strings.Contains(text, "---") {
		t.Fatal("missing body or unexpected empty footer", text)
	}
}

func TestSyntheticErrorModelIsNotDisplayedAsAnActualModel(t *testing.T) {
	run := formattedRun()
	run.Model, run.ExecutionStatus = "<synthetic>", "failed"
	run.Output = "API Error: streaming is required"
	text := resultText(run, errors.New("exit status 1"))
	if strings.Contains(text, "synthetic") || strings.Contains(text, "🤖") || strings.Contains(text, run.RequestedModel) || strings.Contains(text, run.Output) || !strings.Contains(text, "处理失败") || run.Model != "<synthetic>" {
		t.Fatal("synthetic model was displayed or evidence changed", text)
	}
}

func TestReplyFailureWarningPrecedesBodyWithoutInternalDetails(t *testing.T) {
	run := formattedRun()
	run.MemoryStatus, run.ExperienceStatus = "failed", "review_pending"
	text := resultText(run, errors.New("private provider detail must not leak"))
	if warning, body := strings.Index(text, "⚠️"), strings.Index(text, run.Output); warning < 0 || warning >= body {
		t.Fatal("failure warning hidden behind body", text)
	}
	if !strings.Contains(text, "失败或结果未确认") || strings.Contains(text, "private provider") || strings.Contains(text, run.ID) || strings.Contains(text, "下方状态") {
		t.Fatal("failure hidden or internal detail leaked", text)
	}
}

func TestReplyDoesNotReflectPotentialSecretsInQuestion(t *testing.T) {
	run := formattedRun()
	run.Input = "Inspect /Users/private/secret with token private-credential"
	text := resultText(run, nil)
	if !strings.HasPrefix(text, run.Output) || strings.Contains(text, "private-") {
		t.Fatal("public reply reflected input", text)
	}
}

func TestReplyLongMarkdownKeepsFooterAndClosesTruncatedCodeBlock(t *testing.T) {
	for _, fence := range []string{"```", "~~~", "````"} {
		t.Run(fence, func(t *testing.T) {
			run := formattedRun()
			run.MemoryStatus = "failed"
			run.Output = "代码示例：\n\n" + fence + "go\n" + strings.Repeat("fmt.Println(\"保存来源\")\n", 500) + fence
			original := run.Output
			text := resultText(run, errors.New("memory step failed"))
			if !utf8.ValidString(text) || len(text) > dingtalk.MaxReplyBytes || !strings.Contains(text, "节选") || strings.Contains(text, run.ID) || strings.Contains(text, "run show") || !strings.HasSuffix(text, "⏱ 执行 2.5 s · 🤖 native-actual-model") {
				t.Fatalf("invalid excerpt size=%d text=%q", len(text), text)
			}
			if !strings.Contains(text, "\n"+fence+"\n\n> 正文过长") || strings.Count(text, "⚠️") != 1 || closingFence(text) != "" {
				t.Fatal("footer swallowed by code or status omitted", text)
			}
			if run.Output != original {
				t.Fatal("truncation modified stored native output")
			}
		})
	}
}

func TestRuntimeFooterUsesOnlyValidRecordedExecutionTimings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"live milliseconds", int64(85), "85 ms"},
		{"integer", 2000, "2.0 s"},
		{"decoded JSON", float64(2530), "2.5 s"},
		{"JSON number", json.Number("1500"), "1.5 s"},
		{"recorded zero", int64(0), "0 ms"},
		{"missing", nil, ""},
		{"string", "1500", ""},
		{"negative", int64(-1), ""},
		{"invalid number", json.Number("invalid"), ""},
		{"NaN", math.NaN(), ""},
		{"infinite", math.Inf(1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := formattedRun()
			run.Model = ""
			run.Events = []event.Event{
				{Type: "phase.finished", Phase: "remember", Data: map[string]any{"duration_ms": int64(9999)}},
				{Type: "phase.finished", Phase: "execute", Data: map[string]any{"duration_ms": tc.value}},
			}
			text := resultText(run, nil)
			if tc.want == "" {
				if text != run.Output {
					t.Fatal("invented timing without execution evidence", text)
				}
			} else if !strings.HasSuffix(text, "\n\n---\n⏱ 执行 "+tc.want) {
				t.Fatal("missing measured execution duration", text)
			}
		})
	}
}

func TestRuntimeFooterShowsActualModelWithoutTimingAndEscapesMarkdown(t *testing.T) {
	run := formattedRun()
	run.Events = nil
	run.Model = "actual-[model] *name*"
	text := resultText(run, nil)
	if !strings.HasSuffix(text, `🤖 actual-\[model\] \*name\*`) || strings.Contains(text, "⏱") || strings.Contains(text, run.RequestedModel) {
		t.Fatal("model metadata was forged or unescaped", text)
	}
}

func TestFailedExecutionKeepsObservedRuntimeFooter(t *testing.T) {
	run := formattedRun()
	run.ExecutionStatus = "canceled"
	text := resultText(run, context.DeadlineExceeded)
	if !strings.Contains(text, "已超时") || !strings.HasSuffix(text, "⏱ 执行 2.5 s · 🤖 native-actual-model") || strings.Contains(text, run.Output) {
		t.Fatal("failure lost observed metadata or displayed partial diagnostic output", text)
	}
}

func TestReplyShortBodyWhitespaceAndMarkdownArePreserved(t *testing.T) {
	run := formattedRun()
	run.Output = "  **正文**\n\n[来源](https://example.test)\n\n```go\nx := 1\n```\n"
	text := resultText(run, nil)
	if !strings.HasPrefix(text, run.Output) || strings.Contains(text, "节选") {
		t.Fatal("short Markdown body was altered", text)
	}
	run.Output = "```go\nx := 1"
	if text := resultText(run, nil); !strings.Contains(text, run.Output+"\n```") {
		t.Fatal("unterminated short code block swallowed footer", text)
	}
}

func TestReplyTruncationAvoidsIncompleteLinkAndMultibyteBoundary(t *testing.T) {
	run := formattedRun()
	run.Output = strings.Repeat("答", 500) + " [完整链接](https://example.test/" + strings.Repeat("long-path-", 1000) + ")"
	text := resultText(run, nil)
	if !utf8.ValidString(text) || len(text) > dingtalk.MaxReplyBytes || strings.Contains(text, "[完整链接]") || !strings.Contains(text, "节选") {
		t.Fatal("excerpt retained a broken link", text)
	}
	guarded := limitReply(strings.Repeat("多字节正文", 1000))
	if !utf8.ValidString(guarded) || len(guarded) > dingtalk.MaxReplyBytes || !strings.Contains(guarded, "节选") || strings.Contains(guarded, "run show") {
		t.Fatal("final safety limit broke UTF-8 or displayed internal commands")
	}
}

func TestReplyWithoutRunDoesNotRequireManualSessionReset(t *testing.T) {
	text := resultText(state.Run{Output: "untrusted partial output"}, errors.New("no execution"))
	if !strings.Contains(text, "本次请求未能启动") || strings.Contains(text, "/new") || strings.Contains(text, "untrusted partial output") || strings.Contains(text, "运行：") {
		t.Fatal(text)
	}
}

func TestRoutingFailureReplyExplainsCauseAndRemedy(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{&routing.EvaluationError{Cause: &jev.ChoiceMismatchError{Question: "private-question"}, Attempts: 2}, "自动重新评估一次后仍异常"},
		{&routing.EvaluationError{Cause: errors.New("private-secret https://internal.invalid"), Attempts: 1}, "Jev 请求失败或返回格式异常"},
		{context.DeadlineExceeded, "项目路由等待超时"},
		{context.Canceled, "项目路由尚未完成"},
		{errors.New("/private/project invalid configuration"), "项目路由配置或结果校验失败"},
	} {
		text := resultTextWithNotice(state.Run{}, &routingFailureError{Cause: tc.cause}, "", dingtalk.MaxReplyBytes)
		if !strings.Contains(text, tc.want) || !strings.Contains(text, "任务没有执行") || !strings.Contains(text, "重新发送") || strings.Contains(text, "private") || strings.Contains(text, "internal.invalid") || strings.Count(text, "本次请求") != 1 {
			t.Fatal(text)
		}
	}
}

func TestPublicCommandsUseOnlyDisplayNames(t *testing.T) {
	text := routeMetadata("private-id", "Project Alpha", "/private/work", "internal-session")
	if text != "项目：Project Alpha" {
		t.Fatal(text)
	}
	text = projectSetMetadata([]state.ProjectRef{{ID: "a", Name: "Alpha"}, {ID: "b", Name: "Beta"}}, "a", "Alpha", "/private/work", []string{"/private/extra"}, "session")
	if text != "项目：Alpha、Beta" {
		t.Fatal(text)
	}
}

func TestFailureWithoutRunDoesNotExposeAssembledEnvironment(t *testing.T) {
	text := resultText(state.Run{ProjectID: "private-project", Workspace: "/private/cwd", SessionID: "private-session"}, errors.New("private-error"))
	for _, hidden := range []string{"private-project", "/private/cwd", "private-session", "private-error"} {
		if strings.Contains(text, hidden) {
			t.Fatal("failed startup disclosed internal configuration", text)
		}
	}
}

func TestExecutionFailureRepliesExplainOutcomeAndSupportedNextStep(t *testing.T) {
	const diagnostic = "private diagnostic /Users/private token=secret"
	for _, tc := range []struct {
		name, status, category, nextStep string
		kind                             harness.FailureKind
		cause                            error
	}{
		{"provider", "failed", "模型服务未能完成", "稍后", harness.FailureProvider, errors.New(diagnostic)},
		{"process", "failed", "进程异常退出", "再明确发送", harness.FailureProcess, errors.New(diagnostic)},
		{"timeout", "canceled", "已超时", "较小任务", harness.FailureDeadline, context.DeadlineExceeded},
		{"canceled", "canceled", "已取消", "需要继续时", harness.FailureCanceled, context.Canceled},
		{"unknown", "unknown", "结果未确认", "是否重新发送", harness.FailureUnknown, errors.New(diagnostic)},
		{"outcome not persisted", "running", "结果未确认", "是否重新发送", "", errors.New(diagnostic)},
		{"legacy failure", "failed", "处理失败", "再明确发送", "", errors.New(diagnostic)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := formattedRun()
			run.ExecutionStatus, run.Output, run.Error = tc.status, diagnostic, diagnostic
			cause := tc.cause
			if tc.kind != "" {
				cause = &harness.ExecutionError{Kind: tc.kind, Cause: cause}
			}
			text := resultText(run, errors.Join(cause, errors.New(diagnostic)))
			for _, want := range []string{tc.category, tc.nextStep, "部分操作完成", "先", "旧任务没有自动重做"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %q", want, text)
				}
			}
			for _, hidden := range []string{"private", "token=secret", "/retry", "已回滚"} {
				if strings.Contains(text, hidden) {
					t.Fatalf("unsafe or unsupported guidance %q in %q", hidden, text)
				}
			}
			if tc.status == "canceled" && tc.kind != harness.FailureDeadline && strings.Contains(text, "失败") {
				t.Fatal("cancellation presented as a system failure", text)
			}
			if run.Output != diagnostic || run.Error != diagnostic {
				t.Fatal("formatting rewrote local evidence")
			}
		})
	}
}

func TestStartupAndUnresumableSessionGuidance(t *testing.T) {
	startup := resultText(state.Run{ID: "run", ExecutionStatus: "failed"}, &harness.ExecutionError{Kind: harness.FailureStartup, Cause: errors.New("private config")})
	if !strings.Contains(startup, "未能启动执行") || !strings.Contains(startup, "检查当前助手配置") || strings.Contains(startup, "部分操作完成") {
		t.Fatal(startup)
	}
	resume := resultText(state.Run{}, &state.SessionResumeError{RunID: "private-run", ExecutionStatus: "failed"})
	for _, want := range []string{"新会话", "保存的上下文", "/new", "旧任务没有自动重做"} {
		if !strings.Contains(resume, want) {
			t.Fatal(resume)
		}
	}
	if strings.Contains(resume, "private-run") || strings.Contains(resume, "/retry") {
		t.Fatal(resume)
	}
}

func TestCompletedExecutionWithSavingFailureDoesNotSuggestRepeatingTask(t *testing.T) {
	run := formattedRun()
	run.MemoryStatus = "failed"
	text := resultText(run, errors.New("private memory error"))
	for _, want := range []string{"执行已完成", "保存步骤失败", "可以继续提问", "关键信息请再次说明", run.Output} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	if strings.Contains(text, "重新发送") || strings.Contains(text, "private memory error") {
		t.Fatal(text)
	}
}
