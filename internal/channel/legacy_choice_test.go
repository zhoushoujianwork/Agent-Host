package channel

import (
	"context"
	"encoding/json"
	"testing"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

// Seed a receipt created by an older release. Current routing must never create
// another pending project choice; these fixtures retain upgrade compatibility.
func legacyPending(t *testing.T, f *projectFixture, input dingtalk.Message) state.ChannelMessage {
	t.Helper()
	f.receive(t, input)
	m := f.next(t)
	decision := routing.Decision{Kind: "clarify", Reason: "A historical request awaits its project.", CandidateIDs: []string{"alpha", "beta"}}
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.service.Store.SaveChannelRoutingDecision(ctx, m.ID, encoded); err != nil {
		t.Fatal(err)
	}
	choice := state.ChannelChoice{ID: choiceFor(m), Question: decision.Reason, CandidateIDs: decision.CandidateIDs, Decision: encoded}
	if err := f.service.Store.WaitChannelChoice(ctx, m.ID, choice); err != nil {
		t.Fatal(err)
	}
	reply := replyForMessage(m, "历史任务等待确认，请由原提问者回复项目名称。任务编号："+choice.ID)
	if err := f.service.Store.PrepareChannelReply(ctx, m.ID, reply.Text); err != nil {
		t.Fatal(err)
	}
	url, err := f.service.Store.ChannelReplyURL(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Replier.Reply(ctx, url, m.ReplyExpiresAt, reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.FinishChannelReply(ctx, m.ID, status, ""); err != nil {
		t.Fatal(err)
	}
	return f.receipt(t, input.ID)
}

func legacyAddressedPending(t *testing.T, f *projectFixture, id, sender, text string) state.ChannelMessage {
	t.Helper()
	m, err := addressedGroupInput(t, f.serviceFixture, id, sender, text, true)
	if err != nil {
		t.Fatal(err)
	}
	return legacyPending(t, f, m)
}
