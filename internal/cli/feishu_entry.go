package cli

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/channel"
	"agent-host/internal/channel/feishu"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/event"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

type meetingEntryAuthority interface {
	CanReply(string) bool
}

type meetingContextSource interface {
	Context(string) (context.Context, error)
}

// Entry and model execution share one durable queue and concurrency limit.
// Each meeting keeps an independently cancelable polling/native lifetime.
func serveFeishuAutomaticEntry(ctx context.Context, cfg feishumeeting.Config, home string, service *channel.Service, sink event.Sink, observe func(string, string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	registry := &meetingPollRegistry{home: home, service: service, observe: observe, meetings: map[string]*meetingPoll{}, failures: make(chan meetingPollFailure, 4)}
	defer registry.Close()
	var gateMu sync.Mutex
	meetingEnabled := cfg.AutoJoin || cfg.MeetingID != ""
	managerReady, streamReady, emittedReady := !meetingEnabled, false, false
	runtimeName, readyMessage := "Feishu automatic meeting entry", "Feishu automatic meeting entry ready; meeting questions use polling"
	if cfg.IMChat {
		runtimeName, readyMessage = "Feishu text channel", "Feishu private/group text receiver ready"
		if meetingEnabled {
			readyMessage += "; meeting entry and polling are ready"
		}
	}
	var receiverObserve func(string, string) error
	ready := func() error {
		gateMu.Lock()
		defer gateMu.Unlock()
		if managerReady && streamReady && !emittedReady && receiverObserve != nil {
			if err := receiverObserve("connected", readyMessage); err != nil {
				return err
			}
			emittedReady = true
		}
		return nil
	}
	entryObserve := func(kind, status string, data map[string]any) error {
		if err := sink(event.Event{Version: event.Version, At: time.Now().UTC(), Type: kind, Phase: "channel", Component: "channel.feishu-meeting", Status: status, Data: data}); err != nil {
			return err
		}
		if kind == "meeting.entry.ready" {
			gateMu.Lock()
			managerReady = true
			gateMu.Unlock()
			return ready()
		}
		return nil
	}
	path := filepath.Join(home, "feishu-meeting", "entry-"+channel.Key(cfg.Profile, cfg.AppID, cfg.TenantID, cfg.BotOpenID)+".json")
	var manager *feishumeeting.EntryManager
	if meetingEnabled {
		entryCfg := cfg
		entryCfg.AutoJoin = true // A manual seed is only restored through a read.
		var err error
		manager, err = feishumeeting.OpenEntryManager(entryCfg, path, nil, feishumeeting.EntryHooks{StartMeeting: registry.Start, StopMeeting: registry.Stop, Observe: entryObserve})
		if err != nil {
			return err
		}
	}
	var meetingReplier channel.TextReplier
	if manager != nil {
		meetingReplier = automaticMeetingReplier{config: cfg, authority: manager, contexts: registry}
	}
	service.TextReplier = meetingReplier
	if cfg.IMChat {
		service.TextReplier = feishuTextReplier{Meeting: meetingReplier, IM: feishu.Replier{Config: feishuIMConfig(cfg)}}
	}
	resolve := service.ResolveAgent
	service.ResolveAgent = func(ctx context.Context, message state.ChannelMessage) (*channel.Service, error) {
		if message.Channel == state.ReplyTargetFeishu {
			return resolve(ctx, message)
		}
		if manager == nil || !manager.CanReply(message.ConversationID) {
			return nil, errors.New("meeting is not active for this application")
		}
		meeting, err := registry.Context(message.ConversationID)
		if err != nil {
			return nil, err
		}
		ctx, cancel := meetingContext(ctx, meeting)
		defer cancel()
		child, err := resolve(ctx, message)
		if err != nil {
			return nil, err
		}
		return bindMeetingLifetime(child, meeting), nil
	}
	receiver := feishumeeting.EntryReceiver{Config: cfg}
	if cfg.IMChat {
		parser := feishu.Parser{Config: feishuIMConfig(cfg)}
		opened := time.Now().UTC().Add(-2 * time.Second)
		receiver.HandleIM = func(ctx context.Context, raw []byte) error {
			message, err := parser.Parse(raw)
			if err != nil {
				return err
			}
			if message.ID == "" {
				return nil
			}
			if message.CreatedAt.Before(opened) {
				return observe("im.ignored", "Feishu IM message predates this receiver; it was not replayed")
			}
			return service.ReceiveMessage(ctx, message)
		}
	}
	handleEntry := func(ctx context.Context, entry feishumeeting.EntryEvent) error {
		if manager == nil || !cfg.AutoJoin && entry.Kind != feishumeeting.EntryEnd {
			return nil
		}
		return manager.Receive(ctx, entry)
	}
	return serveChannelRuntime(ctx, runtimeName, func(ctx context.Context, observed func(string, string) error) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		gateMu.Lock()
		receiverObserve = observed
		gateMu.Unlock()
		done := make(chan error, 3)
		count := 1
		if manager != nil {
			count += 2
			go func() { done <- manager.Run(ctx) }()
		}
		go func() {
			done <- receiver.Run(ctx, handleEntry, func(kind, message string) error {
				if kind == "disconnected" || kind == "reconnecting" {
					gateMu.Lock()
					streamReady = false
					gateMu.Unlock()
				}
				if err := observe("meeting.entry."+kind, message); err != nil {
					return err
				}
				if kind == "connected" {
					gateMu.Lock()
					streamReady = true
					gateMu.Unlock()
					return ready()
				}
				return nil
			})
		}()
		if manager != nil {
			go func() {
				for {
					select {
					case <-ctx.Done():
						done <- ctx.Err()
						return
					case failure := <-registry.failures:
						if err := manager.ReportPollFailure(ctx, failure.id, failure.lifetime); err != nil {
							done <- err
							return
						}
					}
				}
			}()
		}
		err := <-done
		cancel()
		for i := 1; i < count; i++ {
			err = errors.Join(err, <-done)
		}
		return err
	}, service.Work, observe)
}

type automaticMeetingReplier struct {
	config    feishumeeting.Config
	authority meetingEntryAuthority
	contexts  meetingContextSource
}

func (r automaticMeetingReplier) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	if target.Kind != state.ReplyTargetFeishuMeeting || !r.authority.CanReply(target.ID) {
		return feishumeeting.Failed, errors.New("meeting reply destination is no longer active")
	}
	meeting, err := r.contexts.Context(target.ID)
	if err != nil {
		return feishumeeting.Failed, err
	}
	ctx, cancel := meetingContext(ctx, meeting)
	defer cancel()
	cfg := r.config
	cfg.MeetingID, cfg.AutoJoin = target.ID, false
	return (feishumeeting.Replier{Config: cfg}).ReplyText(ctx, target, text)
}

type meetingPoll struct {
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{}
	done   chan struct{}
}

type meetingPollFailure struct {
	id       string
	lifetime context.Context
}

type meetingPollRegistry struct {
	mu       sync.Mutex
	home     string
	service  *channel.Service
	observe  func(string, string) error
	meetings map[string]*meetingPoll
	failures chan meetingPollFailure
	run      func(context.Context, feishumeeting.Config, func(context.Context, channel.Message) error, func(string, string) error) error
}

func (r *meetingPollRegistry) Start(ctx context.Context, cfg feishumeeting.Config) error {
	if err := cfg.ValidateMeeting(); err != nil {
		return err
	}
	r.mu.Lock()
	if _, exists := r.meetings[cfg.MeetingID]; exists {
		r.mu.Unlock()
		return errors.New("meeting receiver is already attached")
	}
	childCtx, cancel := context.WithCancel(ctx)
	poll := &meetingPoll{ctx: childCtx, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	r.meetings[cfg.MeetingID] = poll
	r.mu.Unlock()
	run := r.run
	if run == nil {
		run = func(ctx context.Context, cfg feishumeeting.Config, handle func(context.Context, channel.Message) error, observed func(string, string) error) error {
			receiver := feishumeeting.Receiver{Config: cfg, ProgressPath: filepath.Join(r.home, "feishu-meeting", channel.Key(cfg.Profile, cfg.AppID, cfg.TenantID, cfg.BotOpenID, cfg.MeetingID)+".json")}
			return receiver.Run(ctx, handle, observed)
		}
	}
	go func() {
		var once sync.Once
		_ = run(childCtx, cfg, r.service.ReceiveMessage, func(kind, message string) error {
			if err := r.observe("meeting.poll."+kind, "meeting="+cfg.MeetingID+" "+message); err != nil {
				return err
			}
			if kind == "connected" {
				once.Do(func() { close(poll.ready) })
			}
			return nil
		})
		unexpected := childCtx.Err() == nil
		cancel()
		close(poll.done)
		if unexpected {
			select {
			case r.failures <- meetingPollFailure{id: cfg.MeetingID, lifetime: ctx}:
			case <-ctx.Done():
			}
		}
	}()
	select {
	case <-ctx.Done():
		_ = r.Stop(cfg.MeetingID)
		return ctx.Err()
	case <-poll.done:
		_ = r.Stop(cfg.MeetingID)
		return errors.New("meeting polling stopped before its history baseline was ready")
	case <-poll.ready:
		if poll.ctx.Err() != nil {
			_ = r.Stop(cfg.MeetingID)
			return errors.New("meeting polling stopped while becoming ready")
		}
		return nil
	}
}

func (r *meetingPollRegistry) Context(id string) (context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	poll, exists := r.meetings[id]
	if !exists || poll.ctx.Err() != nil {
		return nil, errors.New("meeting receiver is not active")
	}
	select {
	case <-poll.ready:
		return poll.ctx, nil
	default:
		return nil, errors.New("meeting receiver has not established its history baseline")
	}
}

func (r *meetingPollRegistry) Stop(id string) error {
	r.mu.Lock()
	poll, exists := r.meetings[id]
	if exists {
		delete(r.meetings, id)
	}
	r.mu.Unlock()
	if exists {
		poll.cancel()
		<-poll.done
	}
	return nil
}

func (r *meetingPollRegistry) Close() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.meetings))
	for id := range r.meetings {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		_ = r.Stop(id)
	}
}

type meetingScopedHost struct {
	host    channel.Host
	meeting context.Context
}

type meetingScopedReplier struct {
	replier channel.TextReplier
	meeting context.Context
}

type meetingScopedRouter struct {
	router  routing.Decider
	meeting context.Context
}

func (r meetingScopedRouter) Decide(ctx context.Context, request routing.Request) (routing.Decision, error) {
	ctx, cancel := meetingContext(ctx, r.meeting)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return routing.Decision{}, err
	}
	return r.router.Decide(ctx, request)
}

// Turn finalization uses a fresh bounded context. It must not acquire a newer
// meeting lifetime after its own polling/native runtime has been canceled.
func (r meetingScopedReplier) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	ctx, cancel := meetingContext(ctx, r.meeting)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return feishumeeting.Failed, err
	}
	return r.replier.ReplyText(ctx, target, text)
}

func meetingContext(ctx, meeting context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(meeting, cancel)
	if meeting.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (h meetingScopedHost) Session(ctx context.Context, id string) (state.Session, error) {
	ctx, cancel := meetingContext(ctx, h.meeting)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return state.Session{}, err
	}
	return h.host.Session(ctx, id)
}

func (h meetingScopedHost) RunChannel(ctx context.Context, session state.Session, input, receipt string) (state.Run, error) {
	ctx, cancel := meetingContext(ctx, h.meeting)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return state.Run{}, err
	}
	return h.host.RunChannel(ctx, session, input, receipt)
}

func bindMeetingLifetime(service *channel.Service, meeting context.Context) *channel.Service {
	child := *service
	if child.Host != nil {
		child.Host = meetingScopedHost{host: child.Host, meeting: meeting}
	}
	if child.TextReplier != nil {
		child.TextReplier = meetingScopedReplier{replier: child.TextReplier, meeting: meeting}
	}
	if child.Router != nil {
		child.Router = meetingScopedRouter{router: child.Router, meeting: meeting}
	}
	if projects := child.Projects; projects != nil {
		child.Projects = func(ctx context.Context) ([]state.Project, error) {
			ctx, cancel := meetingContext(ctx, meeting)
			defer cancel()
			return projects(ctx)
		}
	}
	if resolve := child.ResolveHost; resolve != nil {
		child.ResolveHost = func(ctx context.Context, id string) (channel.Host, assembly.Runtime, error) {
			ctx, cancel := meetingContext(ctx, meeting)
			defer cancel()
			h, runtime, err := resolve(ctx, id)
			if err != nil {
				return nil, runtime, err
			}
			return meetingScopedHost{host: h, meeting: meeting}, runtime, nil
		}
	}
	if resolve := child.ResolveProjects; resolve != nil {
		child.ResolveProjects = func(ctx context.Context, ids []string, primary string) (channel.Host, assembly.Runtime, error) {
			ctx, cancel := meetingContext(ctx, meeting)
			defer cancel()
			h, runtime, err := resolve(ctx, ids, primary)
			if err != nil {
				return nil, runtime, err
			}
			return meetingScopedHost{host: h, meeting: meeting}, runtime, nil
		}
	}
	return &child
}
