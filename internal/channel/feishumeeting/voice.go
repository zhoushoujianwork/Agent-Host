package feishumeeting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"agent-host/internal/channel"
	"agent-host/internal/state"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

// Source identity of every voice-originated request. Meeting audio carries no
// verified speaker, so no human Open ID, owner or private profile is ever used.
const (
	VoiceSenderID   = "meeting-audio"
	VoiceSenderName = "Unattributed meeting audio"
	// VoiceTextMarker prefixes admitted task text so the model and the durable
	// record both show that the speaker is unverified.
	VoiceTextMarker = "[Source: unattributed meeting audio transcript. The speaker's identity is unverified; do not attribute this transcript to an allowlisted user. The text may be misrecognized.]\n"

	maxTracks          = 8
	maxFrameBytes      = 256 << 10
	maxUtteranceQueue  = 4
	maxSpeechQueue     = 8
	maxPending         = 64
	pendingTTL         = 30 * time.Minute
	maxTranscriptBytes = 4 << 10
	maxSpokenBytes     = 2000
	maxSpeechAudio     = 24000 * 2 * 120 // two minutes of PCM per reply
	asrChunkBytes      = 16000 * 2 / 10  // 100 ms
	playChunkBytes     = 24000 * 2 / 10  // 100 ms
	maxASRSegments     = 32              // distinct final segments per utterance
	maxASRBytes        = 16 << 10        // aggregate final transcript bytes per utterance
	asrTimeout         = 30 * time.Second
	speechTimeout      = 3 * time.Minute // synthesis plus paced playback of one reply
	clearTimeout       = 2 * time.Second
	submitTimeout      = 5 * time.Second
	defaultHoldoff     = 500 * time.Millisecond
	playAhead          = 200 * time.Millisecond
)

// VoiceBridge turns completed unattributed meeting utterances into durable
// Host channel messages and speaks only replies of those voice requests. It is
// half duplex: input is discarded while its own playback (plus a short tail)
// is active. It does not reconnect; a failed bridge stays detached.
type VoiceBridge struct {
	Config    VoiceConfig
	MeetingID string
	TenantID  string
	ClientID  string
	Voice     string
	Audio     AudioOpener
	ASR       speech.ASRProvider
	TTS       speech.TTSProvider
	// Submit must only persist the message, as Service.ReceiveMessage does.
	Submit  func(context.Context, channel.Message) error
	Observe func(kind, message string)

	// Replaced by tests only.
	Now           func() time.Time
	Sleep         func(context.Context, time.Duration) error
	Holdoff       time.Duration
	SpeechTimeout time.Duration

	mu       sync.Mutex
	session  string
	seq      uint64
	closed   bool
	pending  map[string]time.Time
	speakQ   chan speechJob
	playing  int
	quietEnd time.Time
	rejected map[string]bool
	// playEnd is when audio handed to Play so far finishes sounding:
	// max(now, previous end) + duration, advanced per Play call. dirty is set
	// while Play output may still be queued and no clear has been attempted.
	playEnd time.Time
	dirty   bool
	// gen changes at the start and end of every playback so input state from
	// before it is never continued after it.
	gen uint64
}

type speechJob struct {
	uuid, text string
}

func (b *VoiceBridge) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *VoiceBridge) sleep(ctx context.Context, d time.Duration) error {
	if b.Sleep != nil {
		return b.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (b *VoiceBridge) observe(kind, format string, args ...any) {
	if b.Observe != nil {
		b.Observe(kind, fmt.Sprintf(format, args...))
	}
}

func (b *VoiceBridge) validate() error {
	if err := b.Config.Validate(); err != nil {
		return err
	}
	if !b.Config.Enabled || b.Audio == nil || b.ASR == nil || b.TTS == nil || b.Submit == nil || b.MeetingID == "" || b.TenantID == "" || b.ClientID == "" || strings.TrimSpace(b.Voice) == "" {
		return errors.New("Feishu voice bridge requires enabled configuration, audio, ASR, TTS, a submit handler and meeting identity")
	}
	return nil
}

// Run connects the meeting audio, calls ready once connected, and processes
// audio until ctx ends or the audio session fails. It never reconnects. Every
// goroutine it starts has exited when it returns.
func (b *VoiceBridge) Run(ctx context.Context, ready func()) (err error) {
	if err := b.validate(); err != nil {
		return err
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return errors.New("Feishu voice session identity unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	audio, err := b.Audio.Open(ctx, b.MeetingID)
	if err != nil {
		return fmt.Errorf("Feishu meeting audio could not connect: %w", err)
	}
	b.mu.Lock()
	if b.closed || b.session != "" {
		b.mu.Unlock()
		_ = audio.Close()
		return errors.New("Feishu voice bridge is single use")
	}
	b.session = hex.EncodeToString(raw[:])
	b.pending = map[string]time.Time{}
	b.rejected = map[string]bool{}
	b.speakQ = make(chan speechJob, maxSpeechQueue)
	b.mu.Unlock()

	utterances := make(chan []byte, maxUtteranceQueue)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); b.transcribeLoop(ctx, utterances) }()
	go func() { defer workers.Done(); b.speakLoop(ctx, audio) }()
	defer func() {
		cancel()
		b.mu.Lock()
		b.closed = true
		b.pending = nil
		b.mu.Unlock()
		workers.Wait()
		if b.stillSounding() {
			b.clearPlayback(audio) // bounded by clearTimeout
		}
		err = errors.Join(err, audio.Close())
	}()
	if ready != nil {
		ready()
	}
	b.observe("voice.connected", "meeting audio connected; unattributed audio is half duplex and not replayed")
	return b.inputLoop(ctx, audio, utterances)
}

type voiceTrack struct {
	resample resampler24to16
	vad      segmenter
}

func (b *VoiceBridge) inputLoop(ctx context.Context, audio AudioSession, utterances chan<- []byte) error {
	tracks := map[string]*voiceTrack{}
	frames, errs := audio.Frames(), audio.Errors()
	gen, _ := b.inputState()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return fmt.Errorf("Feishu meeting audio failed: %w", err)
			}
		case f, ok := <-frames:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				// The session may close Frames before this loop sees its
				// terminal error; read it so the failure is not lost.
				select {
				case err, ok := <-audio.Errors():
					if ok && err != nil {
						return fmt.Errorf("Feishu meeting audio failed: %w", err)
					}
				default:
				}
				return errors.New("Feishu meeting audio ended")
			}
			// Half duplex: resampler, VAD and partial speech from before any
			// playback never continue after it, whether or not frames arrived
			// while it was active.
			g, quiet := b.inputState()
			if g != gen {
				gen = g
				clear(tracks)
			}
			if quiet {
				continue
			}
			if len(f.PCM) > maxFrameBytes || len(f.Track) > 128 {
				b.observe("voice.audio.rejected", "oversized audio frame dropped")
				continue
			}
			t := tracks[f.Track]
			if t == nil {
				if len(tracks) >= maxTracks {
					b.rejectTrackOnce(f.Track)
					continue
				}
				t = &voiceTrack{}
				tracks[f.Track] = t
			}
			for _, u := range t.vad.Write(t.resample.Write(f.PCM)) {
				select {
				case utterances <- u:
				default:
					b.observe("voice.utterance.dropped", "utterance queue is full; one completed utterance was discarded")
				}
			}
		}
	}
}

func (b *VoiceBridge) rejectTrackOnce(track string) {
	b.mu.Lock()
	first := !b.rejected[track] && len(b.rejected) < 64
	if first {
		b.rejected[track] = true
	}
	b.mu.Unlock()
	if first {
		b.observe("voice.audio.rejected", "audio track limit reached; an additional track was ignored")
	}
}

// inputState returns the playback generation and whether input is suppressed.
func (b *VoiceBridge) inputState() (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen, b.playing > 0 || b.now().Before(b.quietEnd)
}

func (b *VoiceBridge) beginPlayback() {
	b.mu.Lock()
	b.playing++
	b.gen++
	b.mu.Unlock()
}

// endPlayback holds input off until the scheduled end of the audio plus the tail.
func (b *VoiceBridge) endPlayback() {
	b.mu.Lock()
	b.playing--
	b.gen++
	b.quietEnd = laterOf(b.playEnd, b.now()).Add(b.holdoff())
	b.mu.Unlock()
}

func (b *VoiceBridge) stillSounding() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dirty && b.playEnd.After(b.now())
}

func (b *VoiceBridge) hasQueued() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dirty
}

func laterOf(a, c time.Time) time.Time {
	if a.After(c) {
		return a
	}
	return c
}

func (b *VoiceBridge) transcribeLoop(ctx context.Context, utterances <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case pcm := <-utterances:
			b.handleUtterance(ctx, pcm)
		}
	}
}

func (b *VoiceBridge) handleUtterance(ctx context.Context, pcm []byte) {
	text, err := b.transcribe(ctx, pcm)
	if err != nil {
		if ctx.Err() == nil {
			b.observe("voice.asr.failed", "utterance transcription failed: %v", err)
		}
		return
	}
	request, ok := StripWakePrefix(b.Config.Normalized().WakePrefix, text)
	if !ok {
		return // not addressed to the assistant; the transcript is discarded
	}
	if len(request) > maxTranscriptBytes {
		b.observe("voice.request.rejected", "wake-prefixed transcript exceeds the size limit")
		return
	}
	if err := b.admit(ctx, request); err != nil && ctx.Err() == nil {
		b.observe("voice.request.failed", "voice request was not durably received: %v", err)
	}
}

func (b *VoiceBridge) nextID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return "voice-" + b.session + "-" + strconv.FormatUint(b.seq, 10)
}

// replyUUID mirrors the durable reply identity channel.Service assigns to a
// meeting message, so the later text reply can be correlated to this request.
func (b *VoiceBridge) replyUUID(m channel.Message) string {
	return channel.Key("channel-reply-v1", state.ReplyTargetFeishuMeeting, b.ClientID, m.TenantID, m.ConversationID, m.ID)
}

func (b *VoiceBridge) admit(ctx context.Context, request string) error {
	m := channel.Message{
		ID: b.nextID(), ConversationID: b.MeetingID, ConversationType: "2",
		SenderID: VoiceSenderID, SenderName: VoiceSenderName, TenantID: b.TenantID, MessageType: "text",
		Text: VoiceTextMarker + request, CreatedAt: b.now().UTC(),
		ReplyTarget: state.ChannelReplyTarget{Kind: state.ReplyTargetFeishuMeeting, ID: b.MeetingID},
	}
	uuid := b.replyUUID(m)
	if err := b.register(uuid); err != nil {
		return err
	}
	submit, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	if err := b.Submit(submit, m); err != nil {
		b.forget(uuid)
		return err
	}
	b.observe("voice.request.accepted", "message=%s admitted from unattributed meeting audio", m.ID)
	return nil
}

func (b *VoiceBridge) register(uuid string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending == nil {
		return errors.New("voice bridge is stopped")
	}
	now := b.now()
	for k, at := range b.pending {
		if now.Sub(at) > pendingTTL {
			delete(b.pending, k)
		}
	}
	if len(b.pending) >= maxPending {
		return errors.New("too many voice requests are awaiting replies")
	}
	b.pending[uuid] = now
	return nil
}

func (b *VoiceBridge) forget(uuid string) {
	b.mu.Lock()
	delete(b.pending, uuid)
	b.mu.Unlock()
}

// Reply reports the durable text reply of a request. Only an accepted reply of
// a registered voice request is queued for speech; the text outcome is never
// changed by anything that happens afterward. It does not block.
func (b *VoiceBridge) Reply(uuid string, accepted bool, text string) {
	b.mu.Lock()
	at, voice := b.pending[uuid]
	expired := voice && b.now().Sub(at) > pendingTTL
	delete(b.pending, uuid)
	queue, closed := b.speakQ, b.closed
	b.mu.Unlock()
	if expired {
		b.observe("voice.speech.expired", "voice reply correlation expired; no speech was queued")
		return
	}
	if !voice || !accepted || closed || queue == nil {
		return
	}
	text = strings.TrimSpace(text)
	if len(text) > maxSpokenBytes {
		text = strings.TrimSpace(text[:maxSpokenBytes])
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		b.observe("voice.speech.truncated", "reply text was shortened for speech")
	}
	if text == "" || !utf8.ValidString(text) {
		return
	}
	select {
	case queue <- speechJob{uuid: uuid, text: text}:
	default:
		b.observe("voice.speech.dropped", "speech queue is full; the text reply remains delivered")
	}
}

func (b *VoiceBridge) speakLoop(ctx context.Context, audio AudioSession) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-b.speakQ:
			if err := b.speak(ctx, audio, job); err != nil {
				if ctx.Err() == nil {
					b.observe("voice.speech.failed", "speech failed after the text reply was delivered: %v", err)
				}
				// Also on shutdown: audio already handed to Play must not outlive it.
				if b.hasQueued() {
					b.clearPlayback(audio)
				}
			}
		}
	}
}

// clearPlayback discards queued playback within clearTimeout. It is attempted
// once per batch of Play calls; a failure is reported, never retried.
func (b *VoiceBridge) clearPlayback(audio AudioSession) {
	b.mu.Lock()
	b.dirty = false
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), clearTimeout)
	defer cancel()
	if err := audio.ClearPlayback(ctx); err != nil {
		b.observe("voice.playback.clear_failed", "pending playback could not be cleared: %v", err)
		return
	}
	b.mu.Lock()
	now := b.now()
	b.playEnd = now
	if b.playing == 0 {
		b.quietEnd = now.Add(b.holdoff())
	}
	b.mu.Unlock()
}

func (b *VoiceBridge) speechTimeout() time.Duration {
	if b.SpeechTimeout > 0 {
		return b.SpeechTimeout
	}
	return speechTimeout
}

func (b *VoiceBridge) speak(parent context.Context, audio AudioSession, job speechJob) (err error) {
	timeout := b.speechTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	defer func() {
		if err != nil && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("speech did not finish within %s: %w", timeout, err)
		}
	}()
	b.mu.Lock()
	session := b.session
	b.mu.Unlock()
	stream, err := b.TTS.Synthesize(ctx, speech.TTSRequest{SessionID: "feishu-voice-" + session, TurnID: "tts-" + job.uuid[:min(16, len(job.uuid))], Voice: b.Voice, Text: job.text, Encoding: "pcm_s16le", SampleRate: audioRate})
	if err != nil {
		return err
	}
	defer stream.Close()
	started := false
	defer func() {
		if started {
			b.endPlayback()
		}
	}()
	var carry []byte
	var total int
	chunks, errs := stream.Chunks(), stream.Errors()
	for chunks != nil || errs != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return err
			}
		case c, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if c.SampleRate != audioRate {
				return errors.New("speech audio is not 24 kHz PCM")
			}
			total += len(c.PCM)
			if total > maxSpeechAudio {
				return errors.New("speech audio exceeds the playback limit")
			}
			if len(c.PCM) == 0 {
				continue
			}
			if !started {
				started = true
				b.beginPlayback()
			}
			data := append(carry, c.PCM...)
			carry = nil
			for len(data) >= playChunkBytes {
				if err := b.play(ctx, audio, data[:playChunkBytes]); err != nil {
					return err
				}
				data = data[playChunkBytes:]
			}
			carry = append([]byte(nil), data...)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A completed stream must be non-empty whole 16-bit samples; nothing is
	// trimmed or invented to make it succeed.
	if total == 0 {
		return errors.New("speech provider completed without audio")
	}
	if total%2 != 0 {
		return errors.New("speech provider completed with a partial PCM sample")
	}
	if len(carry) > 0 {
		return b.play(ctx, audio, carry)
	}
	return nil
}

// play keeps delivery near realtime. Each Play extends the scheduled end to
// max(now, previous end) + duration, and it waits first so that no more than
// playAhead is queued ahead of the clock, even after the provider stalled.
func (b *VoiceBridge) play(ctx context.Context, audio AudioSession, pcm []byte) error {
	duration := time.Duration(len(pcm)/2) * time.Second / audioRate
	b.mu.Lock()
	lead := max(b.playEnd.Sub(b.now()), 0) + duration
	b.mu.Unlock()
	if lead > playAhead {
		if err := b.sleep(ctx, lead-playAhead); err != nil {
			return err
		}
	}
	b.mu.Lock()
	b.dirty = true // Play may enqueue before it reports an error
	b.mu.Unlock()
	if err := audio.Play(ctx, pcm); err != nil {
		return err
	}
	b.mu.Lock()
	b.playEnd = laterOf(b.playEnd, b.now()).Add(duration)
	b.mu.Unlock()
	return nil
}

func (b *VoiceBridge) holdoff() time.Duration {
	if b.Holdoff > 0 {
		return b.Holdoff
	}
	return defaultHoldoff
}

// transcribe sends one completed utterance. Writing, the explicit end and
// draining run concurrently so provider backpressure cannot stall completion.
// The writer goroutine has exited when it returns: cancelling the context and
// closing the stream unblock any pending write.
func (b *VoiceBridge) transcribe(ctx context.Context, pcm []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, asrTimeout)
	defer cancel()
	b.mu.Lock()
	b.seq++
	id := "feishu-voice-" + b.session + "-asr-" + strconv.FormatUint(b.seq, 10)
	b.mu.Unlock()
	stream, err := b.ASR.Open(ctx, speech.ASRConfig{SessionID: id, Encoding: "pcm_s16le", SampleRate: asrRate})
	if err != nil {
		return "", err
	}
	inputDone := make(chan error, 1)
	writerExit := make(chan struct{})
	defer func() {
		cancel()
		_ = stream.Close()
		<-writerExit
	}()
	go func() {
		defer close(writerExit)
		for off := 0; off < len(pcm); off += asrChunkBytes {
			end := min(off+asrChunkBytes, len(pcm))
			if err := stream.WriteAudio(ctx, pcm[off:end]); err != nil {
				inputDone <- err
				return
			}
		}
		inputDone <- stream.EndUtterance(ctx)
	}()
	var finals []speech.TranscriptEvent
	events, errs, input := stream.Events(), stream.Errors(), inputDone
	for events != nil || errs != nil || input != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if e.Final && strings.TrimSpace(e.Text) != "" {
				finals = reviseFinals(finals, e)
				if len(finals) > maxASRSegments {
					return "", errors.New("transcript has too many final segments")
				}
				if finalBytes(finals) > maxASRBytes {
					return "", errors.New("transcript exceeds the size limit")
				}
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return "", err
			}
		case err := <-input:
			input = nil
			if err != nil {
				return "", err
			}
		}
	}
	parts := make([]string, len(finals))
	for i, e := range finals {
		parts[i] = strings.TrimSpace(e.Text)
	}
	return strings.Join(parts, " "), ctx.Err()
}

func finalBytes(finals []speech.TranscriptEvent) (n int) {
	for _, e := range finals {
		n += len(e.Text)
	}
	return n
}

// reviseFinals keeps first-seen order and replaces repeated revisions of one
// segment, identified by segment ID, else start time, else exact text.
func reviseFinals(finals []speech.TranscriptEvent, e speech.TranscriptEvent) []speech.TranscriptEvent {
	for i, p := range finals {
		var same bool
		switch {
		case e.SegmentID != "":
			same = p.SegmentID == e.SegmentID
		case e.StartMS != 0 || e.EndMS != 0:
			same = p.SegmentID == "" && (p.StartMS != 0 || p.EndMS != 0) && p.StartMS == e.StartMS
		default:
			same = p.SegmentID == "" && p.StartMS == 0 && p.EndMS == 0 && p.Text == e.Text
		}
		if same {
			finals[i] = e
			return finals
		}
	}
	return append(finals, e)
}

// StripWakePrefix returns the request after an explicit leading wake word. The
// word must stand alone (not "AIR ..."), and an empty request is not admitted.
func StripWakePrefix(prefix, text string) (string, bool) {
	text = strings.TrimLeftFunc(text, isWakeSeparator)
	rest, want := text, []rune(prefix)
	for _, w := range want {
		r, n := utf8.DecodeRuneInString(rest)
		if n == 0 || r == utf8.RuneError || unicode.ToLower(r) != unicode.ToLower(w) {
			return "", false
		}
		rest = rest[n:]
	}
	if len(want) == 0 {
		return "", false
	}
	if next, n := utf8.DecodeRuneInString(rest); n > 0 && isASCIIWord(next) && isASCIIWord(want[len(want)-1]) {
		return "", false
	}
	rest = strings.TrimSpace(strings.TrimLeftFunc(rest, isWakeSeparator))
	return rest, rest != "" && utf8.ValidString(rest)
}

func isASCIIWord(r rune) bool {
	return r < 0x80 && (r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
}

func isWakeSeparator(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsPunct(r) && r != '/' && r != '#'
}
