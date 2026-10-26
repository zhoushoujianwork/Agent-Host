package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testMeeting = "700000000000000001"

type response struct {
	Output
	err error
}
type scriptedTransport struct {
	responses []response
	calls     [][]string
}

func (s *scriptedTransport) Run(_ context.Context, args ...string) (Output, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	if len(s.calls) > len(s.responses) {
		return Output{}, errors.New("unexpected command")
	}
	r := s.responses[len(s.calls)-1]
	return r.Output, r.err
}
func result(raw string) response { return response{Output: Output{Stdout: []byte(raw), Started: true}} }
func clientFor(t *testing.T, responses ...response) (*MeetingClient, *scriptedTransport) {
	t.Helper()
	transport := &scriptedTransport{responses: responses}
	c, err := NewMeetingClient(Config{Profile: "fixture", AppID: "cli_app", BotOpenID: "ou_bot"}, transport)
	if err != nil {
		t.Fatal(err)
	}
	return c, transport
}
func arg(args []string, name string) string {
	for i, value := range args {
		if value == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestMutationOutcomesNeverRetryOrLeakDiagnostics(t *testing.T) {
	for name, tc := range map[string]struct {
		response response
		outcome  string
	}{
		"accepted":                  {result(`{"ok":true,"data":{}}`), Accepted},
		"not started":               {response{err: errors.New("private-secret")}, Failed},
		"authorization":             {result(`{"ok":false,"error":{"type":"authorization","message":"private-secret"}}`), Failed},
		"network":                   {result(`{"ok":false,"error":{"type":"network","message":"private-secret"}}`), Unknown},
		"missing ack":               {result(`{"ok":true}`), Unknown},
		"null ack":                  {result(`{"ok":true,"data":null}`), Unknown},
		"wrong UUID":                {result(`{"ok":true,"data":{"uuid":"other"}}`), Unknown},
		"malformed":                 {result(`private-secret`), Unknown},
		"trailing JSON":             {result(`{"ok":true,"data":{}} {}`), Unknown},
		"process failure after ack": {response{Output: Output{Stdout: []byte(`{"ok":true,"data":{}}`), Started: true}, err: errors.New("private-secret")}, Unknown},
	} {
		t.Run(name, func(t *testing.T) {
			c, transport := clientFor(t, tc.response)
			r, err := c.SendText(context.Background(), testMeeting, "saved-uuid", "Caller prefix: $(literal)")
			if r.Outcome != tc.outcome || (err == nil) != (tc.outcome == Accepted) || len(transport.calls) != 1 {
				t.Fatalf("result=%+v err=%v calls=%v", r, err, transport.calls)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("diagnostic leaked")
			}
			if arg(transport.calls[0], "--text") != "Caller prefix: $(literal)" || arg(transport.calls[0], "--profile") != "fixture" || arg(transport.calls[0], "--meeting-id") != testMeeting {
				t.Fatal("caller text or destination changed")
			}
		})
	}
}

func TestJoinVerifiesAcknowledgedIdentityAndForwardsCallID(t *testing.T) {
	for _, id := range []string{testMeeting, "700000000000000002"} {
		c, transport := clientFor(t, result(`{"ok":true,"data":{"meeting":{"id":"`+id+`","meeting_no":"123456789"}}}`))
		r, err := c.Join(context.Background(), JoinRequest{MeetingNumber: "123456789", ExpectedMeetingID: testMeeting, CallID: "invitation-call"})
		if (err == nil) != (id == testMeeting) || (r.Outcome == Accepted) != (id == testMeeting) || arg(transport.calls[0], "--call-id") != "invitation-call" {
			t.Fatalf("result=%+v err=%v", r, err)
		}
	}
}

func TestMembershipDoesNotTreatUncertaintyAsAbsence(t *testing.T) {
	for name, tc := range map[string]struct {
		meeting string
		status  string
		wantErr bool
	}{
		"present":      {`"status":2,"participants":[{"id":"ou_bot","status":"2"}]`, "present", false},
		"absent":       {`"status":2,"participants":[]`, "absent", false},
		"departed":     {`"status":2,"participants":[{"id":"ou_bot","status":4}]`, "absent", false},
		"unsettled":    {`"status":2,"participants":[{"id":"ou_bot","status":1}]`, Unknown, true},
		"missing list": {`"status":2`, Unknown, true},
		"null list":    {`"status":2,"participants":null`, Unknown, true},
		"ended":        {`"status":3`, "ended", false},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := clientFor(t, result(`{"ok":true,"data":{"meeting":{"id":"`+testMeeting+`",`+tc.meeting+`}}}`))
			m, err := c.Membership(context.Background(), testMeeting)
			if m.Status != tc.status || (err != nil) != tc.wantErr {
				t.Fatalf("membership=%+v err=%v", m, err)
			}
		})
	}
}

func TestIdentityProbeRejectsWrongAppAndUnavailableBot(t *testing.T) {
	for _, raw := range []string{
		`{"profile":"fixture","appId":"cli_other","identity":"bot","available":true}`,
		`{"profile":"fixture","appId":"cli_app","identity":"user","available":true}`,
		`{"profile":"fixture","appId":"cli_app","identity":"bot","available":false}`,
		`{"profile":"other","appId":"cli_app","identity":"bot","available":true}`,
		`{"profile":"fixture","appId":"cli_app","identity":"bot","available":true} {}`,
	} {
		c, _ := clientFor(t, result(raw))
		if c.CheckIdentity(context.Background()) == nil {
			t.Fatal("accepted mismatched identity")
		}
	}
	c, transport := clientFor(t, result(`{"profile":"fixture","appId":"cli_app","identity":"bot","available":true}`))
	if err := c.CheckIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.calls[0], []string{"--profile", "fixture", "whoami", "--as", "bot"}) {
		t.Fatal(transport.calls)
	}
}

func TestInvalidInputsAndCancellationNeverInvokeTransport(t *testing.T) {
	c, transport := clientFor(t)
	ctx := context.Background()
	checks := []func() error{
		func() error { _, e := c.Join(ctx, JoinRequest{MeetingNumber: "123"}); return e },
		func() error { _, e := c.Join(ctx, JoinRequest{MeetingNumber: "123456789", CallID: "bad\n"}); return e },
		func() error { _, e := c.Leave(ctx, "123456789"); return e },
		func() error { _, e := c.SendText(ctx, testMeeting, "", "text"); return e },
		func() error { _, e := c.SendText(ctx, testMeeting, "uuid", strings.Repeat("x", 48*1024+1)); return e },
		func() error { _, e := c.PollEvents(ctx, testMeeting, "bad\x00cursor"); return e },
	}
	for _, check := range checks {
		if check() == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, e1 := c.Join(ctx, JoinRequest{MeetingNumber: "123456789"})
	_, e2 := c.Leave(ctx, testMeeting)
	_, e3 := c.SendText(ctx, testMeeting, "uuid", "text")
	_, e4 := c.Membership(ctx, testMeeting)
	_, e5 := c.PollEvents(ctx, testMeeting, "")
	for _, err := range []error{e1, e2, e3, e4, e5, c.CheckIdentity(ctx)} {
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if len(transport.calls) != 0 {
		t.Fatal("invalid/cancelled input launched transport")
	}
}

func TestLeaveTargetsOnlySelectedMeeting(t *testing.T) {
	c, transport := clientFor(t, result(`{"ok":true,"data":{}}`))
	r, err := c.Leave(context.Background(), testMeeting)
	if err != nil || r.Outcome != Accepted || !reflect.DeepEqual(transport.calls[0], []string{"--profile", "fixture", "vc", "+meeting-leave", "--as", "bot", "--meeting-id", testMeeting, "--format", "json"}) {
		t.Fatalf("%+v %v %v", r, err, transport.calls)
	}
}

func TestPollValidatesWholePageAndPreservesUnknownEvents(t *testing.T) {
	data := map[string]any{"identity": map[string]any{"id": "ou_bot"}, "page_token": "next", "has_more": true,
		"events": []any{map[string]any{"event_id": "source", "event_type": "future_kind", "payload": map[string]any{"unknown": "retained"}}}}
	for _, mutation := range []string{"valid", "wrong bot", "wrong meeting", "wrong event meeting", "stalled cursor", "missing events", "bad event id"} {
		t.Run(mutation, func(t *testing.T) {
			raw, _ := json.Marshal(data)
			var d map[string]any
			_ = json.Unmarshal(raw, &d)
			switch mutation {
			case "wrong bot":
				d["identity"] = map[string]any{"id": "ou_other"}
			case "wrong meeting":
				d["meeting"] = map[string]any{"id": "700000000000000002"}
			case "wrong event meeting":
				d["events"].([]any)[0].(map[string]any)["payload"] = map[string]any{"meeting": map[string]any{"id": "700000000000000002"}}
			case "stalled cursor":
				d["page_token"] = "previous"
			case "missing events":
				delete(d, "events")
			case "bad event id":
				d["events"].([]any)[0].(map[string]any)["event_id"] = ""
			}
			raw, _ = json.Marshal(map[string]any{"ok": true, "data": d})
			c, transport := clientFor(t, result(string(raw)))
			page, err := c.PollEvents(context.Background(), testMeeting, "previous")
			if mutation != "valid" {
				if err == nil || len(page.Events) != 0 {
					t.Fatal("invalid page delivered", page, err)
				}
				return
			}
			if err != nil || page.Cursor != "next" || !page.HasMore || len(page.Events) != 1 || !strings.Contains(string(page.Events[0].Payload), "retained") || arg(transport.calls[0], "--page-token") != "previous" {
				t.Fatalf("page=%+v err=%v", page, err)
			}
		})
	}
}

func TestEnvelopeAndOutputBounds(t *testing.T) {
	for _, raw := range []string{`{}`, `{"ok":true} {"ok":true}`, `{"ok":"yes"}`, `null`} {
		if _, err := decodeEnvelope(Output{Stdout: []byte(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if e, err := decodeEnvelope(Output{Stderr: []byte(`{"ok":false,"error":{"type":"authorization"}}`)}); err != nil || *e.OK {
		t.Fatal("stderr rejection lost")
	}
	b := limitedBuffer{limit: 4}
	if n, err := b.Write([]byte("123456")); n != 6 || err != nil || !b.overflow || b.String() != "1234" {
		t.Fatal("output bound failed")
	}
}
