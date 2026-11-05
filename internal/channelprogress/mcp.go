// Package channelprogress exposes run-bound progress reservations over MCP stdio.
// Provider capabilities and network delivery remain in the Channel worker.
package channelprogress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"agent-host/internal/harness"
	"agent-host/internal/mcpstdio"
	"agent-host/internal/state"
)

const Instructions = `For a Channel task requiring several tool calls, call channel_progress.send_progress before starting the work and at meaningful stage changes or blockers. Supply a stage code from the tool enum, not a sentence: checking_permissions (检查权限), locating_target (定位目标), connecting (连接机器), checking_status (查询状态), checking_resources (检查资源), searching (检索资料), reading_logs (查看日志), analyzing (分析结果), waiting_confirmation (等待确认), preparing_answer (整理结果), or blocked (遇到阻碍). For example, before an SSH resource check call {"stage":"checking_resources","idempotency_key":"resource-check"}. Host generates the short attached reaction label. Never send ordinary process messages, routine per-tool chatter, raw logs, credentials or internal reasoning. Do not use the legacy text argument for explanations. If argument validation rejects a call, correct the stage and call again; validation rejection reserves no delivery attempt. At most five attempts are reserved per run, at least 15 seconds apart; failed and unknown attempts count. Reuse an idempotency_key to inspect the same attempt without sending again. Delivery is independent of task completion, acceptance and memory. The tool waits up to 25 seconds for delivery. pending means delivery has not been confirmed and cannot guarantee ordering relative to the final answer. Never replay pending or unknown attempts, including with a new key. DingTalk displays the short label; Feishu displays its native Typing status emoji. The destination is the authenticated current receipt and cannot be selected. Produce exactly one normal final answer.`

// Stage codes keep native callers from composing long sentences for a short badge.
var progressStages = []struct{ code, label string }{
	{"checking_permissions", "检查权限"},
	{"locating_target", "定位目标"},
	{"connecting", "连接机器"},
	{"checking_status", "查询状态"},
	{"checking_resources", "检查资源"},
	{"searching", "检索资料"},
	{"reading_logs", "查看日志"},
	{"analyzing", "分析结果"},
	{"waiting_confirmation", "等待确认"},
	{"preparing_answer", "整理结果"},
	{"blocked", "遇到阻碍"},
}

func stageLabel(code string) (string, bool) {
	for _, stage := range progressStages {
		if stage.code == code {
			return stage.label, true
		}
	}
	return "", false
}

const invalidStage = "Progress was not sent. Call send_progress again with a valid stage, for example {\"stage\":\"checking_resources\",\"idempotency_key\":\"resource-check\"}. Do not pass a sentence in text. Argument rejection reserves no delivery attempt."

type Binding struct {
	Home    string
	Receipt state.ChannelProgressBinding
}

func Resolve(home, message, run string) (Binding, error) {
	s, err := state.OpenForChannelProgress(home)
	if err != nil {
		return Binding{}, errors.New("channel progress state unavailable")
	}
	defer s.Close()
	b, err := s.ChannelProgressBinding(context.Background(), message, run)
	if err != nil {
		return Binding{}, state.ErrProgressUnavailable
	}
	return Binding{Home: s.Home(), Receipt: b}, nil
}
func (b Binding) Server(executable string) harness.MCPServer {
	digest := sha256.Sum256([]byte(b.Home + "\x00" + b.Receipt.ConversationKey))
	return harness.MCPServer{Name: "channel_progress", Command: executable, Args: []string{"channel-progress-mcp", "--home", b.Home, "--message", b.Receipt.MessageID, "--run", b.Receipt.RunID, "--fingerprint", b.Receipt.Fingerprint}, SessionKey: hex.EncodeToString(digest[:])}
}
func toolsList() []mcpstdio.Tool {
	codes := make([]string, 0, len(progressStages))
	for _, stage := range progressStages {
		codes = append(codes, stage.code)
	}
	return []mcpstdio.Tool{{Name: "send_progress", Description: "Update the short attached status on the original question. Select a stage code; Host renders its short label. Use before multi-step tool work and at meaningful stage changes. No free-text sentence or ordinary chat message is sent.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"stage": map[string]any{"type": "string", "enum": codes, "description": "Choose the current work stage. For a VM resource check use checking_resources; Host displays 检查资源."}, "idempotency_key": map[string]any{"type": "string", "maxLength": 256}}, "required": []string{"stage"}, "additionalProperties": false}, Annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}}}
}
func ToolContract() any { return toolsList() }
func send(ctx context.Context, s *state.Store, b state.ChannelProgressBinding, name string, raw json.RawMessage, wait time.Duration) (state.ChannelProgress, error) {
	var args struct {
		Stage string `json:"stage"`
		Text  string `json:"text"`
		Key   string `json:"idempotency_key"`
	}
	if name != "send_progress" || !utf8.Valid(raw) || mcpstdio.Decode(raw, &args) != nil {
		return state.ChannelProgress{}, errors.New("invalid channel progress arguments")
	}
	text := args.Text
	if args.Stage != "" {
		var ok bool
		text, ok = stageLabel(args.Stage)
		if !ok || args.Text != "" {
			return state.ChannelProgress{}, errors.New(invalidStage)
		}
	} else if _, err := state.ValidateProgressText(text); err != nil {
		return state.ChannelProgress{}, errors.New(invalidStage)
	}
	// Old native histories may still call the former short-text contract. Keep
	// valid labels compatible; long explanations must be corrected, not cut.
	p, err := s.ReserveChannelProgress(ctx, b, text, args.Key)
	if err != nil {
		return p, publicError(err)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for p.Status == "pending" {
		select {
		case <-ctx.Done():
			return p, nil
		case <-timer.C:
			return p, nil
		case <-ticker.C:
			p, err = s.ChannelProgress(ctx, p.ID)
			if err != nil {
				return state.ChannelProgress{}, errors.New("channel progress result unavailable")
			}
		}
	}
	return p, nil
}
func Serve(ctx context.Context, b Binding, in io.Reader, out io.Writer) error {
	s, err := state.OpenForChannelProgress(b.Home)
	if err != nil {
		return errors.New("channel progress state unavailable")
	}
	defer s.Close()
	current, err := s.ChannelProgressBinding(ctx, b.Receipt.MessageID, b.Receipt.RunID)
	if err != nil || current != b.Receipt {
		return state.ErrProgressUnavailable
	}
	return mcpstdio.Serve(ctx, "agent-host-channel-progress", toolsList(), func(ctx context.Context, name string, raw json.RawMessage) (any, error) {
		return send(ctx, s, b.Receipt, name, raw, 25*time.Second)
	}, in, out)
}

// Only adapter-owned validation messages may cross the MCP boundary.
func publicError(err error) error {
	if errors.Is(err, state.ErrProgressUnavailable) {
		return state.ErrProgressUnavailable
	}
	switch err.Error() {
	case "progress text must be valid UTF-8", "progress label must contain 1 to 12 Unicode code points without controls", "invalid progress idempotency key", "channel progress reservation limit reached", "channel progress requires 15 seconds between reservations":
		return err
	}
	return errors.New("channel progress reservation unavailable")
}
