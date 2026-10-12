package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

// Exercise the actual admission boundary before dispatch. A visible @ token
// alone never establishes group membership or permission to execute commands.
func addressedGroupInput(t *testing.T, f *serviceFixture, id, sender, text string, mentioned bool) (dingtalk.Message, error) {
	t.Helper()
	now := time.Now().UTC()
	raw, err := json.Marshal(map[string]any{
		"msgId": id, "conversationId": "synthetic-group", "conversationType": "2",
		"senderId": "sender-open-id", "senderStaffId": sender, "senderCorpId": f.config.CorpID,
		"chatbotCorpId": f.config.CorpID, "chatbotUserId": "robot-id", "msgtype": "text",
		"isInAtList": mentioned, "atUsers": []any{map[string]string{"dingtalkId": "robot-id"}},
		"createAt": now.UnixMilli(), "sessionWebhook": "https://oapi.dingtalk.com/robot/sendBySession?session=cap-" + id,
		"sessionWebhookExpiredTime": now.Add(time.Hour).UnixMilli(), "text": map[string]string{"content": text},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dingtalk.Parse(raw, f.config, now)
}

func processAddressedGroup(t *testing.T, f *projectFixture, id, sender, text string) state.ChannelMessage {
	t.Helper()
	m, err := addressedGroupInput(t, f.serviceFixture, id, sender, text, true)
	if err != nil {
		t.Fatal(err)
	}
	stored := f.process(t, m)
	if stored.Text != text {
		t.Fatalf("dispatch changed source evidence: %q != %q", stored.Text, text)
	}
	return stored
}

func TestAuthenticatedGroupAddressCommandsKeepEvidenceAndResetCurrentSession(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	for i, text := range []string{"@Helper /help", "@Helper /status", "@Helper /project Project alpha", "@Helper /status"} {
		m := processAddressedGroup(t, f, fmt.Sprintf("command-%d", i), "alice", text)
		if m.Status != "completed" || m.RunID != "" || m.SessionID != "" || len(m.Reactions) != 0 || len(f.runner.snapshot()) != 0 {
			t.Fatalf("addressed command became a task: %+v", m)
		}
		if i == 3 && !strings.Contains(m.ReplyText, "自动判断") {
			t.Fatal("addressed selection was not retained", m.ReplyText)
		}
	}
	first := processAddressedGroup(t, f, "first", "bob", "@Helper Inspect alpha")
	resumed := processAddressedGroup(t, f, "resumed", "alice", "@Helper continue")
	reset := processAddressedGroup(t, f, "reset", "bob", "@Helper /new")
	fresh := processAddressedGroup(t, f, "fresh", "alice", "@Helper Inspect alpha")
	if first.Status != "completed" || first.SessionID != resumed.SessionID || fresh.SessionID == first.SessionID || reset.Status != "completed" || reset.RunID != "" || len(reset.Reactions) != 0 || len(f.runner.snapshot()) != 3 {
		t.Fatal("addressed reset or shared continuation failed", first, resumed, reset, fresh)
	}
	for _, m := range []state.ChannelMessage{first, resumed, fresh} {
		run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(context.Background(), m.RunID)
		if err != nil || run.Input != m.Text {
			t.Fatal("native task lost exact source input", run, err)
		}
	}
	invalid := processAddressedGroup(t, f, "invalid", "alice", "@Helper /new extra")
	if invalid.Status != "failed" || invalid.RunID != "" || len(f.runner.snapshot()) != 3 {
		t.Fatal("addressed invalid command entered native execution", invalid)
	}
	if _, err := addressedGroupInput(t, f.serviceFixture, "fake", "alice", "@Helper /new", false); !errors.Is(err, dingtalk.ErrRejected) {
		t.Fatal("visible address authorized a fake group mention", err)
	}
}

func TestGroupAddressChoicesKeepRequesterAndOriginalTask(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	original := legacyAddressedPending(t, f, "ask", "alice", "@Helper ambiguous request")
	if original.Status != "awaiting" {
		t.Fatal("addressed task did not reach route clarification", original)
	}
	wrong := processAddressedGroup(t, f, "wrong", "bob", "@Helper 确认 "+choiceFor(original)+" alpha")
	if wrong.Status != "failed" || len(f.runner.snapshot()) != 0 {
		t.Fatal("address prefix bypassed confirmation ownership", wrong)
	}
	answer := processAddressedGroup(t, f, "answer", "alice", "@Helper 确认 "+choiceFor(original)+" Project alpha")
	if answer.Status != "completed" || len(f.runner.snapshot()) != 1 {
		t.Fatal("addressed conversational choice did not execute original", answer)
	}
	original = f.receipt(t, "ask")
	run, err := f.service.Store.WithPartition(original.AgentID, original.AudienceID).Run(context.Background(), original.RunID)
	if err != nil || run.Input != "@Helper ambiguous request" {
		t.Fatal("confirmation replaced original task evidence", run, err)
	}
	pending := legacyAddressedPending(t, f, "cancel-ask", "alice", "@Helper ambiguous other request")
	canceled := processAddressedGroup(t, f, "cancel-answer", "alice", "@Helper 取消 "+choiceFor(pending))
	if pending.Status != "awaiting" || canceled.Status != "completed" || f.receipt(t, "cancel-ask").Status != "failed" || len(f.runner.snapshot()) != 1 {
		t.Fatal("addressed conversational cancellation failed", pending, canceled)
	}
	chosen := legacyAddressedPending(t, f, "choose-ask", "alice", "@Helper ambiguous final request")
	confirmed := processAddressedGroup(t, f, "choose-answer", "alice", "@Helper 确认 "+choiceFor(chosen)+" alpha -- read only")
	chosen = f.receipt(t, "choose-ask")
	run, err = f.service.Store.WithPartition(chosen.AgentID, chosen.AudienceID).Run(context.Background(), chosen.RunID)
	if confirmed.Status != "completed" || len(f.runner.snapshot()) != 2 || err != nil || run.Input != chosen.Text || len(run.Supplements) != 1 || run.Supplements[0].Text != "read only" || run.Supplements[0].MessageID != "choose-answer" {
		t.Fatal("addressed explicit choice lost its original input or supplement", confirmed, run, err)
	}
}

func TestGroupAddressRoutingViewDoesNotRewriteTasksOrGuessMultiwordNames(t *testing.T) {
	f := newRoutingFixture(t)
	var requests []routing.Request
	f.service.Router = serviceRouter(func(_ context.Context, r routing.Request) (routing.Decision, error) {
		requests = append(requests, r)
		return routing.Decision{Kind: "default"}, nil
	})
	instanceDispatch(t, f, map[string]string{})
	for i, text := range []string{"@Helper initial task", "@Build Helper /new", "@Helper @Colleague /new"} {
		m := processAddressedGroup(t, f, fmt.Sprintf("task-%d", i), "alice", text)
		if m.Status != "completed" || m.RunID == "" {
			t.Fatal("address parser found a command beyond the first token", m)
		}
	}
	if len(requests) != 3 || requests[0].Text != "initial task" || requests[1].Text != "Helper /new" || requests[2].Text != "@Colleague /new" || len(requests[1].Recent) != 1 || requests[1].Recent[0].Text != "initial task" {
		t.Fatalf("routing view lost its bounded address normalization: %+v", requests)
	}
	private := f.process(t, f.message(t, "private", "@Helper /new", "text", "owner-dm"))
	if private.RunID == "" || private.Text != "@Helper /new" || requests[len(requests)-1].Text != private.Text {
		t.Fatal("private message was rewritten as a group command", private)
	}
}
