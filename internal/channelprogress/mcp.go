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

const Instructions = `Use channel_progress.send_progress for a concise attached reaction label during this Channel task at meaningful milestones or blockers only. Do not send routine per-tool chatter, raw tool logs, credentials or internal reasoning. Text is trimmed and must contain 1 to 12 Unicode code points without controls or multiline content. This is a Host policy, not a provider maximum. Progress never sends an ordinary text message. DingTalk displays the label; Feishu displays its native Typing status emoji. At most five attempts are reserved per run, at least 15 seconds apart; failed and unknown attempts count. Reuse an idempotency_key to inspect the same attempt without sending again. Delivery is independent of task completion, acceptance and memory. The tool waits up to 25 seconds for delivery. pending means delivery has not been confirmed and cannot guarantee ordering relative to the final answer. Do not automatically replay pending or unknown attempts, including with a new key. The destination is the authenticated current receipt and cannot be selected by the agent. Continue to produce the normal final answer.`

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
	return []mcpstdio.Tool{{Name: "send_progress", Description: "Reserve a concise attached reaction label to the current authenticated Channel receipt. Returns accepted, failed, unknown, or pending after a bounded wait. Same nonempty idempotency_key returns the original attempt. No destination argument is accepted.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 12}, "idempotency_key": map[string]any{"type": "string", "maxLength": 256}}, "required": []string{"text"}, "additionalProperties": false}, Annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}}}
}
func ToolContract() any { return toolsList() }
func send(ctx context.Context, s *state.Store, b state.ChannelProgressBinding, name string, raw json.RawMessage, wait time.Duration) (state.ChannelProgress, error) {
	var args struct {
		Text string `json:"text"`
		Key  string `json:"idempotency_key"`
	}
	if name != "send_progress" || !utf8.Valid(raw) || mcpstdio.Decode(raw, &args) != nil {
		return state.ChannelProgress{}, errors.New("invalid channel progress arguments")
	}
	p, err := s.ReserveChannelProgress(ctx, b, args.Text, args.Key)
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
