package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

const cliBotSecret = "synthetic-bot-secret-never-print"

func writeBotFixture(t *testing.T) (string, string, botConfigFile) {
	t.Helper()
	dir := t.TempDir()
	secret := filepath.Join(dir, "bot-secret")
	if err := os.WriteFile(secret, []byte(cliBotSecret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := botConfigFile{ClientID: "fixture-app", RobotCode: "fixture-robot", ClientSecretFile: secret, CorpID: "fixture-tenant", OwnerUserID: "fixture-owner"}
	config := filepath.Join(dir, "bot.json")
	writeBotJSON(t, config, c)
	return config, secret, c
}

func writeBotJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

type rejectingHTTPTransport struct{ calls atomic.Int32 }

func (t *rejectingHTTPTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("unexpected network request in local configuration check")
}

func TestDingTalkCheckIsLocalAndKeepsCredentialsPrivate(t *testing.T) {
	config, secretPath, cfg := writeBotFixture(t)
	t.Setenv("TYPESAFE_API_KEY", "synthetic-jev-key-never-print")
	t.Setenv("JEV_API_KEY", "")
	dir := t.TempDir()
	// LookPath can validate this file, but invoking it cannot run native work.
	binary := filepath.Join(dir, "fixture-claude")
	if err := os.WriteFile(binary, []byte("fixture: deliberately not an executable program\n"), 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "uncreated-state")
	transport := &rejectingHTTPTransport{}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	var out, errout bytes.Buffer
	err := Run(context.Background(), []string{"dingtalk", "check", "--config", config, "--home", home, "--workspace", dir, "--claude", binary}, nil, &out, &errout)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Status             string `json:"status"`
		Transport          string `json:"transport"`
		Scope              string `json:"scope"`
		PlatformVerified   bool   `json:"platform_verified"`
		NativeAuthVerified bool   `json:"native_auth_verified"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Status != "local_configuration_valid" || report.Transport != "dingtalk_stream" || report.Scope != "owner_direct_and_enterprise_group_mentions" || report.PlatformVerified || report.NativeAuthVerified {
		t.Fatalf("local check overclaimed verification: %s %v", out.String(), err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local check created host state: %v", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("local check attempted network access")
	}
	for _, private := range []string{cliBotSecret, "synthetic-jev-key-never-print", secretPath, cfg.ClientID, cfg.CorpID, cfg.OwnerUserID} {
		if strings.Contains(out.String()+errout.String(), private) {
			t.Fatalf("local check disclosed %q", private)
		}
	}
}

func TestDingTalkConfigRequiresExplicitRobotCode(t *testing.T) {
	config, _, cfg := writeBotFixture(t)
	for _, value := range []string{"", " ", strings.Repeat("x", 513)} {
		cfg.RobotCode = value
		writeBotJSON(t, config, cfg)
		if _, err := loadBotConfig(config); err == nil || !strings.Contains(err.Error(), "robot_code") {
			t.Fatalf("invalid robot identity accepted: %v", err)
		}
	}
	cfg.RobotCode = "explicit-robot"
	writeBotJSON(t, config, cfg)
	got, err := loadBotConfig(config)
	if err != nil || got.RobotCode != cfg.RobotCode {
		t.Fatalf("robot identity changed: %v", err)
	}
}

func TestDingTalkConfigRejectsMalformedUnknownTrailingAndOversizedData(t *testing.T) {
	config, _, cfg := writeBotFixture(t)
	valid, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"malformed":        "{\"client_id\":\"" + cliBotSecret,
		"unknown field":    strings.TrimSuffix(string(valid), "}") + `,"client_secret":"` + cliBotSecret + `"}`,
		"trailing object":  string(valid) + `{}`,
		"trailing invalid": string(valid) + cliBotSecret,
		"oversized":        string(valid) + strings.Repeat(" ", 32*1024),
		"null":             "null",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(config, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := loadBotConfig(config); err == nil || got.ClientSecret != "" || strings.Contains(err.Error(), cliBotSecret) {
				t.Fatalf("invalid config accepted or leaked secret: secretPresent=%v err=%v", got.ClientSecret != "", err)
			}
		})
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if _, err := loadBotConfig(path); err == nil {
			t.Fatalf("accepted absent/nonregular configuration %q", path)
		}
	}
	fifo := filepath.Join(t.TempDir(), "config.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBotConfig(fifo); err == nil {
		t.Fatal("accepted FIFO configuration")
	}
}

func TestDingTalkCredentialsRequirePrivateRegularBoundedFile(t *testing.T) {
	for _, mode := range []os.FileMode{0400, 0600} {
		config, secret, _ := writeBotFixture(t)
		if err := os.Chmod(secret, mode); err != nil {
			t.Fatal(err)
		}
		got, err := loadBotConfig(config)
		if err != nil || got.ClientSecret != cliBotSecret {
			t.Fatalf("private mode %o: %v", mode, err)
		}
	}
	for _, name := range []string{"public", "directory", "fifo", "oversized", "empty", "relative"} {
		t.Run(name, func(t *testing.T) {
			config, secret, cfg := writeBotFixture(t)
			switch name {
			case "public":
				if err := os.Chmod(secret, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				cfg.ClientSecretFile = t.TempDir()
			case "fifo":
				cfg.ClientSecretFile = filepath.Join(t.TempDir(), "secret.fifo")
				if err := syscall.Mkfifo(cfg.ClientSecretFile, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(secret, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(secret, []byte(" \n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "relative":
				cfg.ClientSecretFile = "secret-relative"
			}
			writeBotJSON(t, config, cfg)
			if got, err := loadBotConfig(config); err == nil || got.ClientSecret != "" || strings.Contains(err.Error(), cliBotSecret) {
				t.Fatalf("invalid credential accepted or leaked: secretPresent=%v err=%v", got.ClientSecret != "", err)
			}
		})
	}
}

func TestDingTalkStatusIsReadOnlyWithoutCredentialsAndHidesReplyURL(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	home, workspace := t.TempDir(), t.TempDir()
	release, err := state.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := state.ChannelMessage{ID: "receipt", Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "provider-id", ConversationID: "dm", SenderID: "owner", Profile: "profile", Binding: "binding", Scope: workspace, MessageType: "text", Text: "A source statement.", ReplyExpiresAt: time.Now().Add(time.Hour)}
	const replyURL = "https://oapi.dingtalk.com/robot/sendBySession?session=never-print-capability"
	if inserted, err := store.EnqueueChannelMessage(context.Background(), m, replyURL); err != nil || !inserted {
		t.Fatalf("seed receipt: %v %v", inserted, err)
	}
	var out bytes.Buffer
	args := []string{"dingtalk", "status", "--home", home, "--workspace", workspace, "--claude", "/absent/claude"}
	if err := Run(context.Background(), args, nil, &out, io.Discard); err != nil {
		t.Fatalf("status required writer lock/configuration/credentials: %v", err)
	}
	var messages []state.ChannelMessage
	if err := json.Unmarshal(out.Bytes(), &messages); err != nil || len(messages) != 1 || messages[0].ID != m.ID || messages[0].Status != "queued" {
		t.Fatalf("status output: %s %v", out.String(), err)
	}
	for _, forbidden := range []string{"never-print-capability", "reply_url", "sessionWebhook", replyURL} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("status leaked %q", forbidden)
		}
	}
	stored, err := store.ChannelMessage(context.Background(), m.ID)
	if err != nil || stored.Status != "queued" || stored.ReplyStatus != "pending" {
		t.Fatalf("status mutated receipt: %+v %v", stored, err)
	}
	missing := filepath.Join(t.TempDir(), "missing-state")
	if err := Run(context.Background(), []string{"dingtalk", "status", "--home", missing, "--workspace", workspace}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("status manufactured missing host state")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created missing directory: %v", err)
	}
}

type cliReceiverFunc func(context.Context, func(context.Context, dingtalk.Message) error, func(string, string) error) error

func (f cliReceiverFunc) Run(ctx context.Context, receive func(context.Context, dingtalk.Message) error, observe func(string, string) error) error {
	return f(ctx, receive, observe)
}

type cliChannelHost struct {
	store   *state.Store
	started chan struct{}
	stopped chan struct{}
	calls   atomic.Int32
}

func (h *cliChannelHost) Session(ctx context.Context, id string) (state.Session, error) {
	if id != "" {
		return h.store.Session(ctx, id)
	}
	s := state.Session{ID: "fixture-session", Scope: "/fixture-workspace", Model: "fixture", NativeSessionID: "fixture-native-session"}
	return s, h.store.CreateSession(ctx, s)
}

func (h *cliChannelHost) RunChannel(ctx context.Context, _ state.Session, _, _ string) (state.Run, error) {
	h.calls.Add(1)
	close(h.started)
	defer close(h.stopped)
	<-ctx.Done()
	return state.Run{}, ctx.Err()
}

type cliReplier struct{ calls atomic.Int32 }

type cliReactor struct{}

func (cliReactor) AddReaction(context.Context, string, string, string) error    { return nil }
func (cliReactor) RemoveReaction(context.Context, string, string, string) error { return nil }

func (r *cliReplier) Reply(context.Context, string, time.Time, dingtalk.ReplyMessage) (string, error) {
	r.calls.Add(1)
	return "accepted", nil
}

func newCLIChannelService(t *testing.T) (*channel.Service, *cliChannelHost, *cliReplier, string) {
	t.Helper()
	store, err := state.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	h := &cliChannelHost{store: store, started: make(chan struct{}), stopped: make(chan struct{})}
	replier := &cliReplier{}
	s := &channel.Service{Store: store, Host: h, Replier: replier, Reactor: cliReactor{}, ClientID: "app", Scope: "/fixture-workspace", Profile: "profile", Timeout: time.Minute}
	m := dingtalk.Message{ID: "queued", ConversationID: "owner-dm", SenderID: "owner", TenantID: "tenant", MessageType: "text", Text: "A queued task.", ReplyURL: "https://oapi.dingtalk.com/robot/sendBySession?session=fixture", ReplyExpiresAt: time.Now().Add(time.Hour)}
	if err := s.Receive(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return s, h, replier, channel.Key("dingtalk", s.ClientID, m.ID)
}

func requireClosed(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatalf("%s was not joined before serveChannel returned", name)
	}
}

func TestServeChannelWaitsForReadinessAndJoinsCancellation(t *testing.T) {
	s, h, replier, id := newCLIChannelService(t)
	entered, connected, receiverStopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	receiver := cliReceiverFunc(func(ctx context.Context, _ func(context.Context, dingtalk.Message) error, observe func(string, string) error) error {
		defer close(receiverStopped)
		close(entered)
		select {
		case <-connected:
			if err := observe("connected", "fixture ready"); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveChannel(ctx, receiver, s, func(string, string) error { return nil }) }()
	<-entered
	select {
	case <-h.started:
		t.Fatal("queued native work started before receiver readiness")
	case <-time.After(30 * time.Millisecond):
	}
	queued, err := s.Store.ChannelMessage(context.Background(), id)
	if err != nil || queued.Status != "queued" || h.calls.Load() != 0 {
		t.Fatalf("work claimed before readiness: %+v %v", queued, err)
	}
	close(connected)
	select {
	case <-h.started:
	case <-ctx.Done():
		t.Fatal("readiness did not release queued work")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	requireClosed(t, "receiver", receiverStopped)
	requireClosed(t, "native worker", h.stopped)
	finished, err := s.Store.ChannelMessage(context.Background(), id)
	if err != nil || finished.Status != "failed" || finished.ReplyStatus != "accepted" || replier.calls.Load() != 1 {
		t.Fatalf("worker returned before recording cancellation: %+v %v", finished, err)
	}
}

func TestServeChannelConnectionFailureNeverClaimsQueuedWork(t *testing.T) {
	for _, name := range []string{"failure", "unexpected success"} {
		t.Run(name, func(t *testing.T) {
			s, h, _, id := newCLIChannelService(t)
			want := errors.New("fixture connection failed")
			receiver := cliReceiverFunc(func(context.Context, func(context.Context, dingtalk.Message) error, func(string, string) error) error {
				if name == "unexpected success" {
					return nil
				}
				return want
			})
			err := serveChannel(context.Background(), receiver, s, func(string, string) error { return nil })
			if err == nil || name == "failure" && !errors.Is(err, want) {
				t.Fatalf("connection failure hidden: %v", err)
			}
			m, readErr := s.Store.ChannelMessage(context.Background(), id)
			if readErr != nil || m.Status != "queued" || h.calls.Load() != 0 {
				t.Fatalf("failed receiver released queued work: %+v %v", m, readErr)
			}
		})
	}
}

func TestServeChannelReceiverFailureCancelsAndJoinsActiveWorker(t *testing.T) {
	s, h, replier, _ := newCLIChannelService(t)
	want := errors.New("fixture receiver disconnected")
	receiverStopped := make(chan struct{})
	receiver := cliReceiverFunc(func(ctx context.Context, _ func(context.Context, dingtalk.Message) error, observe func(string, string) error) error {
		defer close(receiverStopped)
		if err := observe("connected", "fixture ready"); err != nil {
			return err
		}
		select {
		case <-h.started:
			return want
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := serveChannel(ctx, receiver, s, func(string, string) error { return nil })
	if !errors.Is(err, want) || !errors.Is(err, context.Canceled) {
		t.Fatalf("receiver/worker failures were not joined: %v", err)
	}
	requireClosed(t, "receiver", receiverStopped)
	requireClosed(t, "native worker", h.stopped)
	if replier.calls.Load() != 1 {
		t.Fatal("worker was not joined through final delivery recording")
	}
}

func TestServeChannelWorkerFailureCancelsAndJoinsReceiver(t *testing.T) {
	s, h, _, _ := newCLIChannelService(t)
	workerErr, receiverErr := errors.New("fixture worker journal failed"), errors.New("fixture receiver closed")
	s.Observe = func(kind, message string) error {
		if kind == "channel.processing" {
			return workerErr
		}
		return nil
	}
	receiverStopped := make(chan struct{})
	receiver := cliReceiverFunc(func(ctx context.Context, _ func(context.Context, dingtalk.Message) error, observe func(string, string) error) error {
		defer close(receiverStopped)
		if err := observe("connected", "fixture ready"); err != nil {
			return err
		}
		<-ctx.Done()
		return receiverErr
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := serveChannel(ctx, receiver, s, func(string, string) error { return nil })
	if !errors.Is(err, workerErr) || !errors.Is(err, receiverErr) || h.calls.Load() != 0 {
		t.Fatalf("worker/receiver failures were not joined: %v", err)
	}
	requireClosed(t, "receiver", receiverStopped)
}

func TestServeChannelCancellationBeforeReadyJoinsReceiver(t *testing.T) {
	s, h, _, _ := newCLIChannelService(t)
	entered, receiverStopped := make(chan struct{}), make(chan struct{})
	closeErr := errors.New("fixture close failure")
	receiver := cliReceiverFunc(func(ctx context.Context, _ func(context.Context, dingtalk.Message) error, _ func(string, string) error) error {
		defer close(receiverStopped)
		close(entered)
		<-ctx.Done()
		return closeErr
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveChannel(ctx, receiver, s, func(string, string) error { return nil }) }()
	<-entered
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) || h.calls.Load() != 0 {
		t.Fatalf("preconnection cancellation not joined: %v", err)
	}
	requireClosed(t, "receiver", receiverStopped)
}
