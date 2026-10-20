package dingtalk

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNotificationUsesBotAndVerifiedRecipientWithOneMutation(t *testing.T) {
	for _, tc := range []struct {
		name, body, status string
		network            bool
	}{
		{"accepted", `{"processQueryKey":"platform-receipt"}`, Accepted, false},
		{"invalid recipient", `{"processQueryKey":"receipt","invalidStaffIdList":["alice"]}`, Failed, false},
		{"flow control", `{"flowControlledStaffIdList":["alice"]}`, Failed, false},
		{"missing acceptance", `{}`, Unknown, false},
		{"network ambiguity", ``, Unknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			r := newTestReactor(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == reactionTokenPath {
					return reactionResponse(req, 200, `{"accessToken":"`+reactionTestToken+`","expireIn":7200}`), nil
				}
				mutations++
				if req.URL.Host != "api.dingtalk.com" || req.URL.Path != "/v1.0/robot/oToMessages/batchSend" || req.Header.Get("x-acs-dingtalk-access-token") != reactionTestToken {
					t.Fatal("wrong bot endpoint")
				}
				var payload struct {
					RobotCode string   `json:"robotCode"`
					UserIDs   []string `json:"userIds"`
					MsgKey    string   `json:"msgKey"`
					MsgParam  string   `json:"msgParam"`
				}
				if json.NewDecoder(req.Body).Decode(&payload) != nil || len(payload.UserIDs) != 1 || payload.UserIDs[0] != "alice" || payload.RobotCode != "fixture-robot" || payload.MsgKey != "sampleMarkdown" || !strings.Contains(payload.MsgParam, "Task result") {
					t.Fatal(payload)
				}
				if tc.network {
					return nil, errors.New("SECRET network diagnostic")
				}
				return reactionResponse(req, 200, tc.body), nil
			})
			status, receipt, err := r.NotifyUser(t.Context(), "alice", "durable-key", "Task result")
			if status != tc.status || mutations != 1 || (err == nil) != (status == Accepted) || status == Accepted && receipt != "platform-receipt" || err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal(status, receipt, err, mutations)
			}
		})
	}
}
