package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"agent-host/internal/channel"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

// feishuVoiceProviders validates voice configuration and credentials locally.
// It reads only the referenced credentials and never connects. With a
// credentials_file the private file is the exclusive source; otherwise lookup
// (the process environment) is used.
func feishuVoiceProviders(cfg feishumeeting.Config, lookup func(string) (string, bool)) (speech.ASRProvider, speech.TTSProvider, error) {
	if !cfg.Voice.Enabled {
		return nil, nil, nil
	}
	if err := cfg.Voice.Validate(); err != nil {
		return nil, nil, err
	}
	if cfg.Voice.CredentialsFile != "" {
		var err error
		if lookup, err = voiceCredentialsFileLookup(cfg.Voice); err != nil {
			return nil, nil, err
		}
	}
	credentials, err := cfg.Voice.ResolveCredentials(lookup)
	if err != nil {
		return nil, nil, err
	}
	return feishumeeting.DoubaoSpeech(cfg.Voice, credentials)
}

// voiceCredentialsFileLookup loads the private credentials file once and returns
// a lookup over it. Values stay in memory only: nothing is exported to the
// process environment. Errors name neither values nor unknown keys.
func voiceCredentialsFileLookup(v feishumeeting.VoiceConfig) (func(string) (string, bool), error) {
	var raw map[string]json.RawMessage
	if err := readServiceJSON(v.CredentialsFile, &raw); err != nil {
		return nil, fmt.Errorf("Feishu voice credentials_file: %w", err)
	}
	referenced := map[string]bool{}
	for _, name := range v.ReferencedEnvNames() {
		referenced[name] = true
	}
	values := make(map[string]string, len(raw))
	for name, field := range raw {
		if !referenced[name] {
			return nil, errors.New("Feishu voice credentials_file contains a field the voice configuration does not reference")
		}
		field = bytes.TrimSpace(field)
		var value string
		if len(field) == 0 || field[0] != '"' || json.Unmarshal(field, &value) != nil {
			return nil, errors.New("Feishu voice credentials_file values must be strings")
		}
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("Feishu voice credentials_file values must be nonempty single-line strings without surrounding whitespace")
		}
		values[name] = value
	}
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }, nil
}

// meetingVoice owns one independently cancelable voice bridge per meeting.
type meetingVoice struct {
	asr     speech.ASRProvider
	tts     speech.TTSProvider
	submit  func(context.Context, channel.Message) error
	observe func(string, string) error
	open    func(feishumeeting.Config) feishumeeting.AudioOpener

	mu      sync.Mutex
	bridges map[string]*voiceRun
}

type voiceRun struct {
	bridge *feishumeeting.VoiceBridge
	cancel context.CancelFunc
	done   chan struct{}
	err    error // written before done is closed
}

func newMeetingVoice(asr speech.ASRProvider, tts speech.TTSProvider, submit func(context.Context, channel.Message) error, observe func(string, string) error) *meetingVoice {
	return &meetingVoice{asr: asr, tts: tts, submit: submit, observe: observe, bridges: map[string]*voiceRun{},
		open: func(cfg feishumeeting.Config) feishumeeting.AudioOpener {
			return feishumeeting.FeishuAudioOpener{Config: cfg}
		}}
}

// Start returns after the audio connection is ready. The bridge lives until ctx
// ends, Stop is called, or its audio session fails; it is never restarted.
func (v *meetingVoice) Start(ctx context.Context, cfg feishumeeting.Config) error {
	if err := cfg.ValidateMeeting(); err != nil {
		return err
	}
	if !cfg.Voice.Enabled {
		return errors.New("Feishu voice is not enabled")
	}
	id := cfg.MeetingID
	child, cancel := context.WithCancel(ctx)
	run := &voiceRun{cancel: cancel, done: make(chan struct{})}
	run.bridge = &feishumeeting.VoiceBridge{
		Config: cfg.Voice, MeetingID: id, TenantID: cfg.TenantID, ClientID: cfg.AppID, Voice: cfg.Voice.TTS.Voice,
		Audio: v.open(cfg), ASR: v.asr, TTS: v.tts, Submit: v.submit,
		Observe: func(kind, message string) { _ = v.observe("meeting."+kind, "meeting="+id+" "+message) },
	}
	v.mu.Lock()
	if previous := v.bridges[id]; previous != nil {
		select {
		case <-previous.done:
		default:
			v.mu.Unlock()
			cancel()
			return errors.New("meeting voice is already attached")
		}
	}
	v.bridges[id] = run
	v.mu.Unlock()
	ready := make(chan struct{})
	var once sync.Once
	go func() {
		err := run.bridge.Run(child, func() { once.Do(func() { close(ready) }) })
		unexpected := child.Err() == nil
		run.err = err
		cancel()
		close(run.done)
		if unexpected && err != nil {
			_ = v.observe("meeting.voice.failed", fmt.Sprintf("meeting=%s voice detached and is not reconnected: %v; text replies continue", id, err))
		}
	}()
	select {
	case <-ready:
		return nil
	case <-run.done:
		_ = v.Stop(id)
		return fmt.Errorf("meeting voice could not become ready: %w", run.err)
	case <-ctx.Done():
		_ = v.Stop(id)
		return ctx.Err()
	}
}

func (v *meetingVoice) Stop(id string) error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	run := v.bridges[id]
	delete(v.bridges, id)
	v.mu.Unlock()
	if run != nil {
		run.cancel()
		<-run.done
	}
	return nil
}

func (v *meetingVoice) Close() {
	if v == nil {
		return
	}
	v.mu.Lock()
	ids := make([]string, 0, len(v.bridges))
	for id := range v.bridges {
		ids = append(ids, id)
	}
	v.mu.Unlock()
	for _, id := range ids {
		_ = v.Stop(id)
	}
}

func (v *meetingVoice) reply(target state.ChannelReplyTarget, accepted bool, text string) {
	v.mu.Lock()
	run := v.bridges[target.ID]
	v.mu.Unlock()
	if run != nil {
		run.bridge.Reply(target.UUID, accepted, text)
	}
}

// voiceReplier observes the final durable text outcome. It never changes it:
// speech is queued only after an accepted text reply and cannot fail the reply.
type voiceReplier struct {
	inner channel.TextReplier
	voice *meetingVoice
}

func (r voiceReplier) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	outcome, err := r.inner.ReplyText(ctx, target, text)
	if target.Kind == state.ReplyTargetFeishuMeeting {
		r.voice.reply(target, outcome == feishumeeting.Accepted && err == nil, text)
	}
	return outcome, err
}

func withMeetingVoice(replier channel.TextReplier, voice *meetingVoice) channel.TextReplier {
	if voice == nil || replier == nil {
		return replier
	}
	return voiceReplier{inner: replier, voice: voice}
}

// hooks adapts the automatic-entry lifetime hooks. Voice attaches only after
// the polling baseline is ready and its context derives from that poll
// lifetime. Startup failure stops both; stopping a meeting stops both.
func (v *meetingVoice) hooks(pollContext func(string) (context.Context, error), startPolling func(context.Context, feishumeeting.Config) error, stopPolling func(string) error) (func(context.Context, feishumeeting.Config) error, func(string) error) {
	if v == nil {
		return startPolling, stopPolling
	}
	start := func(ctx context.Context, cfg feishumeeting.Config) error {
		if err := startPolling(ctx, cfg); err != nil {
			return err
		}
		poll, err := pollContext(cfg.MeetingID)
		if err == nil {
			err = v.Start(poll, cfg)
		}
		if err != nil {
			_ = v.Stop(cfg.MeetingID)
			_ = stopPolling(cfg.MeetingID)
			return err
		}
		return nil
	}
	stop := func(id string) error {
		_ = v.Stop(id)
		return stopPolling(id)
	}
	return start, stop
}

// serveFeishuFixedMeeting runs the fixed-meeting receiver. Voice attaches after
// the receiver's history baseline, and readiness waits for the audio connection.
// The voice bridge never outlives the receiver or the worker.
func serveFeishuFixedMeeting(ctx context.Context, cfg feishumeeting.Config, service *channel.Service, voice *meetingVoice, observe func(string, string) error,
	run func(context.Context, feishumeeting.Config, func(context.Context, channel.Message) error, func(string, string) error) error) error {
	defer voice.Close()
	service.TextReplier = withMeetingVoice(service.TextReplier, voice)
	return serveChannelRuntime(ctx, "Feishu meeting", func(ctx context.Context, observed func(string, string) error) error {
		attached := false
		return run(ctx, cfg, service.ReceiveMessage, func(kind, message string) error {
			if kind == "connected" && voice != nil && !attached {
				if err := voice.Start(ctx, cfg); err != nil {
					return err
				}
				attached = true
			}
			return observed(kind, message)
		})
	}, service.Work, observe)
}
