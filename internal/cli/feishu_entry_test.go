package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func TestAutomaticMeetingCheckNeedsNoFixedMeetingOrPlatformCalls(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "meeting.json")
	cfg := meetingConfigFixture()
	cfg.AutoJoin, cfg.MeetingID = true, ""
	writeBotJSON(t, path, cfg)
	native, marker := meetingCommandFixtures(t)
	t.Setenv("TYPESAFE_API_KEY", serviceTestKey)
	t.Setenv("JEV_API_KEY", "")
	transport := blockMeetingCheckNetwork(t)
	home := filepath.Join(root, "uncreated-state")
	var out, errout bytes.Buffer
	if err := Run(t.Context(), []string{"feishu-meeting", "check", "--config", path,
		"--home", home, "--workspace", root, "--claude", native, "--model", "fixture-model",
		"--jev-provider", "typesafe"}, nil, &out, &errout); err != nil {
		t.Fatal(err)
	}
	report := assertMeetingLocalReport(t, out.Bytes())
	if report["auto_join_enabled"] != true {
		t.Fatal("check did not distinguish automatic entry from fixed-meeting mode")
	}
	for _, path := range []string{home, marker} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("local automatic-entry check created state or invoked a process")
		}
	}
	if transport.calls.Load() != 0 {
		t.Fatal("automatic-entry check contacted a platform")
	}
	loaded, err := loadFeishuMeetingConfig(path)
	if err != nil || loaded.Validate() != nil || loaded.ValidateMeeting() == nil {
		t.Fatal("automatic entry must not grant a reply or polling destination")
	}
	cfg.AutoJoin = false
	writeBotJSON(t, path, cfg)
	if _, err := loadFeishuMeetingConfig(path); err == nil {
		t.Fatal("fixed-meeting mode accepted an empty destination")
	}
}

type entryGuardFixture bool

func (g entryGuardFixture) CanReply(string) bool { return bool(g) }

type meetingContextFixture struct {
	context context.Context
	err     error
}

func (f meetingContextFixture) Context(string) (context.Context, error) {
	return f.context, f.err
}

func TestAutomaticMeetingReplyRejectsInactiveOrUnreadyDestinations(t *testing.T) {
	_, marker := meetingCommandFixtures(t)
	for _, test := range []struct {
		name   string
		active bool
		kind   string
		err    error
	}{
		{"ended", false, state.ReplyTargetFeishuMeeting, nil},
		{"wrong provider", true, "dingtalk", nil},
		{"not baselined", true, state.ReplyTargetFeishuMeeting, errors.New("receiver is not ready")},
	} {
		t.Run(test.name, func(t *testing.T) {
			replier := automaticMeetingReplier{authority: entryGuardFixture(test.active), contexts: meetingContextFixture{context: t.Context(), err: test.err}}
			status, err := replier.ReplyText(t.Context(), state.ChannelReplyTarget{Kind: test.kind, ID: "700000000000000001", UUID: "synthetic-reply"}, "synthetic reply")
			if status != feishumeeting.Failed || err == nil {
				t.Fatal("inactive meeting did not fail before platform sending")
			}
		})
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected automatic-entry reply invoked the platform CLI")
	}
}

type waitingMeetingHost struct {
	started chan string
}

type countingMeetingReplier struct{ calls int }

func (r *countingMeetingReplier) ReplyText(context.Context, state.ChannelReplyTarget, string) (string, error) {
	r.calls++
	return feishumeeting.Accepted, nil
}

func TestCanceledMeetingTurnCannotSendUsingAReplacementLifetime(t *testing.T) {
	oldMeeting, endOld := context.WithCancel(t.Context())
	defer endOld()
	replier := &countingMeetingReplier{}
	parent := &channel.Service{TextReplier: replier}
	oldTurn := bindMeetingLifetime(parent, oldMeeting)
	endOld()
	// The global reply adapter can serve a replacement meeting, but the old
	// turn's Background finalization must not call it with fresh authorization.
	status, err := oldTurn.TextReplier.ReplyText(context.Background(), state.ChannelReplyTarget{Kind: state.ReplyTargetFeishuMeeting, ID: "700000000000000001", UUID: "synthetic-old-turn"}, "old canceled reply")
	if status != feishumeeting.Failed || !errors.Is(err, context.Canceled) || replier.calls != 0 {
		t.Fatal("a canceled turn acquired the replacement meeting's reply capability")
	}
	newTurn := bindMeetingLifetime(parent, t.Context())
	if status, err := newTurn.TextReplier.ReplyText(t.Context(), state.ChannelReplyTarget{}, "new reply"); status != feishumeeting.Accepted || err != nil || replier.calls != 1 {
		t.Fatal("the ended lifetime affected a fresh meeting reply")
	}
}

func (h waitingMeetingHost) Session(context.Context, string) (state.Session, error) {
	return state.Session{ID: "synthetic-session"}, nil
}

func (h waitingMeetingHost) RunChannel(ctx context.Context, _ state.Session, input, _ string) (state.Run, error) {
	h.started <- input
	<-ctx.Done()
	return state.Run{}, ctx.Err()
}

func TestMeetingEndCancelsNativeWorkAcrossProjectResolution(t *testing.T) {
	for _, source := range []string{"default", "project", "project set"} {
		t.Run(source, func(t *testing.T) {
			meeting, end := context.WithCancel(t.Context())
			defer end()
			native := waitingMeetingHost{started: make(chan string, 1)}
			parent := &channel.Service{Host: native,
				ResolveHost: func(context.Context, string) (channel.Host, assembly.Runtime, error) {
					return native, assembly.Runtime{}, nil
				},
				ResolveProjects: func(context.Context, []string, string) (channel.Host, assembly.Runtime, error) {
					return native, assembly.Runtime{}, nil
				}}
			child := bindMeetingLifetime(parent, meeting)
			var selected channel.Host
			switch source {
			case "default":
				selected = child.Host
			case "project":
				selected, _, _ = child.ResolveHost(t.Context(), "project")
			case "project set":
				selected, _, _ = child.ResolveProjects(t.Context(), []string{"first", "second"}, "first")
			}
			done := make(chan error, 1)
			go func() {
				_, err := selected.RunChannel(t.Context(), state.Session{}, "current meeting", "receipt")
				done <- err
			}()
			select {
			case <-native.started:
			case <-time.After(5 * time.Second):
				t.Fatal("native request did not start")
			}
			end()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("meeting end did not cancel native execution")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native request survived meeting end")
			}
			if _, err := selected.Session(t.Context(), ""); !errors.Is(err, context.Canceled) {
				t.Fatal("ended meeting could create another native session")
			}
			if _, ok := parent.Host.(waitingMeetingHost); !ok {
				t.Fatal("meeting binding changed the parent service")
			}
		})
	}
}

func TestMeetingContextsRemainIndependent(t *testing.T) {
	first, endFirst := context.WithCancel(t.Context())
	second, endSecond := context.WithCancel(t.Context())
	defer endFirst()
	defer endSecond()
	firstTurn, releaseFirst := meetingContext(t.Context(), first)
	secondTurn, releaseSecond := meetingContext(t.Context(), second)
	defer releaseFirst()
	defer releaseSecond()
	endFirst()
	select {
	case <-firstTurn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("first meeting context did not end")
	}
	if secondTurn.Err() != nil {
		t.Fatal("ending one meeting canceled another meeting")
	}
}

type entryRoutingFixture func(context.Context, routing.Request) (routing.Decision, error)

func (f entryRoutingFixture) Decide(ctx context.Context, request routing.Request) (routing.Decision, error) {
	return f(ctx, request)
}

func TestMeetingEndCancelsRoutingAndProjectResolution(t *testing.T) {
	for _, phase := range []string{"routing", "catalog", "project", "project set"} {
		t.Run(phase, func(t *testing.T) {
			meeting, end := context.WithCancel(t.Context())
			defer end()
			entered := make(chan struct{}, 1)
			wait := func(ctx context.Context) error { entered <- struct{}{}; <-ctx.Done(); return ctx.Err() }
			parent := &channel.Service{
				Router: entryRoutingFixture(func(ctx context.Context, _ routing.Request) (routing.Decision, error) {
					return routing.Decision{}, wait(ctx)
				}),
				Projects: func(ctx context.Context) ([]state.Project, error) { return nil, wait(ctx) },
				ResolveHost: func(ctx context.Context, _ string) (channel.Host, assembly.Runtime, error) {
					return nil, assembly.Runtime{}, wait(ctx)
				},
				ResolveProjects: func(ctx context.Context, _ []string, _ string) (channel.Host, assembly.Runtime, error) {
					return nil, assembly.Runtime{}, wait(ctx)
				},
			}
			child := bindMeetingLifetime(parent, meeting)
			done := make(chan error, 1)
			go func() {
				var err error
				switch phase {
				case "routing":
					_, err = child.Router.Decide(t.Context(), routing.Request{})
				case "catalog":
					_, err = child.Projects(t.Context())
				case "project":
					_, _, err = child.ResolveHost(t.Context(), "project")
				case "project set":
					_, _, err = child.ResolveProjects(t.Context(), []string{"first", "second"}, "first")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("turn phase did not start")
			}
			end()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("meeting end did not cancel the in-flight phase")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ended meeting retained a global worker slot")
			}
		})
	}
}

func TestAutomaticPollRegistryRequiresBaselineAndJoinsStoppedReceivers(t *testing.T) {
	cfg := feishumeeting.Config{Profile: "fixture", AppID: "cli_fixture", BotOpenID: "ou_bot", MeetingID: "700000000000000001", TenantID: "fixture-tenant", AllowedSenderOpenIDs: []string{"ou_human"}, PollInterval: 5 * time.Second, AutoJoin: true}
	entered, allowReady, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	registry := &meetingPollRegistry{service: &channel.Service{}, meetings: map[string]*meetingPoll{}, failures: make(chan meetingPollFailure, 4), observe: func(string, string) error { return nil },
		run: func(ctx context.Context, _ feishumeeting.Config, _ func(context.Context, channel.Message) error, observe func(string, string) error) error {
			defer close(exited)
			close(entered)
			select {
			case <-allowReady:
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := observe("connected", "synthetic baseline"); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}}
	defer registry.Close()
	started := make(chan error, 1)
	go func() { started <- registry.Start(t.Context(), cfg) }()
	<-entered
	if _, err := registry.Context(cfg.MeetingID); err == nil {
		t.Fatal("an unbaselined receiver authorized native work")
	}
	close(allowReady)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Context(cfg.MeetingID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Stop(cfg.MeetingID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before the receiver exited")
	}
	if _, err := registry.Context(cfg.MeetingID); err == nil {
		t.Fatal("a stopped receiver still authorized meeting work")
	}
}
