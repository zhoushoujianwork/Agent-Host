package feishumeeting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func entryFixture(t *testing.T, kind string, body any) []byte {
	t.Helper()
	c := fixtureConfig()
	b, err := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{
		"event_id": "event-one", "event_type": kind, "app_id": c.AppID, "tenant_key": c.TenantID, "create_time": "1791777600123",
	}, "event": body})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func invitedEntryFixture() map[string]any {
	return map[string]any{
		"meeting": map[string]any{"id": json.Number("700000000000000001"), "meeting_no": "123456789"},
		"bot":     map[string]any{"id": map[string]string{"open_id": "ou_bot"}, "user_type": 10},
		"inviter": map[string]any{"id": map[string]string{"open_id": "ou_alice"}, "user_type": 1},
		"call_id": "call-one", "invite_time": "1791777600",
	}
}

func imEntryFixture(kind, content string) map[string]any {
	c := fixtureConfig()
	return map[string]any{
		"sender":  map[string]any{"sender_id": map[string]string{"open_id": "ou_alice"}, "sender_type": "user", "tenant_key": c.TenantID},
		"message": map[string]any{"message_id": "om_one", "chat_id": "oc_direct", "chat_type": "p2p", "message_type": kind, "content": content},
	}
}

func TestEntryInvitationRetainsExactIDsAndAuthenticatedActor(t *testing.T) {
	r := EntryReceiver{Config: fixtureConfig()}
	for _, shape := range []string{"object", "string", "direct"} {
		t.Run(shape, func(t *testing.T) {
			body := invitedEntryFixture()
			for key, id := range map[string]string{"bot": "ou_bot", "inviter": "ou_alice"} {
				actor := body[key].(map[string]any)
				if shape == "string" {
					actor["id"] = id
				}
				if shape == "direct" {
					delete(actor, "id")
					actor["open_id"] = id
				}
			}
			e, err := r.parseEntry(entryFixture(t, "vc.bot.meeting_invited_v1", body))
			if err != nil || e.Kind != EntryInvite || e.ID != "event-one" || e.MeetingID != "700000000000000001" || e.MeetingNumber != "123456789" || e.CallID != "call-one" || e.SenderOpenID != "ou_alice" || !e.At.Equal(time.Unix(1791777600, 0)) {
				t.Fatalf("invitation provenance changed: %+v %v", e, err)
			}
		})
	}
	for name, mutate := range map[string]func(map[string]any){
		"unlisted sender":        func(b map[string]any) { b["inviter"].(map[string]any)["id"] = "ou_unknown" },
		"bot inviter":            func(b map[string]any) { b["inviter"].(map[string]any)["user_type"] = 10 },
		"missing human type":     func(b map[string]any) { delete(b["inviter"].(map[string]any), "user_type") },
		"different bot":          func(b map[string]any) { b["bot"].(map[string]any)["id"] = "ou_other" },
		"bot in user namespace":  func(b map[string]any) { b["bot"].(map[string]any)["user_type"] = 1 },
		"user id is not open id": func(b map[string]any) { b["inviter"].(map[string]any)["id"] = map[string]string{"user_id": "ou_alice"} },
		"union id is not open id": func(b map[string]any) {
			b["inviter"].(map[string]any)["id"] = map[string]string{"union_id": "ou_alice"}
		},
		"conflicting actor ids": func(b map[string]any) { b["inviter"].(map[string]any)["open_id"] = "ou_bob" },
	} {
		t.Run(name, func(t *testing.T) {
			body := invitedEntryFixture()
			mutate(body)
			e, err := r.parseEntry(entryFixture(t, "vc.bot.meeting_invited_v1", body))
			if err != nil || e.Kind != "" {
				t.Fatalf("unauthorized invitation admitted: %+v %v", e, err)
			}
		})
	}
}

func TestEntryEnvelopeAndIdentifierFailuresStopIntake(t *testing.T) {
	r := EntryReceiver{Config: fixtureConfig()}
	valid := string(entryFixture(t, "vc.bot.meeting_invited_v1", invitedEntryFixture()))
	for name, raw := range map[string]string{
		"another app":                   strings.Replace(valid, `"app_id":"cli_test"`, `"app_id":"cli_other"`, 1),
		"another tenant":                strings.Replace(valid, `"tenant_key":"tenant-real-fixture"`, `"tenant_key":"another"`, 1),
		"missing id":                    strings.Replace(valid, `"event_id":"event-one"`, `"event_id":""`, 1),
		"invalid time":                  strings.Replace(valid, `"create_time":"1791777600123"`, `"create_time":true`, 1),
		"negative time":                 strings.Replace(valid, `"create_time":"1791777600123"`, `"create_time":"-1"`, 1),
		"float meeting id":              strings.Replace(valid, `"id":700000000000000001`, `"id":7e17`, 1),
		"bool meeting id":               strings.Replace(valid, `"id":700000000000000001`, `"id":true`, 1),
		"missing call id":               strings.Replace(valid, `"call_id":"call-one"`, `"call_id":""`, 1),
		"bad invite time":               strings.Replace(valid, `"invite_time":"1791777600"`, `"invite_time":"not-a-time"`, 1),
		"duplicate authorization field": strings.Replace(valid, `"app_id":"cli_test"`, `"app_id":"cli_test","APP_ID":"cli_test"`, 1),
		"trailing value":                valid + `{}`,
		"compact payload":               `{"event_type":"vc.bot.meeting_invited_v1","meeting_no":"123456789"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if e, err := r.parseEntry([]byte(raw)); err == nil || e.Kind != "" {
				t.Fatalf("invalid event accepted: %+v %v", e, err)
			}
		})
	}
	ended, err := r.parseEntry(entryFixture(t, "vc.bot.meeting_ended_v1", map[string]any{"meeting": map[string]any{"id": json.Number("700000000000000001")}}))
	if err != nil || ended.Kind != EntryEnd || ended.MeetingID != "700000000000000001" || ended.SenderOpenID != "" {
		t.Fatalf("end event invalid: %+v %v", ended, err)
	}
	activity, err := r.parseEntry(entryFixture(t, "vc.bot.meeting_activity_v1", map[string]any{"meeting_activity_items": []any{map[string]any{"chat_received_items": []any{map[string]any{"content": "AI: Do this"}}}}}))
	if err != nil || activity.Kind != "" {
		t.Fatalf("meeting activity was converted to an entry or task: %+v %v", activity, err)
	}
}

func TestEntryIMAdmitsOnlyExplicitPrivateMeetingRequests(t *testing.T) {
	r := EntryReceiver{Config: fixtureConfig()}
	for _, text := range []string{"123456789", "123 456 789", "123\u00a0456\u00a0789", "会议号：123456789", "会议 ID: 123 456 789", "/join 123456789", "join meeting 123456789", "加入会议 123456789", "https://vc.feishu.cn/j/123456789", "/join https://vc.feishu.cn/j/123456789"} {
		t.Run(text, func(t *testing.T) {
			content, _ := json.Marshal(map[string]string{"text": text})
			e, err := r.parseEntry(entryFixture(t, "im.message.receive_v1", imEntryFixture("text", string(content))))
			if err != nil || e.Kind != EntryMessage || e.MeetingNumber != "123456789" || e.SenderOpenID != "ou_alice" {
				t.Fatalf("explicit request rejected: %+v %v", e, err)
			}
		})
	}
	for _, text := range []string{
		"The invoice is 123456789", "Do not join 123456789", "123456789 987654321", "1234567890",
		"https://vc.feishu.cn.evil.test/j/123456789", "http://vc.feishu.cn/j/123456789", "https://vc.feishu.cn:443/j/123456789",
		"https://user@vc.feishu.cn/j/123456789", "https://vc.feishu.cn/j/opaque", "https://vc.feishu.cn/j/123456789?redirect=https://evil.test",
		"https://vc.feishu.cn/j/%31%32%33%34%35%36%37%38%39", "Don't join https://vc.feishu.cn/j/123456789",
	} {
		content, _ := json.Marshal(map[string]string{"text": text})
		e, err := r.parseEntry(entryFixture(t, "im.message.receive_v1", imEntryFixture("text", string(content))))
		if err != nil || e.Kind != "" {
			t.Fatalf("unrelated or ambiguous request admitted: %q %+v %v", text, e, err)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"group":    func(b map[string]any) { b["message"].(map[string]any)["chat_type"] = "group" },
		"app echo": func(b map[string]any) { b["sender"].(map[string]any)["sender_type"] = "app" },
		"self echo": func(b map[string]any) {
			b["sender"].(map[string]any)["sender_id"] = map[string]string{"open_id": "ou_bot"}
		},
		"unlisted human": func(b map[string]any) {
			b["sender"].(map[string]any)["sender_id"] = map[string]string{"open_id": "ou_unknown"}
		},
		"another tenant":          func(b map[string]any) { b["sender"].(map[string]any)["tenant_key"] = "another" },
		"unverified meeting card": func(b map[string]any) { b["message"].(map[string]any)["message_type"] = "meeting_card" },
	} {
		t.Run(name, func(t *testing.T) {
			body := imEntryFixture("text", `{"text":"123456789"}`)
			mutate(body)
			e, err := r.parseEntry(entryFixture(t, "im.message.receive_v1", body))
			if err != nil || e.Kind != "" {
				t.Fatalf("inadmissible IM event routed: %+v %v", e, err)
			}
		})
	}
}

func TestEntryVerifiedVideoChatCardUsesOnlyLiveMeetingNumber(t *testing.T) {
	// Same field shape as the first captured platform card, with synthetic data.
	const valid = `{"topic":"Synthetic meeting 987654321","meet_number":"123456789","start_time":"1791775000000","end_time":null}`
	r := EntryReceiver{Config: fixtureConfig()}
	for _, content := range []string{valid, strings.Replace(valid, `"end_time":null`, `"end_time":""`, 1)} {
		event, err := r.parseEntry(entryFixture(t, "im.message.receive_v1", imEntryFixture("video_chat", content)))
		if err != nil || event.Kind != EntryMessage || event.MeetingNumber != "123456789" || event.SenderOpenID != "ou_alice" {
			t.Fatalf("verified card shape was not admitted: %+v %v", event, err)
		}
	}
	for _, content := range []string{
		strings.Replace(valid, `"end_time":null`, `"end_time":"1791776000000"`, 1),
		strings.Replace(valid, `"end_time":null`, `"end_time":0`, 1),
		strings.Replace(valid, `,"end_time":null`, "", 1),
		strings.Replace(valid, `"meet_number":"123456789"`, `"meeting_no":"123456789"`, 1),
		strings.Replace(valid, `"meet_number":"123456789"`, `"meet_number":"bad"`, 1),
		strings.Replace(valid, `"meet_number":"123456789"`, `"meet_number":"123456789 987654321"`, 1),
	} {
		event, err := r.parseEntry(entryFixture(t, "im.message.receive_v1", imEntryFixture("video_chat", content)))
		if err != nil || event.Kind != "" {
			t.Fatalf("ended or unrecognized card admitted: %+v %v", event, err)
		}
	}
	for _, source := range []string{"group", "app"} {
		body := imEntryFixture("video_chat", valid)
		if source == "group" {
			body["message"].(map[string]any)["chat_type"] = "group"
		} else {
			body["sender"].(map[string]any)["sender_type"] = "app"
		}
		if event, err := r.parseEntry(entryFixture(t, "im.message.receive_v1", body)); err != nil || event.Kind != "" {
			t.Fatalf("card bypassed sender gate: %+v %v", event, err)
		}
	}
}

func TestEntryPostUsesOnlyBoundedTextAndOfficialLinks(t *testing.T) {
	for _, content := range []string{
		`{"title":"","content":[[{"tag":"text","text":"/join "},{"tag":"text","text":"123456789"}]]}`,
		`{"title":"","content":[[{"tag":"a","text":"Meeting","href":"https://vc.feishu.cn/j/123456789"}]]}`,
	} {
		if number, err := entryMessageNumber("post", content); err != nil || number != "123456789" {
			t.Fatalf("post entry rejected: %q %v", number, err)
		}
	}
	for _, content := range []string{
		`{"title":"Do not join","content":[[{"tag":"text","text":"123456789"}]]}`,
		`{"content":[[{"tag":"a","href":"https://evil.test/j/123456789"}]]}`,
		`{"content":[[{"tag":"img","text":"123456789"}]]}`,
		`{"en_us":{"title":"","content":[[{"tag":"text","text":"123456789"}]]}}`,
	} {
		if number, err := entryMessageNumber("post", content); err != nil || number != "" {
			t.Fatalf("unsupported post interpreted as a request: %q %v", number, err)
		}
	}
	if _, err := entryMessageNumber("text", `{"text":"123456789","Text":"123456789"}`); err == nil {
		t.Fatal("duplicate text accepted")
	}
}

func entryScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-lark-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func entryShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func TestEntryReceiverIgnoresPrematureConnectedAndSanitizesLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	binary := entryScript(t, `printf '%s\n' 'Connected. Waiting for events... (Ctrl+C to stop)' '[SDK Info] disconnected to wss://secret.invalid?token=never-print' '[SDK Warn] token=never-print' >&2
read ignored`)
	var kinds, logs []string
	err := (EntryReceiver{Config: fixtureConfig(), Binary: binary}).Run(ctx, func(context.Context, EntryEvent) error { t.Fatal("unexpected event"); return nil }, func(kind, text string) error {
		kinds, logs = append(kinds, kind), append(logs, text)
		if kind == "warning" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(kinds, []string{"connecting", "disconnected", "warning"}) || strings.Contains(strings.Join(logs, " "), "never-print") {
		t.Fatalf("readiness/logging incorrect: %v %v", kinds, err)
	}
}

func TestEntryReceiverReadyKeepsStdinOpenThenCancelsAndReaps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidPath := filepath.Join(t.TempDir(), "pid")
	argsPath := filepath.Join(t.TempDir(), "args")
	body := "printf '%s' \"$$\" > " + entryShellQuote(pidPath) + "\nprintf '%s\\n' \"$@\" > " + entryShellQuote(argsPath) + "\nprintf '%s\\n' '[SDK Info] connected to wss://secret.invalid?token=never-print [conn_id=private]' >&2\nread ignored\nexit 72"
	connected := make(chan struct{})
	done := make(chan error, 1)
	r := EntryReceiver{Config: fixtureConfig(), Binary: entryScript(t, body)}
	go func() {
		done <- r.Run(ctx, func(context.Context, EntryEvent) error { return nil }, func(kind, text string) error {
			if strings.Contains(text, "never-print") {
				return errors.New("raw credential disclosed")
			}
			if kind == "connected" {
				close(connected)
			}
			return nil
		})
	}()
	select {
	case <-connected:
	case err := <-done:
		t.Fatalf("exited before ready: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("fixture did not become ready")
	}
	select {
	case err := <-done:
		t.Fatalf("stdin was closed while subscription active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not reap subscriber")
	}
	pidRaw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(pidRaw), &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("subscriber process still exists: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	want := []string{"--profile", r.Config.Profile, "event", "+subscribe", "--as", "bot", "--event-types", entryTypes}
	if err != nil || !reflect.DeepEqual(strings.Split(strings.TrimSpace(string(args)), "\n"), want) {
		t.Fatalf("unsafe subscription arguments: %s %v", args, err)
	}
}

func TestEntryReceiverPreservesBurstOrderingAndStopsOnHandlerFailure(t *testing.T) {
	var script strings.Builder
	for i := 0; i < 100; i++ {
		raw := string(entryFixture(t, "vc.bot.meeting_invited_v1", invitedEntryFixture()))
		raw = strings.Replace(raw, "event-one", fmt.Sprintf("event-%03d", i), 1)
		fmt.Fprintf(&script, "printf '%%s\\n' %s\n", entryShellQuote(raw))
	}
	script.WriteString("read ignored\n")
	var received []string
	ready := 0
	err := (EntryReceiver{Config: fixtureConfig(), Binary: entryScript(t, script.String())}).Run(context.Background(), func(_ context.Context, e EntryEvent) error {
		received = append(received, e.ID)
		if len(received) == 100 {
			return errors.New("private-handler-detail")
		}
		return nil
	}, func(kind, _ string) error {
		if kind == "connected" {
			ready++
		}
		return nil
	})
	if err == nil || strings.Contains(err.Error(), "private-handler-detail") || len(received) != 100 || ready != 1 {
		t.Fatalf("burst lost, failure hidden, or readiness incorrect: count=%d ready=%d err=%v", len(received), ready, err)
	}
	for i, id := range received {
		if id != fmt.Sprintf("event-%03d", i) {
			t.Fatalf("event order changed at %d", i)
		}
	}
}

func TestEntryReceiverRejectsOversizedMalformedAndFailedStreams(t *testing.T) {
	passive := string(entryFixture(t, "im.message.reaction.created_v1", map[string]any{}))
	for name, script := range map[string]string{
		"oversized stdout":          "printf '%s\\n' " + entryShellQuote(strings.Repeat("x", entryMaxLine+1)) + "\nread ignored",
		"oversized stderr":          "printf '%s\\n' " + entryShellQuote(strings.Repeat("x", 32*1024+1)) + " >&2\nread ignored",
		"malformed JSON":            "printf '%s\\n' '{invalid-secret-value}'\nread ignored",
		"SDK error":                 "printf '%s\\n' '[SDK Error] private-secret-value' >&2\nread ignored",
		"passive wrong app":         "printf '%s\\n' " + entryShellQuote(strings.Replace(passive, `"app_id":"cli_test"`, `"app_id":"cli_other"`, 1)) + "\nread ignored",
		"passive malformed payload": "printf '%s\\n' " + entryShellQuote(strings.Replace(passive, `"event":{}`, `"event":null`, 1)) + "\nread ignored",
		"exit failure":              "printf '%s\\n' 'private-secret-value' >&2\nexit 71",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := (EntryReceiver{Config: fixtureConfig(), Binary: entryScript(t, script)}).Run(ctx, func(context.Context, EntryEvent) error { t.Fatal("invalid stream emitted an event"); return nil }, func(string, string) error { return nil })
			if err == nil || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("stream failure not safely surfaced: %v", err)
			}
			if name == "exit failure" && (!strings.Contains(err.Error(), "exit 71") || !strings.Contains(err.Error(), "app lock")) {
				t.Fatalf("subscriber exit lost actionable sanitized diagnostics: %v", err)
			}
		})
	}
}

func TestEntryReceiverPassiveEventsKeepIMStreamAliveWithoutDispatch(t *testing.T) {
	passiveTypes := []string{
		"vc.bot.meeting_activity_v1",
		"im.chat.member.bot.added_v1", "im.chat.member.bot.deleted_v1",
		"im.message.message_read_v1",
		"im.message.reaction.created_v1", "im.message.reaction.deleted_v1",
		"drive.notice.comment_add_v1",
		"vc.meeting.participant_meeting_ended_v1", "vc.note.generated_v1", "minutes.minute.generated_v1",
	}
	cfg := fixtureConfig()
	cfg.AutoJoin, cfg.IMChat, cfg.OwnerOpenID = true, true, "ou_alice"
	argsPath := filepath.Join(t.TempDir(), "args")
	var script strings.Builder
	fmt.Fprintf(&script, "printf '%%s\\n' \"$@\" > %s\n", entryShellQuote(argsPath))
	for i, kind := range passiveTypes {
		// Even an auxiliary payload resembling a request is never task input.
		body := imEntryFixture("text", `{"text":"123456789"}`)
		for key, value := range invitedEntryFixture() {
			body[key] = value
		}
		raw := strings.Replace(string(entryFixture(t, kind, body)), "event-one", fmt.Sprintf("passive-%d", i), 1)
		fmt.Fprintf(&script, "printf '%%s\\n' %s\n", entryShellQuote(raw))
	}
	question := entryFixture(t, "im.message.receive_v1", imEntryFixture("text", `{"text":"What is the next step?"}`))
	fmt.Fprintf(&script, "printf '%%s\\n' %s\nread ignored\n", entryShellQuote(string(question)))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entryCalls, imCalls, ready := 0, 0, 0
	r := EntryReceiver{Config: cfg, Binary: entryScript(t, script.String()), HandleIM: func(_ context.Context, raw []byte) error {
		imCalls++
		if string(raw) != string(question) {
			t.Error("passive event reached IM handler")
		}
		cancel()
		return nil
	}}
	err := r.Run(ctx, func(context.Context, EntryEvent) error { entryCalls++; return nil }, func(kind, _ string) error {
		if kind == "connected" {
			ready++
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || entryCalls != 0 || imCalls != 1 || ready != 1 {
		t.Fatalf("passive events interrupted or dispatched work: entry=%d im=%d ready=%d err=%v", entryCalls, imCalls, ready, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(argv) != 8 || !reflect.DeepEqual(argv[:7], []string{"--profile", cfg.Profile, "event", "+subscribe", "--as", "bot", "--event-types"}) {
		t.Fatalf("subscription command changed: %q", argv)
	}
	registered := strings.Split(argv[7], ",")
	want := append([]string{"vc.bot.meeting_invited_v1", "vc.bot.meeting_ended_v1", "im.message.receive_v1"}, passiveTypes...)
	if len(registered) != len(want) {
		t.Fatalf("subscriber registered %d event types; want %d", len(registered), len(want))
	}
	for _, kind := range want {
		count := 0
		for _, got := range registered {
			if got == kind {
				count++
			}
		}
		if count != 1 {
			t.Errorf("subscriber must register %s exactly once; got %d", kind, count)
		}
	}
}

func TestEntryPassiveEventsRequireAuthenticatedObjectEnvelopes(t *testing.T) {
	for _, kind := range []string{
		"vc.bot.meeting_activity_v1",
		"im.chat.member.bot.added_v1", "im.chat.member.bot.deleted_v1",
		"im.message.message_read_v1", "im.message.reaction.created_v1", "im.message.reaction.deleted_v1",
		"drive.notice.comment_add_v1", "vc.meeting.participant_meeting_ended_v1", "vc.note.generated_v1", "minutes.minute.generated_v1",
	} {
		t.Run(kind, func(t *testing.T) {
			valid := string(entryFixture(t, kind, map[string]any{}))
			for name, raw := range map[string]string{
				"schema":    strings.Replace(valid, `"schema":"2.0"`, `"schema":"1.0"`, 1),
				"app":       strings.Replace(valid, `"app_id":"cli_test"`, `"app_id":"cli_other"`, 1),
				"tenant":    strings.Replace(valid, `"tenant_key":"tenant-real-fixture"`, `"tenant_key":"another"`, 1),
				"event id":  strings.Replace(valid, `"event_id":"event-one"`, `"event_id":""`, 1),
				"timestamp": strings.Replace(valid, `"create_time":"1791777600123"`, `"create_time":true`, 1),
				"null":      strings.Replace(valid, `"event":{}`, `"event":null`, 1),
				"array":     strings.Replace(valid, `"event":{}`, `"event":[]`, 1),
				"string":    strings.Replace(valid, `"event":{}`, `"event":"private-secret-value"`, 1),
				"missing":   strings.Replace(valid, `"event":{},`, "", 1),
			} {
				t.Run(name, func(t *testing.T) {
					e, err := (EntryReceiver{Config: fixtureConfig()}).parseEntry([]byte(raw))
					if err == nil || e != (EntryEvent{}) || strings.Contains(err.Error(), "private-secret-value") {
						t.Fatalf("invalid passive event was not safely rejected: event=%+v err=%v", e, err)
					}
				})
			}
		})
	}
}

func TestEntryReceiverDispatchesMeetingEntryBeforeOrdinaryIM(t *testing.T) {
	cfg := fixtureConfig()
	cfg.AutoJoin, cfg.IMChat, cfg.OwnerOpenID = true, true, "ou_alice"
	frames := []struct {
		id, kind string
		body     any
	}{
		{"invite", "vc.bot.meeting_invited_v1", invitedEntryFixture()},
		{"number", "im.message.receive_v1", imEntryFixture("text", `{"text":"123456789"}`)},
		{"card", "im.message.receive_v1", imEntryFixture("video_chat", `{"meet_number":"123456789","end_time":null}`)},
		{"question", "im.message.receive_v1", imEntryFixture("text", `{"text":"What is the next step?"}`)},
		{"activity", "vc.bot.meeting_activity_v1", map[string]any{}},
		{"end", "vc.bot.meeting_ended_v1", map[string]any{"meeting": map[string]any{"id": "700000000000000001"}}},
	}
	var script strings.Builder
	var question []byte
	for _, frame := range frames {
		raw := strings.Replace(string(entryFixture(t, frame.kind, frame.body)), "event-one", frame.id, 1)
		fmt.Fprintf(&script, "printf '%%s\\n' %s\n", entryShellQuote(raw))
		if frame.id == "question" {
			question = []byte(raw)
		}
	}
	script.WriteString("read ignored\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var dispatched []string
	var imFrames [][]byte
	r := EntryReceiver{Config: cfg, Binary: entryScript(t, script.String()), HandleIM: func(_ context.Context, raw []byte) error {
		dispatched = append(dispatched, "im")
		imFrames = append(imFrames, append([]byte(nil), raw...))
		return nil
	}}
	err := r.Run(ctx, func(_ context.Context, event EntryEvent) error {
		dispatched = append(dispatched, event.Kind+":"+event.ID)
		if event.Kind == EntryEnd {
			cancel()
		}
		return nil
	}, func(string, string) error { return nil })
	want := []string{"invite:invite", "message:number", "message:card", "im", "end:end"}
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("entry precedence or stream ordering changed: %v, err=%v", dispatched, err)
	}
	if len(imFrames) != 1 || string(imFrames[0]) != string(question) {
		t.Fatal("ordinary IM must reach its handler exactly once with its original envelope")
	}
}

func TestEntryReceiverWithoutAutoJoinDispatchesPrivateNumberToIM(t *testing.T) {
	cfg := fixtureConfig()
	cfg.AutoJoin, cfg.IMChat, cfg.OwnerOpenID = false, true, "ou_alice"
	raw := entryFixture(t, "im.message.receive_v1", imEntryFixture("text", `{"text":"123456789"}`))
	script := "printf '%s\\n' " + entryShellQuote(string(raw)) + "\nread ignored\n"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entryCalls, imCalls := 0, 0
	var received []byte
	r := EntryReceiver{Config: cfg, Binary: entryScript(t, script), HandleIM: func(_ context.Context, frame []byte) error {
		imCalls++
		received = append([]byte(nil), frame...)
		cancel()
		return nil
	}}
	err := r.Run(ctx, func(context.Context, EntryEvent) error {
		entryCalls++
		return nil
	}, func(string, string) error { return nil })
	if !errors.Is(err, context.Canceled) || entryCalls != 0 || imCalls != 1 || string(received) != string(raw) {
		t.Fatalf("disabled auto-join consumed numeric IM: entries=%d im=%d err=%v", entryCalls, imCalls, err)
	}
}

func TestEntryReceiverIMHandlerFailureStopsAndReapsWithoutLeaking(t *testing.T) {
	cfg := fixtureConfig()
	cfg.IMChat, cfg.OwnerOpenID = true, "ou_alice"
	pidPath := filepath.Join(t.TempDir(), "pid")
	raw := string(entryFixture(t, "im.message.receive_v1", imEntryFixture("text", `{"text":"private-source-content"}`)))
	script := "printf '%s' \"$$\" > " + entryShellQuote(pidPath) + "\nprintf '%s\\n' " + entryShellQuote(raw) + " " + entryShellQuote(strings.Replace(raw, "event-one", "event-two", 1)) + "\nread ignored\n"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entryCalls, imCalls := 0, 0
	var observations []string
	privateError := errors.New("private-source-content token=private-handler-token")
	r := EntryReceiver{Config: cfg, Binary: entryScript(t, script), HandleIM: func(context.Context, []byte) error {
		imCalls++
		return privateError
	}}
	err := r.Run(ctx, func(context.Context, EntryEvent) error {
		entryCalls++
		return nil
	}, func(_, message string) error {
		observations = append(observations, message)
		return nil
	})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, privateError) || err.Error() != "Feishu IM event could not be durably handled; subscription stopped" || entryCalls != 0 || imCalls != 1 {
		t.Fatalf("IM persistence failure did not stop intake safely: entries=%d im=%d err=%v", entryCalls, imCalls, err)
	}
	for _, marker := range []string{"private-source-content", "private-handler-token"} {
		if strings.Contains(err.Error()+strings.Join(observations, " "), marker) {
			t.Fatal("private IM failure details escaped into public diagnostics")
		}
	}
	pidRaw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(pidRaw), &pid); err != nil || pid <= 0 {
		t.Fatalf("subscriber PID unavailable: %v", err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("failed IM subscriber was not reaped: %v", err)
	}
}
