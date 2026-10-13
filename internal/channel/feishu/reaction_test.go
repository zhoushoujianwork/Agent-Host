package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
)

const reactionAck = `{"ok":true,"identity":"bot","data":{"reaction_id":"reaction_record-1","operator":{"operator_id":"cli_test","operator_type":"app"},"action_time":"1791776000000","reaction_type":{"emoji_type":"Get"}}}`

func TestReactionStagesUseExactEmojiAndOneBotCreate(t *testing.T) {
	for label, emoji := range map[string]string{"已收到": "Get", "处理中": "Typing", "已完成": "DONE", "打叉": "ERROR"} {
		t.Run(label, func(t *testing.T) {
			ack := strings.Replace(reactionAck, `"Get"`, `"`+emoji+`"`, 1)
			client := &mockClient{out: feishumeeting.Output{Stdout: []byte(ack), Started: true}}
			target := fixtureTarget()
			target.UUID = "" // Reaction APIs do not expose a reply UUID.
			id, status, err := (Reactor{Config: fixtureConfig(), Client: client}).AddReaction(t.Context(), target, label)
			if id != "reaction_record-1" || status != Accepted || err != nil || len(client.calls) != 1 {
				t.Fatal("own-app reaction was not acknowledged", id, status, err, len(client.calls))
			}
			want := []string{"--profile", "test-profile", "im", "reactions", "create", "--as", "bot",
				"--params", `{"message_id":"om_source"}`, "--data", `{"reaction_type":{"emoji_type":"` + emoji + `"}}`, "--format", "json"}
			if !reflect.DeepEqual(client.calls[0], want) {
				t.Fatal("reaction changed source, identity or documented API flags", client.calls[0])
			}
		})
	}
}

func TestReactionCreateRequiresOwnAppAndUnambiguousAcknowledgement(t *testing.T) {
	for _, tc := range reactionResultCases() {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockClient{out: feishumeeting.Output{Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr), Started: tc.started}, err: tc.err}
			id, status, err := (Reactor{Config: fixtureConfig(), Client: client}).AddReaction(t.Context(), fixtureTarget(), "已收到")
			if status != tc.want || (err == nil) != (status == Accepted) || status != Accepted && id != "" ||
				err != nil && strings.Contains(err.Error(), "SECRET") || len(client.calls) != 1 {
				t.Fatalf("id=%q status=%s err=%v calls=%d", id, status, err, len(client.calls))
			}
			if status == Accepted && id != "reaction_record-1" {
				t.Fatal("accepted reaction did not return its provider ID", id)
			}
		})
	}
}

type reactionResultCase struct {
	name, stdout, stderr string
	started              bool
	err                  error
	want                 string
}

func reactionResultCases() []reactionResultCase {
	return []reactionResultCase{
		{"accepted", reactionAck, "", true, nil, Accepted},
		{"warning", reactionAck, "private warning SECRET", true, nil, Accepted},
		{"ack on stderr", "", reactionAck, true, nil, Unknown},
		{"contradictory envelopes", reactionAck, `{"ok":false,"error":{"type":"permission"}}`, true, nil, Unknown},
		{"other app", strings.Replace(reactionAck, "cli_test", "cli_other", 1), "", true, nil, Unknown},
		{"bot open id is not app id", strings.Replace(reactionAck, "cli_test", "ou_bot", 1), "", true, nil, Unknown},
		{"human operator", strings.Replace(reactionAck, `"operator_type":"app"`, `"operator_type":"user"`, 1), "", true, nil, Unknown},
		{"wrong emoji", strings.Replace(reactionAck, `"Get"`, `"DONE"`, 1), "", true, nil, Unknown},
		{"unsupported emoji", strings.Replace(reactionAck, `"Get"`, `"SMILE"`, 1), "", true, nil, Unknown},
		{"wrong emoji case", strings.Replace(reactionAck, `"Get"`, `"GET"`, 1), "", true, nil, Unknown},
		{"message id reused as reaction", strings.Replace(reactionAck, "reaction_record-1", "om_source", 1), "", true, nil, Unknown},
		{"chat id reused as reaction", strings.Replace(reactionAck, "reaction_record-1", "oc_chat", 1), "", true, nil, Unknown},
		{"empty reaction id", strings.Replace(reactionAck, "reaction_record-1", "", 1), "", true, nil, Unknown},
		{"oversized reaction id", strings.Replace(reactionAck, "reaction_record-1", strings.Repeat("x", maxReactionIDBytes+1), 1), "", true, nil, Unknown},
		{"whitespace reaction id", strings.Replace(reactionAck, "reaction_record-1", "record secret", 1), "", true, nil, Unknown},
		{"numeric reaction id", strings.Replace(reactionAck, `"reaction_record-1"`, "123", 1), "", true, nil, Unknown},
		{"missing operator", `{"ok":true,"identity":"bot","data":{"reaction_id":"reaction_record-1","reaction_type":{"emoji_type":"Get"}}}`, "", true, nil, Unknown},
		{"missing emoji", strings.Replace(reactionAck, `"emoji_type":"Get"`, `"other":"Get"`, 1), "", true, nil, Unknown},
		{"missing data", `{"ok":true,"identity":"bot"}`, "", true, nil, Unknown},
		{"wrong outer identity", strings.Replace(reactionAck, `"identity":"bot"`, `"identity":"user"`, 1), "", true, nil, Unknown},
		{"missing outer identity", strings.Replace(reactionAck, `"identity":"bot",`, "", 1), "", true, nil, Unknown},
		{"null identity", strings.Replace(reactionAck, `"identity":"bot"`, `"identity":null`, 1), "", true, nil, Unknown},
		{"duplicate alias", strings.Replace(reactionAck, `"operator_type":"app"`, `"operator_type":"app","OPERATOR_TYPE":"user"`, 1), "", true, nil, Unknown},
		{"wrong case field", strings.Replace(reactionAck, `"reaction_id"`, `"REACTION_ID"`, 1), "", true, nil, Unknown},
		{"trailing object", reactionAck + `{}`, "", true, nil, Unknown},
		{"null ok", strings.Replace(reactionAck, `"ok":true`, `"ok":null`, 1), "", true, nil, Unknown},
		{"oversized envelope", `{"ok":true,"identity":"bot","padding":"` + strings.Repeat("x", maxFrameBytes) + `"}`, "", true, nil, Unknown},
		{"validation rejected", `{"ok":false,"error":{"type":"validation","message":"SECRET"}}`, "", true, nil, Failed},
		{"auth rejected", "", `{"ok":false,"identity":"bot","error":{"type":"auth","message":"SECRET"}}`, true, errors.New("SECRET"), Failed},
		{"authorization rejected", "", `{"ok":false,"error":{"type":"authorization","subtype":"missing_scope","message":"SECRET"}}`, true, errors.New("SECRET"), Failed},
		{"permission rejected", `{"ok":false,"error":{"type":"permission","message":"SECRET"}}`, "", true, nil, Failed},
		{"network ambiguity", "", `{"ok":false,"error":{"type":"network","message":"SECRET"}}`, true, errors.New("SECRET"), Unknown},
		{"unknown api failure", `{"ok":false,"error":{"type":"api","code":500,"message":"SECRET"}}`, "", true, nil, Unknown},
		{"repeat processing is ambiguous", `{"ok":false,"error":{"type":"api","code":231015,"message":"SECRET"}}`, "", true, nil, Unknown},
		{"invalid numeric error code", `{"ok":false,"error":{"type":"api","code":"231007"}}`, "", true, nil, Unknown},
		{"unclassified rejection", `{"ok":false}`, "", true, nil, Unknown},
		{"ack with exit failure", reactionAck, "", true, errors.New("SECRET"), Unknown},
		{"not started", "", "", false, errors.New("SECRET"), Failed},
		{"ack without start", reactionAck, "", false, nil, Failed},
		{"no json", "SECRET", "SECRET", true, errors.New("SECRET"), Unknown},
	}
}

func TestReactionRemoveUsesOnlySavedIDAndVerifiesOwnOperator(t *testing.T) {
	for _, tc := range []struct {
		name, ack string
		want      string
	}{
		{"accepted", reactionAck, Accepted},
		{"processing stage", strings.Replace(reactionAck, `"Get"`, `"Typing"`, 1), Accepted},
		{"completed stage", strings.Replace(reactionAck, `"Get"`, `"DONE"`, 1), Accepted},
		{"failed stage", strings.Replace(reactionAck, `"Get"`, `"ERROR"`, 1), Accepted},
		{"other record", strings.Replace(reactionAck, "reaction_record-1", "reaction_record-2", 1), Unknown},
		{"other operator", strings.Replace(reactionAck, "cli_test", "cli_other", 1), Unknown},
		{"human operator", strings.Replace(reactionAck, `"operator_type":"app"`, `"operator_type":"user"`, 1), Unknown},
		{"unsupported emoji", strings.Replace(reactionAck, `"Get"`, `"SMILE"`, 1), Unknown},
		{"known ownership rejection", `{"ok":false,"error":{"type":"api","code":231007,"message":"SECRET"}}`, Failed},
		{"repeated processing ambiguity", `{"ok":false,"error":{"type":"api","code":231015,"message":"SECRET"}}`, Unknown},
		{"unconfirmed", "SECRET", Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockClient{out: feishumeeting.Output{Stdout: []byte(tc.ack), Started: true}}
			status, err := (Reactor{Config: fixtureConfig(), Client: client}).RemoveReaction(t.Context(), fixtureTarget(), "reaction_record-1")
			if status != tc.want || (err == nil) != (status == Accepted) || err != nil && strings.Contains(err.Error(), "SECRET") || len(client.calls) != 1 {
				t.Fatal("unexpected removal result or retry", status, err, len(client.calls))
			}
			want := []string{"--profile", "test-profile", "im", "reactions", "delete", "--as", "bot",
				"--params", `{"message_id":"om_source","reaction_id":"reaction_record-1"}`, "--format", "json"}
			if !reflect.DeepEqual(client.calls[0], want) {
				t.Fatal("removal inferred a different target, identity or API", client.calls[0])
			}
		})
	}
}

func TestInvalidReactionTargetsCannotMutateIncludingMeetingTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config, *state.ChannelReplyTarget)
	}{
		{"invalid config", func(c *Config, _ *state.ChannelReplyTarget) { c.OwnerOpenID = "" }},
		{"meeting", func(_ *Config, target *state.ChannelReplyTarget) { target.Kind = state.ReplyTargetFeishuMeeting }},
		{"missing kind", func(_ *Config, target *state.ChannelReplyTarget) { target.Kind = "" }},
		{"invalid chat", func(_ *Config, target *state.ChannelReplyTarget) { target.ID = "oc_chat\n--as user" }},
		{"missing message", func(_ *Config, target *state.ChannelReplyTarget) { target.MessageID = "" }},
		{"meeting composite id", func(_ *Config, target *state.ChannelReplyTarget) { target.MessageID = "123:event:0" }},
		{"unsafe message", func(_ *Config, target *state.ChannelReplyTarget) { target.MessageID = "om_source$(secret)" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, target := fixtureConfig(), fixtureTarget()
			tc.mutate(&config, &target)
			client := &mockClient{}
			r := Reactor{Config: config, Client: client}
			if id, status, err := r.AddReaction(t.Context(), target, "已收到"); id != "" || status != Failed || err == nil {
				t.Fatal("invalid create was not rejected", id, status, err)
			}
			if status, err := r.RemoveReaction(t.Context(), target, "reaction_record-1"); status != Failed || err == nil || len(client.calls) != 0 {
				t.Fatal("invalid reaction target reached CLI", status, err, len(client.calls))
			}
		})
	}
	for _, label := range []string{"", "发送回复", "处理中 ", "Typing"} {
		client := &mockClient{}
		if id, status, err := (Reactor{Config: fixtureConfig(), Client: client}).AddReaction(t.Context(), fixtureTarget(), label); id != "" || status != Failed || err == nil || len(client.calls) != 0 {
			t.Fatal("unsupported stage reached CLI", label, id, status, err)
		}
	}
	for _, id := range []string{"", "om_source", "oc_chat", "record secret", "record\x00", strings.Repeat("x", maxReactionIDBytes+1)} {
		client := &mockClient{}
		if status, err := (Reactor{Config: fixtureConfig(), Client: client}).RemoveReaction(t.Context(), fixtureTarget(), id); status != Failed || err == nil || len(client.calls) != 0 {
			t.Fatal("invalid saved ID reached CLI", status, err)
		}
	}
}

func TestOpaqueReactionIDIsEncodedAsDataWithoutShellOrLookup(t *testing.T) {
	id := `reaction"quote\path$(literal)`
	var envelope map[string]any
	if err := json.Unmarshal([]byte(reactionAck), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["data"].(map[string]any)["reaction_id"] = id
	ack, _ := json.Marshal(envelope)
	client := &mockClient{out: feishumeeting.Output{Stdout: ack, Started: true}}
	r := Reactor{Config: fixtureConfig(), Client: client}
	if got, status, err := r.AddReaction(t.Context(), fixtureTarget(), "已收到"); got != id || status != Accepted || err != nil {
		t.Fatal("opaque provider ID was changed", got, status, err)
	}
	if status, err := r.RemoveReaction(t.Context(), fixtureTarget(), id); status != Accepted || err != nil || len(client.calls) != 2 {
		t.Fatal("opaque removal was not one mutation", status, err, len(client.calls))
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(client.calls[1][8]), &params); err != nil || len(params) != 2 || params["reaction_id"] != id || params["message_id"] != "om_source" {
		t.Fatal("provider ID was not literal JSON data", params, err)
	}
}

type canceledReactionClient struct{ calls int }

func (c *canceledReactionClient) Run(ctx context.Context, _ ...string) (feishumeeting.Output, error) {
	c.calls++
	<-ctx.Done()
	return feishumeeting.Output{Started: true}, ctx.Err()
}

func TestReactionCancellationKeepsStartedMutationUnknownAndDoesNotRetry(t *testing.T) {
	client := &canceledReactionClient{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	id, status, err := (Reactor{Config: fixtureConfig(), Client: client}).AddReaction(ctx, fixtureTarget(), "处理中")
	if id != "" || status != Unknown || err == nil || client.calls != 1 {
		t.Fatal("started mutation was retried or claimed rejected", id, status, err, client.calls)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if status, err := (Reactor{Config: fixtureConfig(), Client: client}).RemoveReaction(canceled, fixtureTarget(), "reaction_record-1"); status != Failed || err == nil || client.calls != 1 {
		t.Fatal("pre-canceled mutation started", status, err, client.calls)
	}
}
