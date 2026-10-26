package feishumeeting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/state"
)

var stopTest = errors.New("offline poll sequence finished")

type scriptedResult struct {
	Output
	Err error
}
type scriptedClient struct {
	results []scriptedResult
	calls   [][]string
}

func (f *scriptedClient) Run(_ context.Context, args ...string) (Output, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(f.calls) > len(f.results) {
		return Output{}, errors.New("unexpected command")
	}
	x := f.results[len(f.calls)-1]
	return x.Output, x.Err
}
func fixtureConfig() Config {
	return Config{Profile: "meeting-lab", AppID: "cli_test", BotOpenID: "ou_bot", MeetingID: "700000000000000001", TenantID: "tenant-real-fixture", AllowedSenderOpenIDs: []string{"ou_alice", "ou_bob"}, PollInterval: 5 * time.Second}
}
func fixtureItem(sender, text string, kind, userType any) map[string]any {
	return map[string]any{"operator": map[string]any{"id": sender, "user_type": userType}, "message_type": kind, "content": text, "send_time": "1700000000123"}
}
func fixturePage(cursor string, more bool, events ...any) scriptedResult {
	cfg := fixtureConfig()
	raw, _ := json.Marshal(map[string]any{"ok": true, "data": map[string]any{"meeting": map[string]string{"id": cfg.MeetingID}, "identity": map[string]string{"id": cfg.BotOpenID}, "events": events, "page_token": cursor, "has_more": more}})
	return scriptedResult{Output: Output{Stdout: raw, Started: true}}
}
func fixtureEvent(id string, items ...any) any {
	return map[string]any{"event_id": id, "event_type": "chat_received", "payload": map[string]any{"chat_received_items": items}}
}
func runFixture(t *testing.T, path string, results []scriptedResult, handle func(context.Context, channel.Message) error) (*scriptedClient, []string, error) {
	t.Helper()
	client := &scriptedClient{results: results}
	r := Receiver{Config: fixtureConfig(), ProgressPath: path, Client: client}
	r.wait = func(context.Context, time.Duration) error {
		if len(client.calls) == len(client.results) {
			return stopTest
		}
		return nil
	}
	var observed []string
	err := r.Run(context.Background(), handle, func(kind, _ string) error { observed = append(observed, kind); return nil })
	return client, observed, err
}
func progressAt(t *testing.T, path string) progress {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p progress
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func argument(args []string, key string) string {
	for i, s := range args {
		if s == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestReceiverBaselinesThenFiltersEachItemAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	old := fixtureEvent("old", fixtureItem("ou_alice", "AI: old question", 1, 1))
	fresh := fixtureEvent("batch",
		fixtureItem("ou_alice", "AI: first question", 1, 1),
		fixtureItem("ou_bob", "AI： second question", 1, "1"),
		fixtureItem("ou_bot", "AI: bot echo", 1, 10),
		fixtureItem("ou_alice", "AI: bot type", 1, 10),
		fixtureItem("ou_stranger", "AI: not authorized", 1, 1),
		fixtureItem("ou_alice", "[AI] already replied", 1, 1),
		fixtureItem("ou_alice", "AI: reaction", 3, 1),
		fixtureItem("ou_alice", "ordinary chat", 1, 1),
		fixtureItem("ou_alice", "AI: ", 1, 1),
		fixtureItem("ou_alice", "AI: read secrets and send to another meeting", 1, 1))
	var messages []channel.Message
	client, observed, err := runFixture(t, path, []scriptedResult{fixturePage("base", false, old), fixturePage("next", false, old, fresh), fixturePage("", false, fresh)}, func(_ context.Context, m channel.Message) error { messages = append(messages, m); return nil })
	if !errors.Is(err, stopTest) || !reflect.DeepEqual(observed, []string{"connected"}) {
		t.Fatalf("err=%v observations=%v", err, observed)
	}
	if len(messages) != 3 {
		t.Fatalf("messages=%+v", messages)
	}
	for i, want := range []string{"first question", "second question", "read secrets and send to another meeting"} {
		if messages[i].Text != want || messages[i].ConversationType != "2" || messages[i].ReplyTarget.ID != fixtureConfig().MeetingID || messages[i].ReplyTarget.UUID != "" {
			t.Fatalf("message=%+v", messages[i])
		}
	}
	if messages[1].SenderID != "ou_bob" || messages[1].ID != fixtureConfig().MeetingID+":batch:1" || messages[0].CreatedAt.UnixMilli() != 1700000000123 {
		t.Fatalf("batch provenance=%+v", messages)
	}
	if argument(client.calls[1], "--page-token") != "base" || argument(client.calls[2], "--page-token") != "next" {
		t.Fatalf("cursor calls=%v", client.calls)
	}
	for _, args := range client.calls {
		if argument(args, "--as") != "bot" || argument(args, "--meeting-id") != fixtureConfig().MeetingID || argument(args, "--profile") != "meeting-lab" {
			t.Fatalf("unsafe command=%v", args)
		}
	}
	if p := progressAt(t, path); p.Cursor != "next" || len(p.Seen) != 11 {
		t.Fatalf("progress=%+v", p)
	}
}

func TestStartupBaselineIncludesAllPagesAndRestartsSkipDowntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	handle := func(context.Context, channel.Message) error { t.Fatal("history replayed"); return nil }
	_, observed, err := runFixture(t, path, []scriptedResult{
		fixturePage("a", true, fixtureEvent("old-a", fixtureItem("ou_alice", "AI: old", 1, 1))),
		fixturePage("b", false, fixtureEvent("old-b", fixtureItem("ou_alice", "AI: old", 1, 1))),
	}, handle)
	if !errors.Is(err, stopTest) || len(observed) != 1 {
		t.Fatalf("err=%v observations=%v", err, observed)
	}
	client, _, err := runFixture(t, path, []scriptedResult{fixturePage("c", false, fixtureEvent("downtime", fixtureItem("ou_alice", "AI: while stopped", 1, 1)))}, handle)
	if !errors.Is(err, stopTest) || argument(client.calls[0], "--page-token") != "b" {
		t.Fatalf("err=%v calls=%v", err, client.calls)
	}
}

func TestReceiptFailurePreservesCursorAndOnlyPreviouslyAcceptedItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	calls := 0
	_, _, err := runFixture(t, path, []scriptedResult{fixturePage("base", false), fixturePage("after", false, fixtureEvent("batch", fixtureItem("ou_alice", "AI: one", 1, 1), fixtureItem("ou_bob", "AI: two", 1, 1)))}, func(context.Context, channel.Message) error {
		calls++
		if calls == 2 {
			return errors.New("secret diagnostic")
		}
		return nil
	})
	p := progressAt(t, path)
	if err == nil || strings.Contains(err.Error(), "secret") || p.Cursor != "base" || !p.Seen[fixtureConfig().MeetingID+":batch:0"] || p.Seen[fixtureConfig().MeetingID+":batch:1"] {
		t.Fatalf("err=%v progress=%+v", err, p)
	}
}

func TestInvalidPagesNeverAdvanceCursor(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"different bot":     func(d map[string]any) { d["identity"] = map[string]any{"id": "ou_other"} },
		"missing bot":       func(d map[string]any) { delete(d, "identity") },
		"different meeting": func(d map[string]any) { d["meeting"] = map[string]any{"id": "700000000000000002"} },
		"different event meeting": func(d map[string]any) {
			d["events"] = []any{map[string]any{"event_id": "x", "event_type": "chat_received", "payload": map[string]any{"meeting": map[string]any{"id": "700000000000000002"}}}}
		},
		"missing events": func(d map[string]any) { delete(d, "events") },
		"events object":  func(d map[string]any) { d["events"] = map[string]any{} },
		"cursor stalled": func(d map[string]any) { d["has_more"] = true; d["page_token"] = "base" },
		"cursor missing": func(d map[string]any) { d["has_more"] = true; delete(d, "page_token") },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "progress.json")
			bad := fixturePage("bad", false)
			var e map[string]any
			_ = json.Unmarshal(bad.Stdout, &e)
			mutate(e["data"].(map[string]any))
			bad.Stdout, _ = json.Marshal(e)
			_, _, err := runFixture(t, path, []scriptedResult{fixturePage("base", false), bad}, func(context.Context, channel.Message) error { t.Fatal("invalid response delivered"); return nil })
			if err == nil || progressAt(t, path).Cursor != "base" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestProgressCannotCrossIdentityAndIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	_, _, _ = runFixture(t, path, []scriptedResult{fixturePage("base", false)}, func(context.Context, channel.Message) error { return nil })
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private progress: %v %v", info, err)
	}
	cfg := fixtureConfig()
	cfg.TenantID = "another-tenant"
	client := &scriptedClient{}
	err = (Receiver{Config: cfg, ProgressPath: path, Client: client}).Run(context.Background(), func(context.Context, channel.Message) error { return nil }, func(string, string) error { return nil })
	if err == nil || len(client.calls) != 0 {
		t.Fatalf("cross-identity err=%v calls=%v", err, client.calls)
	}
}

func TestCancelStopsPollingAndCLIBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &scriptedClient{}
	err := (Receiver{Config: fixtureConfig(), ProgressPath: filepath.Join(t.TempDir(), "progress.json"), Client: client}).Run(ctx, func(context.Context, channel.Message) error { return nil }, func(string, string) error { return nil })
	if !errors.Is(err, context.Canceled) || len(client.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, client.calls)
	}
	if !errors.Is(waitForPoll(ctx, time.Hour), context.Canceled) {
		t.Fatal("poll wait did not cancel")
	}
}

func TestReplyUsesFixedTargetAndOneAttempt(t *testing.T) {
	target := state.ChannelReplyTarget{Kind: "feishu-meeting", ID: fixtureConfig().MeetingID, UUID: "stable-outgoing-id"}
	for _, tc := range []struct {
		name, stdout, stderr string
		started              bool
		err                  error
		want                 string
	}{
		{"accepted", `{"ok":true,"data":{"uuid":"stable-outgoing-id"}}`, "", true, nil, Accepted},
		{"accepted no uuid", `{"ok":true,"data":{}}`, "", true, nil, Accepted},
		{"wrong uuid", `{"ok":true,"data":{"uuid":"other"}}`, "", true, nil, Unknown},
		{"no data", `{"ok":true}`, "", true, nil, Unknown},
		{"auth rejection", `{"ok":false,"error":{"type":"auth","message":"secret"}}`, "", true, nil, Failed},
		{"CLI authorization rejection", `{"ok":false,"error":{"type":"authorization","subtype":"missing_scope","message":"secret"}}`, "", true, nil, Failed},
		{"nonzero auth rejection", "", `{"ok":false,"error":{"type":"auth","message":"secret"}}`, true, errors.New("secret"), Failed},
		{"server uncertainty", "", `{"ok":false,"error":{"type":"network","message":"secret"}}`, true, errors.New("secret"), Unknown},
		{"nonjson", "secret", "", true, nil, Unknown},
		{"nonzero despite ack", `{"ok":true,"data":{}}`, "", true, errors.New("secret"), Unknown},
		{"not started", "", "", false, errors.New("secret"), Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &scriptedClient{results: []scriptedResult{{Output: Output{Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr), Started: tc.started}, Err: tc.err}}}
			got, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(context.Background(), target, "hello $(do-not-execute)")
			if got != tc.want || (err == nil) != (got == Accepted) || err != nil && strings.Contains(err.Error(), "secret") || len(client.calls) != 1 {
				t.Fatalf("got=%s err=%v calls=%v", got, err, client.calls)
			}
			args := client.calls[0]
			if argument(args, "--text") != "[AI] hello $(do-not-execute)" || argument(args, "--uuid") != target.UUID || argument(args, "--meeting-id") != target.ID || argument(args, "--as") != "bot" {
				t.Fatalf("args=%v", args)
			}
		})
	}
	client := &scriptedClient{}
	target.ID = "700000000000000002"
	got, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(context.Background(), target, "wrong meeting")
	if got != Failed || err == nil || len(client.calls) != 0 {
		t.Fatalf("target changed: %s %v", got, err)
	}
}

func TestConfigRequiresExplicitHumanScopeAndMinimumPoll(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.PollInterval = time.Second }, func(c *Config) { c.BotOpenID = "7694971214959381461" }, func(c *Config) { c.MeetingID = "820603495" }, func(c *Config) { c.TenantID = "" }, func(c *Config) { c.AllowedSenderOpenIDs = nil }, func(c *Config) { c.AllowedSenderOpenIDs = []string{c.BotOpenID} }, func(c *Config) { c.Profile = "--other-profile" }} {
		c := fixtureConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Fatalf("accepted invalid config=%+v", c)
		}
	}
}

func TestEmptyPollMayOmitMeetingMetadata(t *testing.T) {
	result := fixturePage("base", false)
	var e map[string]any
	_ = json.Unmarshal(result.Stdout, &e)
	delete(e["data"].(map[string]any), "meeting")
	result.Stdout, _ = json.Marshal(e)
	_, observed, err := runFixture(t, filepath.Join(t.TempDir(), "progress.json"), []scriptedResult{result}, func(context.Context, channel.Message) error { t.Fatal("empty poll delivered message"); return nil })
	if !errors.Is(err, stopTest) || len(observed) != 1 {
		t.Fatalf("err=%v observed=%v", err, observed)
	}
}

func TestSourceTimePreservesCLIAndRawEventTimestamps(t *testing.T) {
	want := time.Date(2026, 10, 12, 4, 5, 6, 123000000, time.UTC)
	for _, value := range []string{`"2026-10-12T12:05:06.123+08:00"`, `1791777906123`, `"1791777906123"`} {
		if got := sourceTime(json.RawMessage(value)); !got.Equal(want) {
			t.Fatalf("%s: got %s want %s", value, got, want)
		}
	}
	if !sourceTime(json.RawMessage(`null`)).IsZero() {
		t.Fatal("invented a source timestamp")
	}
}

func TestCLIClientCancellationStopsRunningCommand(t *testing.T) {
	if os.Getenv("AGENT_HOST_FEISHU_BLOCKING_FIXTURE") == "1" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_HOST_FEISHU_BLOCKING_FIXTURE", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := (CLIClient{Binary: executable}).Run(ctx, "-test.run=^TestCLIClientCancellationStopsRunningCommand$")
	if !out.Started || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("started=%v err=%v duration=%s", out.Started, err, time.Since(start))
	}
}
