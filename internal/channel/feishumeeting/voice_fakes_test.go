package feishumeeting

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-host/internal/channel"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

// fakeClock is a deterministic clock whose Sleep advances time.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// fakeAudio is an AudioSession that models how much audio is queued ahead of
// the clock, so tests can assert the queue stays bounded.
type fakeAudio struct {
	frames chan AudioFrame
	errs   chan error
	now    func() time.Time

	mu         sync.Mutex
	plays      [][]byte
	end        time.Time
	maxLead    time.Duration
	clears     int
	closes     int
	events     []string
	clearBlock bool
	playErr    error
}

func newFakeAudio(now func() time.Time) *fakeAudio {
	if now == nil {
		now = time.Now
	}
	return &fakeAudio{frames: make(chan AudioFrame, 512), errs: make(chan error, 1), now: now}
}

func (a *fakeAudio) Frames() <-chan AudioFrame { return a.frames }
func (a *fakeAudio) Errors() <-chan error      { return a.errs }

func (a *fakeAudio) Play(ctx context.Context, pcm []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.playErr != nil {
		return a.playErr
	}
	now := a.now()
	if a.end.Before(now) {
		a.end = now
	}
	a.end = a.end.Add(time.Duration(len(pcm)/2) * time.Second / audioRate)
	a.maxLead = max(a.maxLead, a.end.Sub(now))
	a.plays = append(a.plays, append([]byte(nil), pcm...))
	a.events = append(a.events, "play")
	return nil
}

func (a *fakeAudio) ClearPlayback(ctx context.Context) error {
	a.mu.Lock()
	a.clears++
	a.events = append(a.events, "clear")
	block := a.clearBlock
	a.end = a.now()
	a.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (a *fakeAudio) Close() error {
	a.mu.Lock()
	a.closes++
	a.events = append(a.events, "close")
	a.mu.Unlock()
	return nil
}

func (a *fakeAudio) snapshot() (plays, clears, closes int, lead time.Duration, events []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.plays), a.clears, a.closes, a.maxLead, append([]string(nil), a.events...)
}

// fakeASR scripts one stream per utterance.
type fakeASR struct {
	mu         sync.Mutex
	configs    []speech.ASRConfig
	streams    []*fakeASRStream
	events     []speech.TranscriptEvent
	script     func(i int) []speech.TranscriptEvent // overrides events per stream
	failWith   error
	openErr    error // delivered on Errors as soon as the stream opens
	blockWrite bool  // WriteAudio waits for cancellation or Close
	hang       bool  // EndUtterance emits nothing and never completes
}

type fakeASRStream struct {
	p        *fakeASR
	idx      int
	events   chan speech.TranscriptEvent
	errs     chan error
	closed   chan struct{}
	once     sync.Once
	endOnce  sync.Once
	audio    atomic.Int64
	started  atomic.Int32
	finished atomic.Int32
	ended    atomic.Int32
}

func (p *fakeASR) Open(_ context.Context, cfg speech.ASRConfig) (speech.ASRStream, error) {
	s := &fakeASRStream{p: p, events: make(chan speech.TranscriptEvent, 256), errs: make(chan error, 4), closed: make(chan struct{})}
	p.mu.Lock()
	s.idx = len(p.streams)
	if p.openErr != nil {
		s.errs <- p.openErr
	}
	p.configs = append(p.configs, cfg)
	p.streams = append(p.streams, s)
	p.mu.Unlock()
	return s, nil
}

func (p *fakeASR) stream(i int) *fakeASRStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < len(p.streams) {
		return p.streams[i]
	}
	return nil
}

func (s *fakeASRStream) WriteAudio(ctx context.Context, pcm []byte) error {
	s.started.Add(1)
	defer s.finished.Add(1)
	s.audio.Add(int64(len(pcm)))
	if s.p.blockWrite {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return errors.New("asr stream closed")
		}
	}
	return nil
}

func (s *fakeASRStream) EndUtterance(context.Context) error {
	s.ended.Add(1)
	if s.p.hang {
		return nil
	}
	s.endOnce.Do(func() {
		events := s.p.events
		if s.p.script != nil {
			events = s.p.script(s.idx)
		}
		for _, e := range events {
			s.events <- e
		}
		if s.p.failWith != nil {
			s.errs <- s.p.failWith
		}
		close(s.events)
		close(s.errs)
	})
	return nil
}

func (s *fakeASRStream) Events() <-chan speech.TranscriptEvent { return s.events }
func (s *fakeASRStream) Errors() <-chan error                  { return s.errs }
func (s *fakeASRStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// fakeTTS hands out scripted audio streams.
type fakeTTS struct {
	mu      sync.Mutex
	reqs    []speech.TTSRequest
	streams []*fakeAudioStream
	next    func() *fakeAudioStream
}

type fakeAudioStream struct {
	chunks chan speech.AudioChunk
	errs   chan error
	done   chan struct{}
	once   sync.Once
}

func newAudioStream(buffer int) *fakeAudioStream {
	return &fakeAudioStream{chunks: make(chan speech.AudioChunk, buffer), errs: make(chan error, 2), done: make(chan struct{})}
}

// staticStream returns a completed stream holding the given chunks.
func staticStream(chunks ...speech.AudioChunk) *fakeAudioStream {
	s := newAudioStream(len(chunks))
	for _, c := range chunks {
		s.chunks <- c
	}
	close(s.chunks)
	close(s.errs)
	return s
}

func (p *fakeTTS) Synthesize(_ context.Context, req speech.TTSRequest) (speech.AudioStream, error) {
	s := p.next()
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.streams = append(p.streams, s)
	p.mu.Unlock()
	return s, nil
}

func (s *fakeAudioStream) Chunks() <-chan speech.AudioChunk { return s.chunks }
func (s *fakeAudioStream) Errors() <-chan error             { return s.errs }
func (s *fakeAudioStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
func (s *fakeAudioStream) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func pcmChunk(n int) speech.AudioChunk {
	return speech.AudioChunk{PCM: make([]byte, n), SampleRate: audioRate}
}

// observed collects Observe calls.
type observed struct {
	mu    sync.Mutex
	items []string
}

func (o *observed) add(kind, _ string) {
	o.mu.Lock()
	o.items = append(o.items, kind)
	o.mu.Unlock()
}

func (o *observed) count(kind string) (n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, k := range o.items {
		if k == kind {
			n++
		}
	}
	return n
}

type bridgeKit struct {
	b    *VoiceBridge
	asr  *fakeASR
	tts  *fakeTTS
	aud  *fakeAudio
	obs  *observed
	mu   sync.Mutex
	subs []channel.Message
	fail error
}

func (k *bridgeKit) submitted() []channel.Message {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]channel.Message(nil), k.subs...)
}

func validVoiceConfig() VoiceConfig {
	return VoiceConfig{
		Enabled: true, AcceptUnattributedAudio: true,
		ASR: VoiceASR{Endpoint: "wss://asr.example.test/ws", ResourceID: "res", APIKeyEnv: "ASR_KEY"},
		TTS: VoiceTTS{Endpoint: "wss://tts.example.test" + ttsEndpointPath, Cluster: "c", Voice: "v", AppIDEnv: "TTS_APP", AccessTokenEnv: "TTS_TOKEN"},
	}
}

// newKit builds a bridge wired to fakes. A nil clock uses real time.
func newKit(clock *fakeClock) *bridgeKit {
	k := &bridgeKit{asr: &fakeASR{}, obs: &observed{}}
	var now func() time.Time
	if clock != nil {
		now = clock.Now
	}
	k.aud = newFakeAudio(now)
	k.tts = &fakeTTS{next: func() *fakeAudioStream { return staticStream(pcmChunk(4800)) }}
	k.b = &VoiceBridge{
		Config: validVoiceConfig(), MeetingID: "700000000000000001", TenantID: "tenant-1", ClientID: "client-1", Voice: "voice-1",
		Audio: openerFunc(func(context.Context, string) (AudioSession, error) { return k.aud, nil }),
		ASR:   k.asr, TTS: k.tts, Observe: k.obs.add,
		Submit: func(_ context.Context, m channel.Message) error {
			k.mu.Lock()
			defer k.mu.Unlock()
			if k.fail != nil {
				return k.fail
			}
			k.subs = append(k.subs, m)
			return nil
		},
		Holdoff: time.Millisecond,
	}
	if clock != nil {
		k.b.Now, k.b.Sleep = clock.Now, clock.Sleep
		k.b.Holdoff = 500 * time.Millisecond
	}
	return k
}

// prime initializes the per-session state Run would create, for tests that
// call internal methods directly.
func (k *bridgeKit) prime() *bridgeKit {
	b := k.b
	b.session = "sess"
	b.pending = map[string]time.Time{}
	b.rejected = map[string]bool{}
	b.speakQ = make(chan speechJob, maxSpeechQueue)
	return k
}

type openerFunc func(context.Context, string) (AudioSession, error)

func (f openerFunc) Open(ctx context.Context, id string) (AudioSession, error) { return f(ctx, id) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// pcm24 returns n 20 ms frames of constant-amplitude 24 kHz PCM16, as one slice.
func pcm24(frames int, amp int16) []byte {
	return appendPCM(nil, constSamples(frames*vadFrameInput, amp))
}

func pcm16k(frames int, amp int16) []byte {
	return appendPCM(nil, constSamples(frames*vadFrameSample, amp))
}

func constSamples(n int, amp int16) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = amp
	}
	return s
}
