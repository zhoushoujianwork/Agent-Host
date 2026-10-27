package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/harness"
	"agent-host/internal/host"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type serviceRunner struct {
	mu        sync.Mutex
	requests  []harness.Request
	run       func(context.Context, harness.Request) (harness.Result, error)
	resultRaw json.RawMessage
}

func (r *serviceRunner) Run(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	result := harness.Result{Status: "completed", Text: "Fixture answer.", Model: "fixture-model", NativeSessionID: req.NativeSessionID}
	var err error
	if r.run != nil {
		result, err = r.run(ctx, req)
	}
	if emitErr := emit(harness.Observation{Type: "init", Model: result.Model, NativeSessionID: req.NativeSessionID, Raw: json.RawMessage(`{"type":"system","subtype":"init"}`)}); emitErr != nil {
		return harness.Result{Status: "unknown", Error: emitErr.Error()}, emitErr
	}
	raw := r.resultRaw
	if raw == nil {
		raw = json.RawMessage(`{"type":"result"}`)
	}
	if emitErr := emit(harness.Observation{Type: "result", Model: result.Model, NativeSessionID: req.NativeSessionID, Message: result.Status, Raw: raw}); emitErr != nil {
		return harness.Result{Status: "unknown", Error: emitErr.Error()}, emitErr
	}
	return result, err
}

func (r *serviceRunner) snapshot() []harness.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]harness.Request(nil), r.requests...)
}

type serviceDecider func(context.Context, string, []state.Memory) (memory.Decision, error)

func (f serviceDecider) Decide(ctx context.Context, input string, existing []state.Memory) (memory.Decision, error) {
	return f(ctx, input, existing)
}

type replyCall struct {
	url      string
	expires  time.Time
	text     string
	atUserID string
}

type serviceReplier struct {
	mu    sync.Mutex
	calls []replyCall
	reply func(context.Context, replyCall) (string, error)
}

func (r *serviceReplier) Reply(ctx context.Context, url string, expires time.Time, message dingtalk.ReplyMessage) (string, error) {
	call := replyCall{url: url, expires: expires, text: message.Text, atUserID: message.AtUserID}
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.reply != nil {
		return r.reply(ctx, call)
	}
	return "accepted", nil
}

func (r *serviceReplier) snapshot() []replyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]replyCall(nil), r.calls...)
}

type serviceFixture struct {
	service *Service
	host    *host.Host
	runner  *serviceRunner
	replier *serviceReplier
	config  dingtalk.Config
	home    string
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	f := &serviceFixture{home: t.TempDir(), runner: &serviceRunner{}, replier: &serviceReplier{}, config: dingtalk.Config{ClientID: "app", ClientSecret: "fixture-secret", CorpID: "tenant", OwnerUserID: "owner"}}
	release, err := state.Acquire(f.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	store, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	f.host = &host.Host{Store: store, Runner: f.runner, Config: host.Config{Scope: t.TempDir(), Model: "fixture-model", DecisionModel: "fixture-decision", DecisionProvider: "fixture", ToolsDisabled: true}}
	f.host.Objects = fixtureObjectExtractor{}
	f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "none", Model: "fixture-decision", Details: json.RawMessage(`{"reason":"question"}`)}, nil
	})
	f.service = &Service{Store: store, Host: f.host, Replier: f.replier, Reactor: &serviceReactor{}, ClientID: f.config.ClientID, Scope: f.host.Config.Scope, Profile: "fixture-profile", Timeout: time.Minute}
	t.Cleanup(func() {
		if err := f.service.Store.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *serviceFixture) message(t *testing.T, id, text, kind, conversation string) dingtalk.Message {
	t.Helper()
	now := time.Now().UTC()
	data, err := json.Marshal(map[string]any{
		"msgId": id, "conversationId": conversation, "conversationType": "1",
		"senderId": "sender-open-id", "senderStaffId": f.config.OwnerUserID, "senderCorpId": f.config.CorpID,
		"chatbotCorpId": f.config.CorpID, "chatbotUserId": "robot-id", "msgtype": kind,
		"createAt": now.UnixMilli(), "sessionWebhook": "https://oapi.dingtalk.com/robot/sendBySession?session=cap-" + id,
		"sessionWebhookExpiredTime": now.Add(time.Hour).UnixMilli(), "text": map[string]string{"content": text},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := dingtalk.Parse(data, f.config, now)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *serviceFixture) richTextMessage(t *testing.T, id string, parts []any, conversation string) dingtalk.Message {
	t.Helper()
	now := time.Now().UTC()
	data, err := json.Marshal(map[string]any{
		"msgId": id, "conversationId": conversation, "conversationType": "1",
		"senderId": "sender-open-id", "senderStaffId": f.config.OwnerUserID, "senderCorpId": f.config.CorpID,
		"chatbotCorpId": f.config.CorpID, "chatbotUserId": "robot-id", "msgtype": "richText",
		"createAt": now.UnixMilli(), "sessionWebhook": "https://oapi.dingtalk.com/robot/sendBySession?session=cap-" + id,
		"sessionWebhookExpiredTime": now.Add(time.Hour).UnixMilli(), "content": map[string]any{"richText": parts},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := dingtalk.Parse(data, f.config, now)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *serviceFixture) receive(t *testing.T, m dingtalk.Message) {
	t.Helper()
	if err := f.service.Receive(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func (f *serviceFixture) next(t *testing.T) state.ChannelMessage {
	t.Helper()
	m, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *serviceFixture) process(t *testing.T, input dingtalk.Message) state.ChannelMessage {
	t.Helper()
	f.receive(t, input)
	m := f.next(t)
	if err := f.service.process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return f.receipt(t, input.ID)
}

func (f *serviceFixture) receipt(t *testing.T, id string) state.ChannelMessage {
	t.Helper()
	m, err := f.service.Store.ChannelMessage(context.Background(), Key("dingtalk", f.service.ClientID, id))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *serviceFixture) run(t *testing.T, m state.ChannelMessage) state.Run {
	t.Helper()
	r, err := f.service.Store.Run(context.Background(), m.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDingTalkServiceMemoryLoopSourceAndExplicitNewSession(t *testing.T) {
	f := newServiceFixture(t)
	const statement = "我偏好中文回复。"
	f.host.Decider = serviceDecider(func(_ context.Context, input string, existing []state.Memory) (memory.Decision, error) {
		if input == statement {
			return memory.Decision{Kind: "preference", Store: true, Model: "fixture-decision", Details: json.RawMessage(`{"reason":"explicit preference"}`)}, nil
		}
		return memory.Decision{Kind: "none", Model: "fixture-decision"}, nil
	})
	firstInput := f.message(t, "first", statement, "text", "owner-dm")
	first := f.process(t, firstInput)
	firstRun := f.run(t, first)
	if first.Status != "completed" || first.ReplyStatus != "accepted" || firstRun.MemoryStatus != "saved" || firstRun.AcceptanceStatus != "unverified" {
		t.Fatalf("first receipt: %+v run=%+v", first, firstRun)
	}
	if !first.SentAt.Equal(firstInput.CreatedAt) {
		t.Fatalf("original message time lost: got=%s want=%s", first.SentAt, firstInput.CreatedAt)
	}
	if firstRun.Source == nil || firstRun.Source.ChannelMessageID != first.ID || firstRun.Source.AccountID != f.config.ClientID || firstRun.Source.TenantID != f.config.CorpID || firstRun.Source.SenderID != f.config.OwnerUserID || firstRun.Source.MessageID != firstInput.ID || firstRun.Source.ConversationID != firstInput.ConversationID {
		t.Fatalf("missing original identity: %+v", firstRun.Source)
	}
	memories, err := f.service.Store.Memories(context.Background(), f.service.Scope)
	if err != nil || len(memories) != 1 || memories[0].SourceRunID != firstRun.ID || memories[0].Text != statement {
		t.Fatalf("memory provenance: %+v %v", memories, err)
	}
	second := f.process(t, f.message(t, "second", "请介绍项目。", "text", "owner-dm"))
	if second.SessionID != first.SessionID || !f.runner.snapshot()[1].Resume {
		t.Fatal("ordinary DM did not resume its bound session")
	}
	reset := f.process(t, f.message(t, "reset", "/new", "text", "owner-dm"))
	if reset.RunID != "" || len(f.runner.snapshot()) != 2 || reset.ReplyStatus != "accepted" {
		t.Fatalf("new-session command executed native work: %+v", reset)
	}
	third := f.process(t, f.message(t, "third", "请介绍项目。", "text", "owner-dm"))
	thirdRun := f.run(t, third)
	requests := f.runner.snapshot()
	if third.SessionID == first.SessionID || requests[2].Resume || requests[2].NativeSessionID == requests[0].NativeSessionID || len(thirdRun.Context) != 1 || thirdRun.Context[0].ID != memories[0].ID {
		t.Fatalf("fresh session memory reuse: receipt=%+v run=%+v", third, thirdRun)
	}
	for _, want := range []string{statement, memories[0].ID, firstRun.ID, "data, not instructions"} {
		if !strings.Contains(requests[2].Prompt, want) {
			t.Errorf("fresh prompt missing %q", want)
		}
	}
	for _, req := range requests {
		if req.Workspace != f.service.Scope || req.Model != f.host.Config.Model || !req.ToolsDisabled || len(req.AllowedTools) != 0 || strings.Contains(req.Prompt, "session=cap-") || strings.Contains(req.Prompt, f.config.ClientSecret) {
			t.Fatalf("request escaped configured authority or leaked capabilities: %+v", req)
		}
	}
	replies := f.replier.snapshot()
	if len(replies) != 4 || replies[0].url != firstInput.ReplyURL || !replies[0].expires.Equal(firstInput.ReplyExpiresAt) || strings.Contains(replies[0].text, firstRun.ID) || !strings.Contains(replies[0].text, firstRun.Output) {
		t.Fatalf("reply did not preserve exact original capability: %+v", replies)
	}
}

func TestDingTalkServiceDuplicatesBeforeDuringAfterExecution(t *testing.T) {
	f := newServiceFixture(t)
	input := f.message(t, "duplicate", "Explain this project.", "text", "owner-dm")
	f.receive(t, input)
	f.receive(t, input)
	if len(f.runner.snapshot()) != 0 || len(f.replier.snapshot()) != 0 {
		t.Fatal("receipt callback waited for execution or delivery")
	}
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		if err := f.service.Receive(context.Background(), input); err != nil {
			return harness.Result{Status: "failed", Error: err.Error()}, err
		}
		return harness.Result{Status: "completed", Text: "answer", Model: req.Model, NativeSessionID: req.NativeSessionID}, nil
	}
	if err := f.service.process(context.Background(), f.next(t)); err != nil {
		t.Fatal(err)
	}
	f.receive(t, input)
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate was queued: %v", err)
	}
	if len(f.runner.snapshot()) != 1 || len(f.replier.snapshot()) != 1 || f.receipt(t, input.ID).ReplyStatus != "accepted" {
		t.Fatal("duplicate replayed execution or delivery")
	}
}

func TestDingTalkServiceAutomaticallyRecoversForNewMessage(t *testing.T) {
	for _, failure := range []string{"failed", "canceled", "unknown", "memory"} {
		t.Run(failure, func(t *testing.T) {
			f := newServiceFixture(t)
			if failure != "memory" {
				f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
					err := errors.New("fixture native failed")
					return harness.Result{Status: failure, Error: err.Error(), Model: req.Model}, err
				}
			} else {
				f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
					return memory.Decision{}, errors.New("fixture memory failed")
				})
			}
			first := f.process(t, f.message(t, "failed", "Explain this project.", "text", "owner-dm"))
			firstRun := f.run(t, first)
			outcomeText := "失败"
			if failure == "canceled" {
				outcomeText = "已取消"
			} else if failure == "unknown" {
				outcomeText = "结果未确认"
			}
			wantStatus := "failed"
			if failure == "memory" {
				wantStatus, outcomeText = "completed", firstRun.Output
			}
			if first.Status != wantStatus || first.ReplyStatus != "accepted" || !strings.Contains(first.ReplyText, outcomeText) {
				t.Fatalf("failure hidden: %+v", first)
			}
			if failure != "memory" && (firstRun.ExecutionStatus != failure || firstRun.MemoryStatus != "skipped") || failure == "memory" && (firstRun.ExecutionStatus != "completed" || firstRun.MemoryStatus != "failed") {
				t.Fatalf("failure outcomes conflated: %+v", firstRun)
			}
			f.runner.run = nil
			f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
				return memory.Decision{Kind: "none"}, nil
			})
			fresh := f.process(t, f.message(t, "fresh", "A fresh request.", "text", "owner-dm"))
			if fresh.Status != "completed" || fresh.SessionID == first.SessionID || len(f.runner.snapshot()) != 2 || f.runner.snapshot()[1].Resume || !strings.Contains(fresh.ReplyText, "本条消息") || strings.Contains(fresh.ReplyText, "/new") {
				t.Fatalf("automatic session recovery failed: %+v", fresh)
			}
			session, err := f.service.Store.Session(context.Background(), fresh.SessionID)
			if err != nil || session.PreviousSessionID != first.SessionID || session.RecoveryRunID != firstRun.ID || session.NativeSessionID == firstRun.NativeSessionID || f.run(t, fresh).Input != "A fresh request." {
				t.Fatal("recovery lost provenance or replayed input", session, err)
			}
			original := f.run(t, first)
			if original.ExecutionStatus != firstRun.ExecutionStatus || original.MemoryStatus != firstRun.MemoryStatus {
				t.Fatal("new session rewrote previous outcome")
			}
			f.receive(t, f.message(t, "fresh", "A fresh request.", "text", "owner-dm"))
			if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("recovery made duplicate delivery replayable", err)
			}
			next := f.process(t, f.message(t, "next", "Continue the new conversation.", "text", "owner-dm"))
			if next.SessionID != fresh.SessionID || !f.runner.snapshot()[2].Resume || strings.Contains(next.ReplyText, "本条消息") {
				t.Fatal("recovery was not bound or notice repeated", next)
			}
		})
	}
}

func TestDingTalkServiceDeliveryFailureIsSeparateAndNeverReplayed(t *testing.T) {
	for _, outcome := range []string{"failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f := newServiceFixture(t)
			f.replier.reply = func(context.Context, replyCall) (string, error) {
				return outcome, errors.New("fixture transport error")
			}
			input := f.message(t, "delivery", "Explain this project.", "text", "owner-dm")
			m := f.process(t, input)
			r := f.run(t, m)
			if m.Status != "completed" || m.ReplyStatus != outcome || m.ReplyError == "" || r.ExecutionStatus != "completed" || r.MemoryStatus != "unchanged" || r.AcceptanceStatus != "unverified" {
				t.Fatalf("delivery changed execution: message=%+v run=%+v", m, r)
			}
			f.receive(t, input)
			if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("failed reply caused replay: %v", err)
			}
			if len(f.runner.snapshot()) != 1 || len(f.replier.snapshot()) != 1 {
				t.Fatal("native task or reply replayed")
			}
		})
	}
}

func TestDingTalkServiceUnsupportedMessageNeverCreatesSessionOrRun(t *testing.T) {
	f := newServiceFixture(t)
	input := f.message(t, "image", "", "picture", "owner-dm")
	m := f.process(t, input)
	if m.Status != "failed" || m.ReplyStatus != "accepted" || m.RunID != "" || m.SessionID != "" || !strings.Contains(m.ReplyText, "内容格式暂不支持") || len(f.runner.snapshot()) != 0 {
		t.Fatalf("unsupported message reached native execution: %+v", m)
	}
	if _, err := f.service.Store.ChannelSession(context.Background(), m.Binding); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unsupported message created session binding: %v", err)
	}
	memories, err := f.service.Store.Memories(context.Background(), f.service.Scope)
	if err != nil || len(memories) != 0 {
		t.Fatalf("unsupported input became memory: %+v %v", memories, err)
	}
}

func TestDingTalkServicePlainRichTextProjectRoutingAndMemory(t *testing.T) {
	f := newProjectFixture(t)
	const statement = "I prefer concise answers for project alpha."
	f.host.Decider = serviceDecider(func(_ context.Context, input string, _ []state.Memory) (memory.Decision, error) {
		return memory.Decision{Store: input == statement, Kind: "preference", Model: "fixture-decision"}, nil
	})
	selection := f.richTextMessage(t, "rich-switch", []any{map[string]string{"text": "/sta"}, map[string]string{"text": "tus"}}, "owner-dm")
	input := f.richTextMessage(t, "rich-task", []any{map[string]string{"text": "I prefer concise answers for "}, map[string]string{"text": "project alpha."}}, "owner-dm")
	f.receive(t, selection)
	f.receive(t, input)
	for i := 0; i < 2; i++ {
		if err := f.service.process(context.Background(), f.next(t)); err != nil {
			t.Fatal(err)
		}
	}
	switchReceipt, task := f.receipt(t, selection.ID), f.receipt(t, input.ID)
	run := f.run(t, task)
	if switchReceipt.Status != "completed" || switchReceipt.MessageType != "richText" || switchReceipt.RunID != "" || switchReceipt.SessionID != "" || len(switchReceipt.Reactions) != 0 || task.Status != "completed" || task.MessageType != "richText" || task.ProjectID != "alpha" || run.Input != statement || run.Workspace != f.projects["alpha"].Workspace || run.MemoryStatus != "saved" {
		t.Fatalf("richText routing or memory failed: switch=%+v task=%+v run=%+v", switchReceipt, task, run)
	}
	if run.Source == nil || run.Source.MessageID != input.ID || run.Source.SenderID != f.config.OwnerUserID || run.Source.TenantID != f.config.CorpID {
		t.Fatalf("richText lost verified source: %+v", run.Source)
	}
	current := f.process(t, f.richTextMessage(t, "rich-current", []any{map[string]string{"text": "/status"}}, "owner-dm"))
	reset := f.process(t, f.richTextMessage(t, "rich-reset", []any{map[string]string{"text": "/new"}}, "owner-dm"))
	fresh := f.process(t, f.richTextMessage(t, "rich-fresh", []any{map[string]string{"text": "Inspect project alpha."}}, "owner-dm"))
	freshRun := f.run(t, fresh)
	requests := f.runner.snapshot()
	if current.Status != "completed" || current.RunID != "" || !strings.Contains(current.ReplyText, "已完成") || reset.RunID != "" || reset.SessionID == task.SessionID || fresh.SessionID != reset.SessionID || len(requests) != 2 || requests[1].Resume || len(freshRun.Context) != 1 || freshRun.Context[0].Text != statement {
		t.Fatalf("richText commands executed or lost fresh-session memory: current=%+v reset=%+v fresh=%+v run=%+v requests=%+v", current, reset, fresh, freshRun, requests)
	}
	f.receive(t, input)
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate richText replayed: %v", err)
	}
	drainReactions(t, f.service)
	task = f.receipt(t, input.ID)
	if len(task.Reactions) == 0 {
		t.Fatalf("richText lost task feedback: %+v", task)
	}
	terminal := task.Reactions[len(task.Reactions)-1]
	if task.ReplyStatus != "accepted" || terminal.Stage != "completed" || terminal.Status != "accepted" {
		t.Fatalf("richText lost independent task feedback: %+v", task)
	}
}

func TestDingTalkServiceLegacyRejectedRichTextRedeliveryDoesNotReplay(t *testing.T) {
	f := newProjectFixture(t)
	normalized := f.richTextMessage(t, "legacy-rich", []any{map[string]string{"text": "/project alpha"}}, "owner-dm")
	legacy := normalized
	legacy.Text = ""
	old := f.process(t, legacy)
	if old.Status != "failed" || old.Error != "unsupported message type" || old.ReplyStatus != "accepted" {
		t.Fatalf("legacy rejection fixture: %+v", old)
	}
	before, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	f.service.Profile = "upgraded-profile"
	f.service.Scope = "/upgraded-queue-workspace"
	f.receive(t, normalized)
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("upgraded redelivery replayed rejected input: %v", err)
	}
	after, err := json.Marshal(f.receipt(t, legacy.ID))
	if err != nil || string(before) != string(after) || len(f.runner.snapshot()) != 0 || len(f.replier.snapshot()) != 1 {
		t.Fatalf("upgraded redelivery modified evidence or repeated execution/reply: %v", err)
	}
	if _, err := f.service.Store.ChannelProject(context.Background(), old.Binding); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("upgraded duplicate executed project command: %v", err)
	}
}

func TestDingTalkServiceMixedRichTextFailsWithoutExecutingCommandPrefix(t *testing.T) {
	for _, extra := range []map[string]string{
		{"type": "audio", "downloadCode": "private-media"},
		{"text": "link", "url": "https://example.com", "extra": "attribute"},
		{"text": "unknown", "extra": "attribute"},
	} {
		t.Run(extra["type"]+extra["text"], func(t *testing.T) {
			f := newProjectFixture(t)
			input := f.richTextMessage(t, "mixed", []any{
				map[string]string{"text": "/project alpha"},
				map[string]string{"type": "picture", "downloadCode": "private-image"},
				map[string]string{"text": "link", "url": "https://example.com"},
				extra,
			}, "owner-dm")
			m := f.process(t, input)
			if m.Status != "failed" || m.MessageType != "richText" || m.Text != "" || m.RunID != "" || m.SessionID != "" || m.ReplyStatus != "accepted" || !strings.Contains(m.ReplyText, "内容格式暂不支持") || len(m.Reactions) != 0 || len(f.runner.snapshot()) != 0 {
				t.Fatalf("mixed richText executed a partial command: %+v", m)
			}
			if _, err := f.service.Store.ChannelProject(context.Background(), m.Binding); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("mixed richText changed project selection: %v", err)
			}
			if _, err := f.service.Store.ChannelSession(context.Background(), m.Binding); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("mixed richText created a native session: %v", err)
			}
			memories, err := f.service.Store.Memories(context.Background(), f.projects["alpha"].Scope)
			if err != nil || len(memories) != 0 {
				t.Fatalf("mixed richText entered memory: %+v %v", memories, err)
			}
		})
	}
}

func TestDingTalkServiceExpiredQueuedReplyPreventsNativeExecution(t *testing.T) {
	f := newServiceFixture(t)
	input := f.message(t, "expired", "Do not execute work after the reply expires.", "text", "owner-dm")
	// Model a formerly valid receipt whose capability has expired while queued.
	input.ReplyExpiresAt = time.Now().Add(-time.Second)
	f.replier.reply = func(_ context.Context, call replyCall) (string, error) {
		if call.expires.After(time.Now()) {
			t.Error("replier did not receive the original expired capability")
		}
		return "failed", errors.New("reply capability expired")
	}
	m := f.process(t, input)
	if m.Status != "failed" || m.ReplyStatus != "failed" || m.RunID != "" || m.SessionID != "" || !strings.Contains(m.Error, "expired before execution") || len(f.runner.snapshot()) != 0 {
		t.Fatalf("expired input reached execution: %+v", m)
	}
	if _, err := f.service.Store.ChannelSession(context.Background(), m.Binding); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired input created a session: %v", err)
	}
}

func TestDingTalkServiceRestartRunsOnlyQueuedInput(t *testing.T) {
	f := newServiceFixture(t)
	claimedInput := f.message(t, "claimed", "Never implicitly replay me.", "text", "claimed-dm")
	f.receive(t, claimedInput)
	claimed := f.next(t)
	runningInput := f.message(t, "running", "Never replay my native operation.", "text", "running-dm")
	f.receive(t, runningInput)
	running := f.next(t)
	session, err := f.service.session(context.Background(), running.Binding, false)
	if err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: host.ID(), SessionID: session.ID, Scope: session.Scope, ProjectID: session.ProjectID, ProjectName: session.ProjectName, Workspace: session.Workspace, RuntimeFingerprint: session.RuntimeFingerprint, Input: running.Text, NativeSessionID: session.NativeSessionID, RequestedModel: f.host.Config.Model}
	if err := f.service.Store.CreateChannelRun(context.Background(), run, running.ID); err != nil {
		t.Fatal(err)
	}
	queued := f.message(t, "queued", "Execute after restart.", "text", "queued-dm")
	f.receive(t, queued)
	if err := f.service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := state.Open(f.home, false)
	if err != nil {
		t.Fatal(err)
	}
	f.service.Store, f.host.Store = restarted, restarted
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := f.next(t)
	if m.MessageID != queued.ID {
		t.Fatalf("restart selected a previously claimed input: %+v", m)
	}
	if err := f.service.process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{claimed.MessageID, running.MessageID} {
		got := f.receipt(t, id)
		if got.Status != "unknown" || got.ReplyStatus != "pending" {
			t.Fatalf("recovery hid uncertainty: %+v", got)
		}
	}
	run, err = restarted.Run(context.Background(), run.ID)
	if err != nil || run.ExecutionStatus != "unknown" || len(f.runner.snapshot()) != 1 || len(f.replier.snapshot()) != 1 {
		t.Fatalf("restart replayed or lost uncertainty: %+v %v", run, err)
	}
	newMessage := f.process(t, f.message(t, "after-interruption", "New task after the interruption.", "text", "running-dm"))
	if newMessage.Status != "completed" || newMessage.SessionID == session.ID || len(f.runner.snapshot()) != 2 || f.runner.snapshot()[1].Resume || !strings.Contains(newMessage.ReplyText, "本条消息") {
		t.Fatal("recovered unknown task blocked a new message", newMessage)
	}
	if f.receipt(t, running.MessageID).Status != "unknown" {
		t.Fatal("new message changed interrupted request")
	}
}

func TestActiveSessionIsNotAutomaticallyReplaced(t *testing.T) {
	f := newServiceFixture(t)
	f.receive(t, f.message(t, "active", "An operation still running.", "text", "dm"))
	m := f.next(t)
	session, err := f.service.session(context.Background(), m.Binding, false)
	if err != nil {
		t.Fatal(err)
	}
	run := state.Run{ID: host.ID(), SessionID: session.ID, Scope: session.Scope, Workspace: session.Workspace, Input: m.Text, NativeSessionID: session.NativeSessionID, RequestedModel: session.Model}
	if err := f.service.Store.CreateChannelRun(context.Background(), run, m.ID); err != nil {
		t.Fatal(err)
	}
	blocked := f.process(t, f.message(t, "new", "A new request.", "text", "dm"))
	boundID, err := f.service.Store.ChannelSession(context.Background(), m.Binding)
	if err != nil || boundID != session.ID || blocked.RunID != "" || blocked.Status != "failed" || len(f.runner.snapshot()) != 0 || !strings.Contains(blocked.Error, "running") {
		t.Fatal("active session was replaced", blocked, boundID, err)
	}
}

func TestDingTalkServiceWorkerSerializesNativeExecution(t *testing.T) {
	f := newServiceFixture(t)
	first := f.message(t, "first", "First request.", "text", "owner-dm")
	second := f.message(t, "second", "Second request.", "text", "owner-dm")
	f.receive(t, first)
	f.receive(t, second)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		if !req.Resume {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return harness.Result{Status: "canceled", Error: ctx.Err().Error()}, ctx.Err()
			}
		}
		return harness.Result{Status: "completed", Text: "answer", Model: req.Model}, nil
	}
	delivered := 0
	f.replier.reply = func(context.Context, replyCall) (string, error) {
		delivered++
		if delivered == 2 {
			cancel()
		}
		return "accepted", nil
	}
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker did not start first request")
	}
	f.receive(t, first)
	if len(f.runner.snapshot()) != 1 || f.receipt(t, second.ID).Status != "queued" {
		t.Fatal("worker claimed another turn while the native session was active")
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker termination: %v", err)
	}
	requests := f.runner.snapshot()
	if len(requests) != 2 || !requests[1].Resume || requests[0].NativeSessionID != requests[1].NativeSessionID || f.receipt(t, second.ID).ReplyStatus != "accepted" {
		t.Fatalf("serialized continuation: %+v", requests)
	}
}

func TestDingTalkServiceLongReplyPreservesUTF8AndDurableFullOutput(t *testing.T) {
	for _, outcome := range []string{"success", "memory failure"} {
		t.Run(outcome, func(t *testing.T) {
			f := newServiceFixture(t)
			full := strings.Repeat("中文答案🙂。", 1000)
			f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
				return harness.Result{Status: "completed", Text: full, Model: req.Model}, nil
			}
			if outcome == "memory failure" {
				f.host.Decider = serviceDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
					return memory.Decision{}, errors.New("fixture memory failure")
				})
			}
			m := f.process(t, f.message(t, "long", "Give a detailed answer.", "text", "owner-dm"))
			r := f.run(t, m)
			reply := f.replier.snapshot()[0].text
			if r.Output != full || len(reply) > dingtalk.MaxReplyBytes || !utf8.ValidString(reply) || !strings.Contains(reply, "节选") || strings.Contains(reply, r.ID) || m.ReplyText != reply {
				t.Fatalf("long reply handling: bytes=%d valid=%v fullSaved=%v reply=%q", len(reply), utf8.ValidString(reply), r.Output == full, reply)
			}
			if outcome == "memory failure" && (r.MemoryStatus != "failed" || m.Status != "completed" || strings.Contains(reply, "后续保存步骤失败")) {
				t.Fatalf("closeout failure changed the completed answer: run=%+v message=%+v", r, m)
			}
		})
	}
	if got := limitReply("Short answer."); got != "Short answer." {
		t.Fatalf("short answer modified: %q", got)
	}
}
