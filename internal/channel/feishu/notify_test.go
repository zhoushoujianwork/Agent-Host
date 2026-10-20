package feishu

import (
	"strings"
	"testing"

	"agent-host/internal/channel/feishumeeting"
)

func TestNotificationUsesBotUserDestinationAndStableID(t *testing.T) {
	for _, tc := range []struct {
		name, body, status string
		started            bool
	}{
		{"accepted", successAck, Accepted, true},
		{"did not start", "", Failed, false},
		{"uncertain", `{}`, Unknown, true},
		{"rejected", `{"ok":false,"identity":"bot","error":{"type":"permission"}}`, Failed, true},
		{"wrong identity", strings.Replace(successAck, `"bot"`, `"user"`, 1), Unknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockClient{out: feishumeeting.Output{Started: tc.started, Stdout: []byte(tc.body)}}
			status, receipt, err := (Replier{Config: fixtureConfig(), Client: client}).NotifyUser(t.Context(), "ou_alice", "durable-delivery", "Task result $(literal)")
			if status != tc.status || (err == nil) != (status == Accepted) || status == Accepted && receipt != "om_new" || len(client.calls) != 1 {
				t.Fatal(status, receipt, err)
			}
			args := client.calls[0]
			for _, want := range []string{"+messages-send", "bot", "--user-id", "ou_alice", "durable-delivery"} {
				found := false
				for _, arg := range args {
					found = found || arg == want
				}
				if !found {
					t.Fatal("wrong notification identity/destination", args)
				}
			}
		})
	}
}
