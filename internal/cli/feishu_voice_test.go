package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

const voiceTestMeeting = "700000000000000001"

func voiceTestConfig() feishumeeting.Config {
	return feishumeeting.Config{Profile: "fixture", AppID: "cli_fixture", BotOpenID: "ou_bot", MeetingID: voiceTestMeeting, TenantID: "fixture-tenant",
		AllowedSenderOpenIDs: []string{"ou_human"}, PollInterval: 5 * time.Second, AutoJoin: true,
		Voice: feishumeeting.VoiceConfig{Enabled: true, AcceptUnattributedAudio: true, WakePrefix: "AI",
			ASR: feishumeeting.VoiceASR{Endpoint: "ws://127.0.0.1:1/asr", ResourceID: "fixture-resource", APIKeyEnv: "FIXTURE_VOICE_ASR_KEY"},
			TTS: feishumeeting.VoiceTTS{Endpoint: "ws://127.0.0.1:1/api/v1/tts/ws_binary", Cluster: "fixture-cluster", Voice: "fixture-voice", AppIDEnv: "FIXTURE_VOICE_TTS_APP", AccessTokenEnv: "FIXTURE_VOICE_TTS_TOKEN"}}}
}

func setVoiceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FIXTURE_VOICE_ASR_KEY", "asr-secret-value")
	t.Setenv("FIXTURE_VOICE_TTS_APP", "tts-app-secret")
	t.Setenv("FIXTURE_VOICE_TTS_TOKEN", "tts-token-secret")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- fakes ----

type fakeAudio struct {
	frames chan feishumeeting.AudioFrame
	errs   chan error
	mu     sync.Mutex
	played [][]byte
	clears int
	closed bool
}

func (a *fakeAudio) Frames() <-chan feishumeeting.AudioFrame { return a.frames }
func (a *fakeAudio) Errors() <-chan error                    { return a.errs }
func (a *fakeAudio) Play(_ context.Context, pcm []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.played = append(a.played, append([]byte(nil), pcm...))
	return nil
}
func (a *fakeAudio) ClearPlayback(context.Context) error {
	a.mu.Lock()
	a.clears++
	a.mu.Unlock()
	return nil
}
func (a *fakeAudio) Close() error {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	return nil
}
func (a *fakeAudio) isClosed() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.closed }
func (a *fakeAudio) playedCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.played)
}
func (a *fakeAudio) clearCount() int { a.mu.Lock(); defer a.mu.Unlock(); return a.clears }

// speak queues 400 ms of loud 24 kHz audio followed by 700 ms of silence, in
// 20 ms frames, which the energy VAD must close as one utterance.
func (a *fakeAudio) speak(track string) {
	for i := 0; i < 55; i++ {
		pcm := make([]byte, 960)
		if i < 20 {
			for s := 0; s < 480; s++ {
				v := int16(8000)
				if s%2 == 1 {
					v = -8000
				}
				binary.LittleEndian.PutUint16(pcm[s*2:], uint16(v))
			}
		}
		a.frames <- feishumeeting.AudioFrame{Track: track, PCM: pcm}
	}
}

type fakeOpener struct {
	mu       sync.Mutex
	opens    int
	err      error
	gate     chan struct{}
	sessions []*fakeAudio
}

func (o *fakeOpener) Open(ctx context.Context, meetingID string) (feishumeeting.AudioSession, error) {
	o.mu.Lock()
	o.opens++
	err, gate := o.err, o.gate
	o.mu.Unlock()
	if meetingID != voiceTestMeeting {
		return nil, errors.New("unexpected meeting")
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	a := &fakeAudio{frames: make(chan feishumeeting.AudioFrame, 256), errs: make(chan error, 2)}
	o.mu.Lock()
	o.sessions = append(o.sessions, a)
	o.mu.Unlock()
	return a, nil
}

func (o *fakeOpener) count() int { o.mu.Lock(); defer o.mu.Unlock(); return o.opens }
func (o *fakeOpener) session(i int) *fakeAudio {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.sessions) {
		return nil
	}
	return o.sessions[i]
}

type fakeASR struct {
	mu    sync.Mutex
	texts []string
	opens int
	bytes int
}

func (p *fakeASR) Open(_ context.Context, cfg speech.ASRConfig) (speech.ASRStream, error) {
	if cfg.Encoding != "pcm_s16le" || cfg.SampleRate != 16000 {
		return nil, errors.New("unexpected ASR format")
	}
	p.mu.Lock()
	text := p.texts[min(p.opens, len(p.texts)-1)]
	p.opens++
	p.mu.Unlock()
	return &fakeASRStream{p: p, text: text, events: make(chan speech.TranscriptEvent, 4), errs: make(chan error)}, nil
}

type fakeASRStream struct {
	p      *fakeASR
	text   string
	events chan speech.TranscriptEvent
	errs   chan error
}

func (s *fakeASRStream) WriteAudio(_ context.Context, pcm []byte) error {
	s.p.mu.Lock()
	s.p.bytes += len(pcm)
	s.p.mu.Unlock()
	return nil
}
func (s *fakeASRStream) EndUtterance(context.Context) error {
	// A partial and a superseded final precede the final revision.
	s.events <- speech.TranscriptEvent{Text: "ignored partial", SegmentID: "1"}
	s.events <- speech.TranscriptEvent{Text: "old revision", SegmentID: "1", Final: true}
	s.events <- speech.TranscriptEvent{Text: s.text, SegmentID: "1", Final: true}
	close(s.events)
	close(s.errs)
	return nil
}
func (s *fakeASRStream) Events() <-chan speech.TranscriptEvent { return s.events }
func (s *fakeASRStream) Errors() <-chan error                  { return s.errs }
func (s *fakeASRStream) Close() error                          { return nil }

type fakeTTS struct {
	mu    sync.Mutex
	texts []string
	err   error
}

func (p *fakeTTS) Synthesize(_ context.Context, req speech.TTSRequest) (speech.AudioStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	p.texts = append(p.texts, req.Text)
	chunks := make(chan speech.AudioChunk, 1)
	chunks <- speech.AudioChunk{PCM: make([]byte, 4800), SampleRate: 24000}
	close(chunks)
	errs := make(chan error)
	close(errs)
	return fakeAudioStream{chunks: chunks, errs: errs}, nil
}
func (p *fakeTTS) spoken() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...)
}

type fakeAudioStream struct {
	chunks chan speech.AudioChunk
	errs   chan error
}

func (s fakeAudioStream) Chunks() <-chan speech.AudioChunk { return s.chunks }
func (s fakeAudioStream) Errors() <-chan error             { return s.errs }
func (s fakeAudioStream) Close() error                     { return nil }

type observed struct {
	mu     sync.Mutex
	events []string
}

func (o *observed) observe(kind, message string) error {
	o.mu.Lock()
	o.events = append(o.events, kind+" "+message)
	o.mu.Unlock()
	return nil
}
func (o *observed) has(kind string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, e := range o.events {
		if strings.HasPrefix(e, kind+" ") {
			return true
		}
	}
	return false
}

func newTestVoice(opener *fakeOpener, asr speech.ASRProvider, tts speech.TTSProvider, submit func(context.Context, channel.Message) error, o *observed) *meetingVoice {
	v := newMeetingVoice(asr, tts, submit, o.observe)
	v.open = func(feishumeeting.Config) feishumeeting.AudioOpener { return opener }
	return v
}

func voiceTestService(t *testing.T) *channel.Service {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &channel.Service{Channel: state.ReplyTargetFeishuMeeting, DisableReactions: true, TextReplier: &countingMeetingReplier{},
		Store: store, Host: waitingMeetingHost{started: make(chan string, 1)}, ClientID: "cli_fixture", Profile: "voice-profile", Scope: t.TempDir(), Timeout: time.Second}
}

// ---- configuration ----

func TestFeishuVoiceConfigurationParsesNormalizesAndChecksCredentialsLocally(t *testing.T) {
	cfg := meetingConfigFixture()
	enabled := feishumeeting.VoiceConfig{Enabled: true, AcceptUnattributedAudio: true,
		ASR: voiceTestConfig().Voice.ASR, TTS: voiceTestConfig().Voice.TTS}
	cfg.Voice = &enabled
	path := filepath.Join(t.TempDir(), "meeting.json")
	writeBotJSON(t, path, cfg)
	loaded, err := loadFeishuMeetingConfig(path)
	if err != nil || !loaded.Voice.Enabled || loaded.Voice.WakePrefix != "AI" {
		t.Fatalf("voice config not loaded with the explicit default wake prefix: %v %+v", err, loaded.Voice)
	}
	blockMeetingCheckNetwork(t)
	for _, name := range []string{"FIXTURE_VOICE_ASR_KEY", "FIXTURE_VOICE_TTS_APP", "FIXTURE_VOICE_TTS_TOKEN"} {
		t.Setenv(name, "")
	}
	_, _, err = feishuVoiceProviders(loaded, os.LookupEnv)
	if err == nil || !strings.Contains(err.Error(), "FIXTURE_VOICE_ASR_KEY") {
		t.Fatalf("missing credentials were accepted or not named: %v", err)
	}
	setVoiceEnv(t)
	asr, tts, err := feishuVoiceProviders(loaded, os.LookupEnv)
	if err != nil || asr == nil || tts == nil {
		t.Fatal("valid voice configuration was rejected", err)
	}
	for _, secret := range []string{"asr-secret-value", "tts-app-secret", "tts-token-secret"} {
		if strings.Contains(path, secret) {
			t.Fatal("test setup leaked a secret into a path")
		}
	}
	if raw, _ := os.ReadFile(path); bytes.Contains(raw, []byte("secret")) {
		t.Fatal("configuration file contains credential values")
	}
	// A config without a voice block, or a disabled one, needs nothing.
	if asr, tts, err := feishuVoiceProviders(feishumeeting.Config{}, func(string) (string, bool) { t.Fatal("env read"); return "", false }); asr != nil || tts != nil || err != nil {
		t.Fatal("disabled voice resolved providers")
	}
}

func TestFeishuVoiceConfigurationRejectsUnsafeOrMalformedVoice(t *testing.T) {
	for name, mutate := range map[string]func(*feishumeeting.VoiceConfig){
		"unattributed audio not accepted": func(v *feishumeeting.VoiceConfig) { v.AcceptUnattributedAudio = false },
		"plaintext secret as env name":    func(v *feishumeeting.VoiceConfig) { v.ASR.APIKeyEnv = "sk-1234 secret" },
		"bad ASR endpoint":                func(v *feishumeeting.VoiceConfig) { v.ASR.Endpoint = "https://example.invalid/asr" },
		"endpoint credentials":            func(v *feishumeeting.VoiceConfig) { v.ASR.Endpoint = "wss://user:pw@example.invalid/asr" },
		"bad TTS path":                    func(v *feishumeeting.VoiceConfig) { v.TTS.Endpoint = "wss://example.invalid/api/v3/tts/bidirection" },
		"missing voice":                   func(v *feishumeeting.VoiceConfig) { v.TTS.Voice = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := meetingConfigFixture()
			voice := voiceTestConfig().Voice
			mutate(&voice)
			cfg.Voice = &voice
			path := filepath.Join(t.TempDir(), "meeting.json")
			writeBotJSON(t, path, cfg)
			if _, err := loadFeishuMeetingConfig(path); err == nil {
				t.Fatal("unsafe voice configuration accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "meeting.json")
	valid := `{"profile":"fixture-meeting","app_id":"cli_fixture","bot_open_id":"ou_bot","meeting_id":"700000000000000001","tenant_id":"fixture-tenant","allowed_sender_open_ids":["ou_human"],"poll_interval_seconds":5,"voice":{"enabled":true,"api_key":"plaintext"}}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFeishuMeetingConfig(path); err == nil {
		t.Fatal("an unknown plaintext credential field was accepted")
	}
}

func TestFeishuMeetingCheckReportsVoiceOnlyWhenConfigured(t *testing.T) {
	for _, configured := range []bool{false, true} {
		root := t.TempDir()
		config, agents := filepath.Join(root, "meeting.json"), filepath.Join(root, "agents.json")
		cfg := meetingConfigFixture()
		if configured {
			voice := voiceTestConfig().Voice
			cfg.Voice = &voice
			setVoiceEnv(t)
		}
		writeBotJSON(t, config, cfg)
		writeBotJSON(t, agents, agentCatalog{DefaultAgent: "helper", Agents: []agentDefinition{{ID: "helper", Name: "Meeting helper", Projects: []string{}, KnowledgeSources: []string{}}}})
		native, marker := meetingCommandFixtures(t)
		t.Setenv("TYPESAFE_API_KEY", serviceTestKey)
		t.Setenv("JEV_API_KEY", "")
		transport := blockMeetingCheckNetwork(t)
		var out, errout bytes.Buffer
		err := Run(context.Background(), []string{"feishu-meeting", "check", "--config", config, "--agents-config", agents,
			"--home", filepath.Join(root, "state"), "--native-profile", filepath.Join(root, "native"), "--workspace", root, "--claude", native,
			"--model", "fixture-model", "--jev-provider", "typesafe"}, nil, &out, &errout)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), `"voice_supported":true`); got != configured {
			t.Fatalf("voice_supported = %v, configured = %v: %s", got, configured, out.String())
		}
		if got := strings.Contains(out.String(), "_and_realtime_audio"); got != configured {
			t.Fatalf("audio transport reporting does not match configuration: %s", out.String())
		}
		if got := strings.Contains(out.String(), "wake_prefixed_unattributed_meeting_audio"); got != configured {
			t.Fatalf("audio admission scope reporting does not match configuration: %s", out.String())
		}
		if transport.calls.Load() != 0 {
			t.Fatal("check contacted the network")
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("check invoked a subprocess")
		}
		if strings.Contains(out.String()+errout.String(), "secret-value") || strings.Contains(out.String(), "tts-token") {
			t.Fatal("check printed credentials")
		}
	}
	// Missing credentials fail the local check without a connection.
	root := t.TempDir()
	config := filepath.Join(root, "meeting.json")
	cfg := meetingConfigFixture()
	voice := voiceTestConfig().Voice
	cfg.Voice = &voice
	writeBotJSON(t, config, cfg)
	for _, name := range []string{"FIXTURE_VOICE_ASR_KEY", "FIXTURE_VOICE_TTS_APP", "FIXTURE_VOICE_TTS_TOKEN"} {
		t.Setenv(name, "")
	}
	native, _ := meetingCommandFixtures(t)
	t.Setenv("TYPESAFE_API_KEY", serviceTestKey)
	blockMeetingCheckNetwork(t)
	err := Run(context.Background(), []string{"feishu-meeting", "check", "--config", config, "--home", filepath.Join(root, "state"),
		"--workspace", root, "--claude", native, "--jev-provider", "typesafe"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "FIXTURE_VOICE_ASR_KEY") {
		t.Fatalf("check accepted missing credentials: %v", err)
	}
}

// ---- reply correlation and failure separation ----

func TestVoiceRequestIsUnattributedAndOnlyItsAcceptedReplyIsSpoken(t *testing.T) {
	service := voiceTestService(t)
	cfg := voiceTestConfig()
	opener, o := &fakeOpener{}, &observed{}
	asr := &fakeASR{texts: []string{"hello everyone", "AI, summarize the plan"}}
	tts := &fakeTTS{}
	voice := newTestVoice(opener, asr, tts, service.ReceiveMessage, o)
	defer voice.Close()
	if err := voice.Start(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	audio := opener.session(0)
	audio.speak("track-1") // no wake prefix: discarded
	waitFor(t, "first transcript", func() bool { asr.mu.Lock(); defer asr.mu.Unlock(); return asr.opens == 1 })
	time.Sleep(50 * time.Millisecond)
	if messages, err := service.Store.ChannelMessages(t.Context(), 10); err != nil || len(messages) != 0 {
		t.Fatalf("an unaddressed utterance was admitted: %v %d", err, len(messages))
	}
	audio.speak("track-1")
	var message state.ChannelMessage
	waitFor(t, "voice request", func() bool {
		messages, err := service.Store.ChannelMessages(t.Context(), 10)
		if err == nil && len(messages) == 1 {
			message = messages[0]
		}
		return message.ID != ""
	})
	if asr.bytes == 0 || message.SenderID != feishumeeting.VoiceSenderID || message.SenderName != feishumeeting.VoiceSenderName ||
		message.Channel != state.ReplyTargetFeishuMeeting || message.ConversationType != "2" || message.ReplyTarget == nil || message.ReplyTarget.ID != voiceTestMeeting {
		t.Fatalf("voice request has the wrong source identity: %+v", message)
	}
	if !strings.HasPrefix(message.Text, feishumeeting.VoiceTextMarker) || !strings.HasSuffix(message.Text, "summarize the plan") || strings.Contains(message.Text, "old revision") {
		t.Fatalf("voice text does not mark its source or uses a stale revision: %q", message.Text)
	}
	if message.SenderID == "ou_human" || strings.HasPrefix(message.AudienceID, "owner") || message.AudienceID == state.OwnerAudienceID {
		t.Fatal("voice request borrowed a human or owner identity")
	}
	counting := &countingMeetingReplier{}
	replier := withMeetingVoice(counting, voice)
	target := *message.ReplyTarget
	// A reply to a text request with another UUID is never spoken.
	other := target
	other.UUID = "text-origin-request"
	if status, err := replier.ReplyText(t.Context(), other, "text answer"); status != feishumeeting.Accepted || err != nil {
		t.Fatal(status, err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(tts.spoken()) != 0 || audio.playedCount() != 0 {
		t.Fatal("a non-voice reply was spoken")
	}
	if status, err := replier.ReplyText(t.Context(), target, "Plan summary"); status != feishumeeting.Accepted || err != nil {
		t.Fatal(status, err)
	}
	waitFor(t, "playback", func() bool { return audio.playedCount() > 0 })
	if spoken := tts.spoken(); len(spoken) != 1 || spoken[0] != "Plan summary" {
		t.Fatalf("wrong speech text: %v", spoken)
	}
	// The correlation is consumed: a second delivery of the same reply is silent.
	_, _ = replier.ReplyText(t.Context(), target, "Plan summary again")
	time.Sleep(50 * time.Millisecond)
	if len(tts.spoken()) != 1 {
		t.Fatal("a reply was spoken twice")
	}
}

func TestVoiceSpeechFailureNeverChangesTextOutcome(t *testing.T) {
	service := voiceTestService(t)
	cfg := voiceTestConfig()
	opener, o := &fakeOpener{}, &observed{}
	tts := &fakeTTS{err: errors.New("synthetic TTS outage")}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"AI status please"}}, tts, service.ReceiveMessage, o)
	defer voice.Close()
	if err := voice.Start(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	audio := opener.session(0)
	audio.speak("t")
	var target state.ChannelReplyTarget
	waitFor(t, "voice request", func() bool {
		messages, _ := service.Store.ChannelMessages(t.Context(), 10)
		if len(messages) == 1 {
			target = *messages[0].ReplyTarget
		}
		return target.UUID != ""
	})
	counting := &countingMeetingReplier{}
	status, err := withMeetingVoice(counting, voice).ReplyText(t.Context(), target, "All good")
	if status != feishumeeting.Accepted || err != nil || counting.calls != 1 {
		t.Fatalf("speech changed the durable text result: %v %v %d", status, err, counting.calls)
	}
	waitFor(t, "independent speech failure", func() bool { return o.has("meeting.voice.speech.failed") })
	if audio.isClosed() {
		t.Fatal("a speech failure detached the meeting audio")
	}
	// A failed or unknown text reply is never spoken.
	failing := withMeetingVoice(failingReplier{}, voice)
	if status, err := failing.ReplyText(t.Context(), target, "x"); status != feishumeeting.Failed || err == nil {
		t.Fatal("wrapper hid a text failure")
	}
}

type failingReplier struct{}

func (failingReplier) ReplyText(context.Context, state.ChannelReplyTarget, string) (string, error) {
	return feishumeeting.Failed, errors.New("synthetic text failure")
}

// ---- lifetimes ----

func TestVoiceStartWaitsForAudioAndFailureLeavesNothingAttached(t *testing.T) {
	opener, o := &fakeOpener{gate: make(chan struct{})}, &observed{}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	defer voice.Close()
	started := make(chan error, 1)
	go func() { started <- voice.Start(t.Context(), voiceTestConfig()) }()
	waitFor(t, "audio open attempt", func() bool { return opener.count() == 1 })
	select {
	case <-started:
		t.Fatal("voice was ready before its audio connected")
	case <-time.After(50 * time.Millisecond):
	}
	close(opener.gate)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := voice.Start(t.Context(), voiceTestConfig()); err == nil {
		t.Fatal("a second bridge attached to the same meeting")
	}
	if err := voice.Stop(voiceTestMeeting); err != nil || !opener.session(0).isClosed() {
		t.Fatal("stop did not close the audio session", err)
	}
	failing := &fakeOpener{err: errors.New("synthetic audio refusal")}
	voice = newTestVoice(failing, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	if err := voice.Start(t.Context(), voiceTestConfig()); err == nil || len(voice.bridges) != 0 {
		t.Fatal("failed audio startup reported readiness or left a bridge", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := voice.Start(canceled, voiceTestConfig()); err == nil || len(voice.bridges) != 0 {
		t.Fatal("a canceled startup left a bridge")
	}
}

func testRegistry(run func(context.Context, feishumeeting.Config, func(context.Context, channel.Message) error, func(string, string) error) error) *meetingPollRegistry {
	return &meetingPollRegistry{service: &channel.Service{}, meetings: map[string]*meetingPoll{}, failures: make(chan meetingPollFailure, 4),
		observe: func(string, string) error { return nil }, run: run}
}

// holdingPoll reaches its baseline when allow is closed, then runs until
// canceled or fail is closed (an unexpected poll failure).
func holdingPoll(allow, fail chan struct{}) func(context.Context, feishumeeting.Config, func(context.Context, channel.Message) error, func(string, string) error) error {
	return func(ctx context.Context, _ feishumeeting.Config, _ func(context.Context, channel.Message) error, observe func(string, string) error) error {
		if allow != nil {
			select {
			case <-allow:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := observe("connected", "synthetic baseline"); err != nil {
			return err
		}
		select {
		case <-fail:
			return errors.New("synthetic poll failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestAutomaticVoiceAttachesAfterBaselineAndStopsWithPolling(t *testing.T) {
	allow := make(chan struct{})
	registry := testRegistry(holdingPoll(allow, nil))
	defer registry.Close()
	opener, o := &fakeOpener{}, &observed{}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	defer voice.Close()
	start, stop := voice.hooks(registry.Context, registry.Start, registry.Stop)
	started := make(chan error, 1)
	go func() { started <- start(t.Context(), voiceTestConfig()) }()
	time.Sleep(50 * time.Millisecond)
	if opener.count() != 0 {
		t.Fatal("voice connected before the history baseline")
	}
	close(allow)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if opener.count() != 1 {
		t.Fatal("voice did not attach after the baseline")
	}
	if _, err := registry.Context(voiceTestMeeting); err != nil {
		t.Fatal(err)
	}
	if err := stop(voiceTestMeeting); err != nil {
		t.Fatal(err)
	}
	if !opener.session(0).isClosed() {
		t.Fatal("stopping the meeting left audio connected")
	}
	if _, err := registry.Context(voiceTestMeeting); err == nil {
		t.Fatal("stopping the meeting left polling attached")
	}
	if len(voice.bridges) != 0 || len(registry.meetings) != 0 {
		t.Fatal("stopped meeting retained runtime state")
	}
}

func TestAutomaticVoiceStartupFailureStopsPollingToo(t *testing.T) {
	registry := testRegistry(holdingPoll(nil, nil))
	defer registry.Close()
	opener, o := &fakeOpener{err: errors.New("synthetic audio refusal")}, &observed{}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	defer voice.Close()
	start, _ := voice.hooks(registry.Context, registry.Start, registry.Stop)
	if err := start(t.Context(), voiceTestConfig()); err == nil {
		t.Fatal("voice startup failure was reported ready")
	}
	if _, err := registry.Context(voiceTestMeeting); err == nil || len(registry.meetings) != 0 || len(voice.bridges) != 0 {
		t.Fatal("startup failure leaked polling or voice")
	}
	// A polling failure before the baseline never opens audio.
	registry = testRegistry(func(context.Context, feishumeeting.Config, func(context.Context, channel.Message) error, func(string, string) error) error {
		return errors.New("synthetic poll refusal")
	})
	counted := &fakeOpener{}
	voice = newTestVoice(counted, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	start, _ = voice.hooks(registry.Context, registry.Start, registry.Stop)
	if err := start(t.Context(), voiceTestConfig()); err == nil || counted.count() != 0 {
		t.Fatal("audio opened for a meeting whose polling failed")
	}
}

func TestAutomaticVoiceFollowsPollFailureAndParentShutdown(t *testing.T) {
	fail := make(chan struct{})
	registry := testRegistry(holdingPoll(nil, fail))
	defer registry.Close()
	opener, o := &fakeOpener{}, &observed{}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	defer voice.Close()
	start, _ := voice.hooks(registry.Context, registry.Start, registry.Stop)
	if err := start(t.Context(), voiceTestConfig()); err != nil {
		t.Fatal(err)
	}
	close(fail)
	select {
	case failure := <-registry.failures:
		if failure.id != voiceTestMeeting {
			t.Fatal("wrong meeting reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll failure was not reported")
	}
	waitFor(t, "audio close after poll failure", func() bool { return opener.session(0).isClosed() })

	// Service shutdown (parent context) tears down both.
	registry2 := testRegistry(holdingPoll(nil, nil))
	defer registry2.Close()
	opener2 := &fakeOpener{}
	voice2 := newTestVoice(opener2, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	start2, _ := voice2.hooks(registry2.Context, registry2.Start, registry2.Stop)
	parent, cancel := context.WithCancel(t.Context())
	if err := start2(parent, voiceTestConfig()); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitFor(t, "audio close after shutdown", func() bool { return opener2.session(0).isClosed() })
	voice2.Close()
	registry2.Close()
	if len(voice2.bridges) != 0 || len(registry2.meetings) != 0 {
		t.Fatal("shutdown retained runtime state")
	}
}

func TestVoiceAudioFailureDetachesWithoutReconnectOrTextImpact(t *testing.T) {
	registry := testRegistry(holdingPoll(nil, nil))
	defer registry.Close()
	opener, o := &fakeOpener{}, &observed{}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, func(context.Context, channel.Message) error { return nil }, o)
	defer voice.Close()
	start, stop := voice.hooks(registry.Context, registry.Start, registry.Stop)
	if err := start(t.Context(), voiceTestConfig()); err != nil {
		t.Fatal(err)
	}
	opener.session(0).errs <- errors.New("synthetic audio loss")
	waitFor(t, "voice failure observation", func() bool { return o.has("meeting.voice.failed") })
	waitFor(t, "audio close", func() bool { return opener.session(0).isClosed() })
	time.Sleep(50 * time.Millisecond)
	if opener.count() != 1 {
		t.Fatal("audio reconnected after a failure")
	}
	if _, err := registry.Context(voiceTestMeeting); err != nil {
		t.Fatal("audio failure ended text polling", err)
	}
	voice.mu.Lock()
	_, evidence := voice.bridges[voiceTestMeeting]
	voice.mu.Unlock()
	if !evidence {
		t.Fatal("completed detached bridge was erased before the meeting stopped")
	}
	if err := stop(voiceTestMeeting); err != nil || len(voice.bridges) != 0 {
		t.Fatal("stop did not clear the detached bridge", err)
	}
}

// ---- fixed meeting ----

func fixedRun(connect bool, after func(ctx context.Context) error) func(context.Context, feishumeeting.Config, func(context.Context, channel.Message) error, func(string, string) error) error {
	return func(ctx context.Context, _ feishumeeting.Config, _ func(context.Context, channel.Message) error, observe func(string, string) error) error {
		if connect {
			if err := observe("connected", "synthetic baseline"); err != nil {
				return err
			}
		}
		if after != nil {
			return after(ctx)
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestFixedMeetingReadinessWaitsForVoiceAndClosesWithRuntime(t *testing.T) {
	cfg := voiceTestConfig()
	cfg.AutoJoin = false
	service := voiceTestService(t)
	opener, o := &fakeOpener{gate: make(chan struct{})}, &observed{}
	voice := newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, service.ReceiveMessage, o)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveFeishuFixedMeeting(ctx, cfg, service, voice, o.observe, fixedRun(true, nil)) }()
	waitFor(t, "audio open attempt", func() bool { return opener.count() == 1 })
	time.Sleep(50 * time.Millisecond)
	if o.has("channel.connected") {
		t.Fatal("channel was ready before its audio connected")
	}
	close(opener.gate)
	waitFor(t, "ready", func() bool { return o.has("channel.connected") })
	if _, wrapped := service.TextReplier.(voiceReplier); !wrapped {
		t.Fatal("text replier was not wrapped for voice correlation")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !opener.session(0).isClosed() || len(voice.bridges) != 0 {
		t.Fatal("shutdown leaked the audio session")
	}
}

func TestFixedMeetingVoiceStartupAndReceiverFailureCleanUp(t *testing.T) {
	cfg := voiceTestConfig()
	cfg.AutoJoin = false
	service := voiceTestService(t)
	o := &observed{}
	failing := &fakeOpener{err: errors.New("synthetic audio refusal")}
	voice := newTestVoice(failing, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, service.ReceiveMessage, o)
	if err := serveFeishuFixedMeeting(t.Context(), cfg, service, voice, o.observe, fixedRun(true, nil)); err == nil || o.has("channel.connected") || len(voice.bridges) != 0 {
		t.Fatal("voice startup failure still reported readiness", err)
	}
	service = voiceTestService(t)
	opener := &fakeOpener{}
	voice = newTestVoice(opener, &fakeASR{texts: []string{"x"}}, &fakeTTS{}, service.ReceiveMessage, o)
	boom := errors.New("synthetic poll failure")
	err := serveFeishuFixedMeeting(t.Context(), cfg, service, voice, o.observe, fixedRun(true, func(context.Context) error { return boom }))
	if !errors.Is(err, boom) || !opener.session(0).isClosed() || len(voice.bridges) != 0 {
		t.Fatal("receiver failure left audio attached", err)
	}
}

func TestFixedMeetingWithoutVoiceKeepsTextOnlyReplier(t *testing.T) {
	cfg := voiceTestConfig()
	cfg.AutoJoin, cfg.Voice = false, feishumeeting.VoiceConfig{}
	service := voiceTestService(t)
	original := service.TextReplier
	o := &observed{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveFeishuFixedMeeting(ctx, cfg, service, nil, o.observe, fixedRun(true, nil)) }()
	waitFor(t, "ready", func() bool { return o.has("channel.connected") })
	if service.TextReplier != original {
		t.Fatal("a text-only meeting wrapped its replier")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
