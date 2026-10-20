package channel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-host/internal/event"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

// Sanitized terminal metrics from the verified daily-operations handoff.
const usageReplyExecution = `{"type":"result","uuid":"execution-result","session_id":"native-execution","total_cost_usd":3.111876,"usage":{"input_tokens":999902,"output_tokens":6952,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`
const usageReplyExtraction = `{"type":"result","uuid":"extraction-result","session_id":"native-extraction","total_cost_usd":0.213489,"usage":{"input_tokens":30938,"output_tokens":1851,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`

func TestReplyUsageEvidence(t *testing.T) {
	run := formattedRun()
	run.Events = append(run.Events,
		event.Event{Phase: "execute", Component: "harness.claude", Data: map[string]any{"raw": json.RawMessage(usageReplyExecution)}},
		event.Event{Phase: "extract", Component: "harness.claude.extractor", Data: map[string]any{"raw": json.RawMessage(usageReplyExtraction)}},
	)
	text := resultText(run, nil)
	for _, want := range []string{"$3.325"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing observed accounting", want, text)
		}
	}
	for _, unwanted := range []string{"Token", "已记录", "估算", "Jev", "$3.325365", "💰"} {
		if strings.Contains(text, unwanted) {
			t.Fatal("verbose accounting remains in footer", text)
		}
	}
	if strings.Contains(text, "native-execution") || strings.Contains(text, run.ID) {
		t.Fatal("public accounting leaked evidence identifiers", text)
	}
	t.Log("Rendered reply from sanitized recorded metrics:\n" + text)
}

func TestReplyUsageKeepsRecordedZeroCompact(t *testing.T) {
	run := formattedRun()
	run.Events = append(run.Events,
		event.Event{Phase: "execute", Component: "harness.claude", Data: map[string]any{"raw": json.RawMessage(`{"type":"result","uuid":"one","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":2}}`)}},
		event.Event{Phase: "extract", Component: "harness.claude.extractor", Data: map[string]any{"raw": json.RawMessage(`{"type":"result","uuid":"two"}`)}},
	)
	text := resultText(run, nil)
	if !strings.Contains(text, "$0.000") || strings.Contains(text, "Token") || strings.Contains(text, "部分调用") {
		t.Fatal("recorded zero did not use compact footer", text)
	}
	if strings.Contains(resultText(formattedRun(), nil), "$") {
		t.Fatal("unavailable cost rendered as zero")
	}
}

func TestReplyUsageSurvivesChannelDeliveryAndLongBody(t *testing.T) {
	for _, channel := range []string{"dingtalk", "feishu", "feishu_meeting"} {
		t.Run(channel, func(t *testing.T) {
			var f *serviceFixture
			var other *meetingTestReplier
			switch channel {
			case "dingtalk":
				f = newServiceFixture(t)
			case "feishu":
				f, other = newFeishuFixture(t)
			default:
				f, other = newMeetingFixture(t)
			}
			f.runner.resultRaw = json.RawMessage(usageReplyExecution)
			full := "```go\n" + strings.Repeat("fmt.Println(\"结果\")\n", 500)
			f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
				return harness.Result{Status: "completed", Text: full, Model: "observed-model", NativeSessionID: req.NativeSessionID}, nil
			}
			var m state.ChannelMessage
			switch channel {
			case "dingtalk":
				m = f.process(t, f.message(t, "accounting", "Check service health", "text", "owner-dm"))
			case "feishu":
				m = processMeeting(t, f, feishuMessage("accounting", "2", "oc_accounting", "ou_alice", "Check service health"))
			default:
				m = processMeeting(t, f, meetingMessage("accounting", "700000000000000001", "alice", "Check service health"))
			}
			wire := m.ReplyText
			if other != nil {
				if len(other.calls) != 1 || other.calls[0].text != wire {
					t.Fatal("stored reply differs from transport")
				}
			} else if calls := f.replier.snapshot(); len(calls) != 1 || calls[0].text != wire {
				t.Fatal("stored reply differs from transport")
			}
			if channel == "feishu" {
				wire = "[AI]\n" + wire
			}
			if m.ReplyStatus != "accepted" || len(wire) > 4000 || !utf8.ValidString(wire) || closingFence(wire) != "" ||
				!strings.Contains(wire, "节选") || !strings.Contains(wire, "$3.112") || strings.Contains(wire, "Token") {
				t.Fatal("channel lost usage or exceeded its byte budget", wire)
			}
			saved, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(t.Context(), m.RunID)
			if err != nil || saved.Output != full || len(saved.Events) == 0 {
				t.Fatal("presentation changed saved native evidence", err)
			}
			t.Logf("%s: stored and fixture-delivered reply match; %d bytes; compact cost retained", channel, len(wire))
		})
	}
}
