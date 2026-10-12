package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

// replyForMessage uses the current receipt's audience and sender, never model
// text or the previous speaker, for group addressing. The short blockquote is
// presentation only; it does not claim a native DingTalk reply relationship.
func replyForMessage(m state.ChannelMessage, body string) dingtalk.ReplyMessage {
	reply := dingtalk.ReplyMessage{Text: limitReply(body)}
	if m.Channel != "dingtalk" || m.ConversationType != "2" {
		return reply
	}
	reply.AtUserID = m.SenderID
	prefix := groupReplyPrefix(m)
	if prefix == "" {
		// Let the adapter reject an invalid mention without inserting its value
		// into the public text. Never derive a replacement recipient from input.
		return reply
	}
	budget := dingtalk.MaxReplyBytes - len(prefix)
	closed := body + closingFence(body)
	if len(closed) <= budget {
		reply.Text = prefix + closed
	} else {
		notice := "\n\n> 正文过长，以上为节选。"
		reply.Text = prefix + fitMarkdownBody(body, budget-len(notice)) + notice
	}
	return reply
}

// Use the same prefix for formatting and its byte budget so group quotes cannot
// push measured run metadata out of a long reply.
func groupReplyPrefix(m state.ChannelMessage) string {
	if m.Channel != "dingtalk" || m.ConversationType != "2" || m.SenderID == "" || dingtalk.ValidateAtUserID(m.SenderID) != nil {
		return ""
	}
	question := dingtalk.AddressedText(m.ConversationType, m.Text)
	quote := "原消息（非文字内容）"
	if (m.MessageType == "text" || m.MessageType == "richText") && strings.TrimSpace(question) != "" {
		quote = questionExcerpt(question)
	}
	return "@" + m.SenderID + "\n\n> **回复：** " + quote + "\n\n"
}

// Do not automatically repeat obvious local paths or credential assignments
// from the question. This bounded preview is not a general secret detector.
var privateQuestionField = regexp.MustCompile(`(?i)(^|[\s\x60"'(=：:])(?:/[\pL\pN_.-]+/|~/|[a-z]:\\)|(?:token|password|passwd|secret|api[_-]?key|authorization|密码|密钥|令牌|凭据)\s*[:=：]|\bBearer\s+\S+|-----BEGIN [A-Z ]*PRIVATE KEY-----`)

func questionExcerpt(text string) string {
	if privateQuestionField.MatchString(text) {
		return "原问题含路径或敏感字段，引用已省略。"
	}
	// Quoted source cannot create another visible @ target or active Markdown.
	text = strings.ReplaceAll(text, "@", "＠")
	return metadataSnippet(text, 480)
}

// resultText formats presentation only; the original native output and evidence
// remain unchanged in the run journal. Actual models and measured execution
// duration are public metadata for every audience.
func resultText(run state.Run, runErr error) string {
	return resultTextWithNotice(run, runErr, "", dingtalk.MaxReplyBytes)
}

func resultTextWithNotice(run state.Run, runErr error, notice string, maxBytes int) string {
	if run.ID == "" {
		text := executionFailureText(run, runErr)
		if notice != "" {
			text += "\n\n" + notice
		}
		return text
	}
	prefix := ""
	if notice != "" {
		prefix = "> " + notice + "\n\n"
	}
	for _, e := range run.Events {
		if message, ok := e.Data["progress_error"].(string); e.Type == "phase.finished" && ok && message != "" {
			prefix += "> ⚠️ 进度提示未能完整记录，本轮结果见下方。\n\n"
			break
		}
	}
	body := run.Output
	if run.ExecutionStatus != "completed" {
		// Native error output can contain provider diagnostics and local paths.
		// The complete outcome remains available in the local run journal.
		body = executionFailureText(run, runErr)
	} else if runErr != nil || run.MemoryStatus == "failed" || run.ExperienceStatus == "failed" {
		prefix += "> ⚠️ **任务执行已完成，但后续保存步骤失败或结果未确认。** 旧任务没有自动重做，可以继续提问；下次需要的关键信息请再次说明。\n\n"
	}
	if body == "" {
		body = "本轮没有可返回的正文。"
	}
	footer := runFooter(run)
	closedBody := body + closingFence(body)
	if len(prefix)+len(closedBody)+len(footer) <= maxBytes {
		return prefix + closedBody + footer
	}
	excerptNotice := "\n\n> 正文过长，以上为节选。"
	return prefix + fitMarkdownBody(body, maxBytes-len(prefix)-len(excerptNotice)-len(footer)) + excerptNotice + footer
}

func runFooter(run state.Run) string {
	var metadata []string
	if elapsed, ok := phaseDurationMS(run, "execute"); ok {
		metadata = append(metadata, "⏱ 执行 "+formatDurationMS(elapsed))
	}
	if model := strings.TrimSpace(run.Model); model != "" && model != "<synthetic>" {
		if label := metadataSnippet(model, 200); label != "" {
			metadata = append(metadata, "🤖 "+label)
		}
	}
	if len(metadata) == 0 {
		return ""
	}
	return "\n\n---\n" + strings.Join(metadata, " · ")
}

func phaseDurationMS(run state.Run, phase string) (float64, bool) {
	for i := len(run.Events) - 1; i >= 0; i-- {
		e := run.Events[i]
		if e.Type != "phase.finished" || e.Phase != phase {
			continue
		}
		var value float64
		switch n := e.Data["duration_ms"].(type) {
		case float64:
			value = n
		case int64:
			value = float64(n)
		case int:
			value = float64(n)
		case json.Number:
			var err error
			value, err = n.Float64()
			if err != nil {
				return 0, false
			}
		default:
			return 0, false
		}
		return value, !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
	}
	return 0, false
}

func formatDurationMS(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0f ms", ms)
	}
	return fmt.Sprintf("%.1f s", ms/1000)
}

func executionFailureText(run state.Run, runErr error) string {
	var resumeErr *state.SessionResumeError
	if errors.As(runErr, &resumeErr) && resumeErr.CanStartFresh() {
		return "当前对话无法继续，本次请求未能启动。下一条消息会尝试使用新会话和保存的上下文继续；也可用 /new 重置对话。旧任务没有自动重做。"
	}
	var failure *harness.ExecutionError
	var kind harness.FailureKind
	if errors.As(runErr, &failure) {
		kind = failure.Kind
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		kind = harness.FailureDeadline
	} else if errors.Is(runErr, context.Canceled) {
		kind = harness.FailureCanceled
	}
	if run.ID == "" {
		switch kind {
		case harness.FailureDeadline:
			return "本次请求未能启动：等待处理已超时。请减少请求范围后重新发送；旧任务没有自动重做。"
		case harness.FailureCanceled:
			return "本次请求已取消，没有开始执行。需要继续时请重新发送；旧任务没有自动重做。"
		default:
			return "本次请求未能启动。请检查当前助手配置或稍后重新发送；旧任务没有自动重做。"
		}
	}
	if kind == harness.FailureStartup {
		return "本次请求未能启动执行。请检查当前助手配置或稍后重新发送；旧任务没有自动重做。"
	}
	if kind == harness.FailureDeadline {
		return "本次处理已超时，可能已有部分操作完成。请先核对实际结果，再拆成较小任务重新发送；旧任务没有自动重做。"
	}
	if run.ExecutionStatus == "canceled" || kind == harness.FailureCanceled {
		return "本次请求已取消，可能已有部分操作完成。请先核对实际结果，需要继续时再发送请求；旧任务没有自动重做。"
	}
	if run.ExecutionStatus != "failed" || kind == harness.FailureUnknown {
		return "本次结果未确认，可能已有部分操作完成。请先检查实际结果，再决定是否重新发送；旧任务没有自动重做。"
	}
	if kind == harness.FailureProvider {
		return "本次处理失败：模型服务未能完成请求，可能已有部分操作完成。请先核对实际结果，稍后再明确发送需要继续的任务；旧任务没有自动重做。"
	}
	if kind == harness.FailureProcess {
		return "本次处理失败：执行进程异常退出，可能已有部分操作完成。请先核对实际结果，再明确发送需要继续的任务；旧任务没有自动重做。"
	}
	return "本次处理失败，可能已有部分操作完成。请先核对实际结果，再明确发送需要继续的任务；旧任务没有自动重做。"
}

// Public route metadata identifies only configured display names. Paths,
// session IDs, scopes and execution diagnostics stay in local evidence.
func routeMetadata(projectID, projectName, workspace, sessionID string) string {
	if projectName == "" {
		projectName = "默认工作区"
	}
	return "项目：" + metadataSnippet(projectName, 200)
}

func projectSetMetadata(projects []state.ProjectRef, primaryID, name, workspace string, additional []string, sessionID string) string {
	if len(projects) <= 1 {
		return routeMetadata(primaryID, name, workspace, sessionID)
	}
	var names []string
	for i, project := range projects {
		if i == 5 {
			break
		}
		label := project.Name
		if label == "" {
			label = "已配置项目"
		}
		names = append(names, metadataSnippet(label, 160))
	}
	text := "项目：" + strings.Join(names, "、")
	if len(projects) > 5 {
		text += fmt.Sprintf("（共 %d 个）", len(projects))
	}
	return text
}

// Escaping can multiply the byte size of otherwise short metadata. Bound each
// field after escaping so public project names remain bounded.
func metadataSnippet(text string, budget int) string {
	limit := min(len([]rune(text)), budget)
	if limit == 0 {
		return ""
	}
	for limit > 0 {
		value := quoteSnippet(text, limit)
		if len(value) <= budget {
			return value
		}
		limit--
	}
	return ""
}

// limitReply is the final guard for non-run system replies.
func limitReply(text string) string {
	if len(text) <= dingtalk.MaxReplyBytes {
		return text
	}
	notice := "\n\n> 正文过长，以上为节选。"
	return fitMarkdownBody(text, dingtalk.MaxReplyBytes-len(notice)) + notice
}

func quoteSnippet(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > limit {
		text = string(runes[:limit-1]) + "…"
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~").Replace(text)
}

// fitMarkdownBody prefers a complete line near the byte limit, and avoids a
// dangling inline link on the final line. It is deliberately not a Markdown parser.
func fitMarkdownBody(body string, budget int) string {
	if budget <= 0 {
		return ""
	}
	limit := min(len(body), budget)
	for limit > 0 {
		for limit > 0 && !utf8.ValidString(body[:limit]) {
			limit--
		}
		part := body[:limit]
		if limit < len(body) {
			if newline := strings.LastIndexByte(part, '\n'); newline >= len(part)*3/4 {
				part = part[:newline]
			}
			lineStart := strings.LastIndexByte(part, '\n') + 1
			line := part[lineStart:]
			if start := strings.LastIndexByte(line, '['); start >= 0 {
				tail := line[start:]
				if !strings.Contains(tail, "]") || strings.Contains(tail, "](") && !strings.Contains(tail, ")") {
					part = part[:lineStart+start]
				}
			}
		}
		closure := closingFence(part)
		if len(part)+len(closure) <= budget {
			return part + closure
		}
		limit = min(len(part)-1, budget-len(closure))
	}
	return ""
}

// closingFence keeps excerpts and their truncation notice outside code blocks.
func closingFence(text string) string {
	var marker byte
	width := 0
	for _, line := range strings.Split(text, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent > 3 {
			continue
		}
		line = line[indent:]
		if len(line) < 3 || line[0] != '`' && line[0] != '~' {
			continue
		}
		count := 1
		for count < len(line) && line[count] == line[0] {
			count++
		}
		if count < 3 {
			continue
		}
		if width == 0 {
			marker, width = line[0], count
		} else if line[0] == marker && count >= width && strings.TrimSpace(line[count:]) == "" {
			width = 0
		}
	}
	if width == 0 {
		return ""
	}
	return "\n" + strings.Repeat(string(marker), width)
}
