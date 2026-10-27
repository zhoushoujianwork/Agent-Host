package feishumeeting

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agent-host/internal/state"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

// sendSpeech queues an utterance as 20 ms 24 kHz frames: voiced then silent.
func sendSpeech(a *fakeAudio, track string) {
	for range 14 {
		a.frames <- AudioFrame{Track: track, PCM: pcm24(1, 3000)}
	}
	for range 40 {
		a.frames <- AudioFrame{Track: track, PCM: pcm24(1, 0)}
	}
}

type runResult struct {
	err   error
	ready chan struct{}
	done  chan struct{}
	stop  context.CancelFunc
}

func (k *bridgeKit) start(t *testing.T) *runResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runResult{ready: make(chan struct{}), done: make(chan struct{}), stop: cancel}
	go func() {
		defer close(r.done)
		r.err = k.b.Run(ctx, func() { close(r.ready) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	select {
	case <-r.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge never became ready")
	}
	return r
}

func (r *runResult) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func final(text, seg string, start int64) speech.TranscriptEvent {
	return speech.TranscriptEvent{Text: text, SegmentID: seg, Final: true, StartMS: start, EndMS: start + 100}
}

func TestVoiceWakeGatingAndUnattributedIdentity(t *testing.T) {
	k := newKit(nil)
	k.asr.script = func(i int) []speech.TranscriptEvent {
		switch i {
		case 0:
			return []speech.TranscriptEvent{{Text: "AIR is great", Final: true}} // not a standalone wake word
		case 1:
			return []speech.TranscriptEvent{{Text: "what is the plan", Final: true}} // no wake word
		default:
			return []speech.TranscriptEvent{{Text: "ai", Final: false}, final("AI, summarize", "s1", 0), final("the meeting", "s2", 200)}
		}
	}
	k.start(t)
	sendSpeech(k.aud, "mixed")
	waitFor(t, "first utterance", func() bool { return k.asr.stream(0) != nil && k.asr.stream(0).ended.Load() == 1 })
	k.b.Holdoff = time.Millisecond
	sendSpeech(k.aud, "mixed")
	sendSpeech(k.aud, "other")
	waitFor(t, "admitted request", func() bool { return len(k.submitted()) == 1 })
	time.Sleep(20 * time.Millisecond)

	subs := k.submitted()
	if len(subs) != 1 {
		t.Fatalf("%d requests admitted, want only the wake-prefixed one", len(subs))
	}
	m := subs[0]
	if m.SenderID != VoiceSenderID || m.SenderName != VoiceSenderName {
		t.Fatalf("identity = %q/%q, want the unattributed constants", m.SenderID, m.SenderName)
	}
	if m.Text != VoiceTextMarker+"summarize the meeting" {
		t.Fatalf("text = %q", m.Text)
	}
	if !strings.Contains(m.Text, "unverified") {
		t.Fatal("marker does not state that the speaker is unverified")
	}
	if m.ConversationID != "700000000000000001" || m.TenantID != "tenant-1" || m.ConversationType != "2" || m.MessageType != "text" {
		t.Fatalf("conversation = %+v", m)
	}
	if want := (state.ChannelReplyTarget{Kind: state.ReplyTargetFeishuMeeting, ID: "700000000000000001"}); m.ReplyTarget != want {
		t.Fatalf("reply target = %+v", m.ReplyTarget)
	}
	if !strings.HasPrefix(m.ID, "voice-") {
		t.Fatalf("message id %q", m.ID)
	}
	cfg := k.asr.configs[0]
	if cfg.SampleRate != asrRate || cfg.Encoding != "pcm_s16le" {
		t.Fatalf("ASR config = %+v", cfg)
	}
}

func TestVoiceRunSharedAudienceTracksStaySeparate(t *testing.T) {
	// Two tracks speaking at once must not blend: each is its own utterance.
	k := newKit(nil)
	k.asr.events = []speech.TranscriptEvent{final("AI hello", "", 0)}
	k.start(t)
	for i := range 54 {
		k.aud.frames <- AudioFrame{Track: "a", PCM: pcm24(1, map[bool]int16{true: 3000, false: 0}[i < 14])}
		k.aud.frames <- AudioFrame{Track: "b", PCM: pcm24(1, map[bool]int16{true: 3000, false: 0}[i < 14])}
	}
	waitFor(t, "two utterances", func() bool { return len(k.submitted()) == 2 })
	for _, m := range k.submitted() {
		if m.SenderID != VoiceSenderID {
			t.Fatalf("track leaked into identity: %q", m.SenderID)
		}
	}
	// 14 voiced + 30 silence frames + 5 preroll cannot exist yet: each stream is exactly one track.
	want := (14 + vadEndSilence) * vadFrameSample * 2
	for i := range 2 {
		if got := k.asr.stream(i).audio.Load(); got != int64(want) {
			t.Fatalf("stream %d received %d bytes, want %d (one track only)", i, got, want)
		}
	}
}

func TestReviseFinalsAndTranscript(t *testing.T) {
	k := newKit(nil).prime()
	k.asr.events = []speech.TranscriptEvent{
		{Text: "partial ignored", SegmentID: "p", Final: false},
		final("first draft", "a", 0),
		final("second", "b", 500),
		final("first revised", "a", 0),   // by segment ID
		final("  ", "c", 900),            // blank is skipped
		final("third draft", "", 1200),   // by start time
		final("third revised", "", 1200), // same start time
		{Text: "dup", Final: true},       // no times: exact text
		{Text: "dup", Final: true},
	}
	got, err := k.b.transcribe(context.Background(), pcm16k(2, 100))
	if err != nil {
		t.Fatal(err)
	}
	if want := "first revised second third revised dup"; got != want {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
	if n := k.asr.stream(0).audio.Load(); n != int64(len(pcm16k(2, 100))) {
		t.Fatalf("ASR received %d bytes", n)
	}
}

func TestTranscribeBoundsSegmentsAndBytes(t *testing.T) {
	t.Run("segments", func(t *testing.T) {
		k := newKit(nil).prime()
		for i := range maxASRSegments + 1 {
			k.asr.events = append(k.asr.events, final("x", string(rune('A'+i)), int64(i)*10))
		}
		if _, err := k.b.transcribe(context.Background(), pcm16k(1, 100)); err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("err = %v, want a segment-count failure", err)
		}
	})
	t.Run("exactly at the segment limit passes", func(t *testing.T) {
		k := newKit(nil).prime()
		for i := range maxASRSegments {
			k.asr.events = append(k.asr.events, final("x", string(rune('A'+i)), int64(i)*10))
		}
		if _, err := k.b.transcribe(context.Background(), pcm16k(1, 100)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("aggregate bytes", func(t *testing.T) {
		k := newKit(nil).prime()
		big := strings.Repeat("y", maxASRBytes/2)
		k.asr.events = []speech.TranscriptEvent{final(big, "a", 0), final(big, "b", 10), final("z", "c", 20)}
		if _, err := k.b.transcribe(context.Background(), pcm16k(1, 100)); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("err = %v, want a byte-limit failure", err)
		}
	})
}

func TestTranscribeJoinsWriterOnEveryExit(t *testing.T) {
	cases := map[string]func(k *bridgeKit) (context.Context, context.CancelFunc){
		"provider error while a write is blocked": func(k *bridgeKit) (context.Context, context.CancelFunc) {
			k.asr.blockWrite, k.asr.openErr = true, errors.New("asr provider failed")
			return context.WithCancel(context.Background())
		},
		"cancellation while a write is blocked": func(k *bridgeKit) (context.Context, context.CancelFunc) {
			k.asr.blockWrite = true
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(20*time.Millisecond, cancel)
			return ctx, cancel
		},
		"too many segments while the provider hangs": func(k *bridgeKit) (context.Context, context.CancelFunc) {
			k.asr.hang, k.asr.blockWrite = true, true
			for i := range maxASRSegments + 1 {
				k.asr.events = append(k.asr.events, final("x", string(rune('A'+i)), int64(i)))
			}
			return context.WithCancel(context.Background())
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			k := newKit(nil).prime()
			ctx, cancel := setup(k)
			defer cancel()
			// "too many segments" needs events delivered although input never ends.
			if k.asr.hang {
				k.asr.script = nil
			}
			done := make(chan error, 1)
			go func() {
				_, err := k.b.transcribe(ctx, pcm16k(5, 100))
				done <- err
			}()
			if k.asr.hang {
				waitFor(t, "stream", func() bool { return k.asr.stream(0) != nil })
				for _, e := range k.asr.events {
					k.asr.stream(0).events <- e
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("transcription unexpectedly succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("transcribe did not return")
			}
			s := k.asr.stream(0)
			if s.started.Load() != s.finished.Load() {
				t.Fatalf("writer still inside WriteAudio after return: %d started, %d finished", s.started.Load(), s.finished.Load())
			}
			select {
			case <-s.closed:
			default:
				t.Fatal("ASR stream was not closed")
			}
		})
	}
}

func TestAdmitSubmitFailureForgetsRequest(t *testing.T) {
	k := newKit(nil).prime()
	k.fail = errors.New("store unavailable")
	if err := k.b.admit(context.Background(), "do it"); err == nil {
		t.Fatal("admit hid a submit failure")
	}
	if len(k.b.pending) != 0 {
		t.Fatal("a request that was never persisted is still awaiting a reply")
	}
}

func TestReplySpeaksOnlyAcceptedVoiceRequestsOnce(t *testing.T) {
	k := newKit(nil).prime()
	admit := func() string {
		t.Helper()
		if err := k.b.admit(context.Background(), "q"); err != nil {
			t.Fatal(err)
		}
		subs := k.submitted()
		return k.b.replyUUID(subs[len(subs)-1])
	}

	rejected := admit()
	k.b.Reply(rejected, false, "text failed")
	if len(k.b.speakQ) != 0 {
		t.Fatal("a failed text reply was converted to speech")
	}
	k.b.Reply(rejected, true, "late success")
	if len(k.b.speakQ) != 0 {
		t.Fatal("a reply already reported as failed became speakable")
	}

	k.b.Reply("not-a-voice-request", true, "hello")
	if len(k.b.speakQ) != 0 {
		t.Fatal("a non-voice reply was spoken")
	}

	ok := admit()
	k.b.Reply(ok, true, "  hello  ")
	k.b.Reply(ok, true, "hello again")
	if len(k.b.speakQ) != 1 {
		t.Fatalf("%d speech jobs, want exactly one for one reply", len(k.b.speakQ))
	}
	if job := <-k.b.speakQ; job.uuid != ok || job.text != "hello" {
		t.Fatalf("job = %+v", job)
	}

	blank := admit()
	k.b.Reply(blank, true, " \n ")
	if len(k.b.speakQ) != 0 {
		t.Fatal("blank text was queued")
	}

	long := admit()
	k.b.Reply(long, true, strings.Repeat("你", 1000))
	job := <-k.b.speakQ
	if len(job.text) > maxSpokenBytes || !utf8.ValidString(job.text) {
		t.Fatalf("spoken text = %d bytes, valid=%v", len(job.text), utf8.ValidString(job.text))
	}
	if k.obs.count("voice.speech.truncated") != 1 {
		t.Fatal("truncation was not reported")
	}
}

func TestVoiceQueuesAreBounded(t *testing.T) {
	t.Run("expired reply without another request", func(t *testing.T) {
		clock := newFakeClock()
		k := newKit(clock).prime()
		if err := k.b.admit(context.Background(), "q"); err != nil {
			t.Fatal(err)
		}
		uuid := k.b.replyUUID(k.submitted()[0])
		clock.Advance(pendingTTL + time.Second)
		k.b.Reply(uuid, true, "late reply")
		if len(k.b.speakQ) != 0 || k.obs.count("voice.speech.expired") != 1 {
			t.Fatal("expired reply was spoken or its expiry was not reported")
		}
	})
	t.Run("speech queue", func(t *testing.T) {
		k := newKit(nil).prime()
		for range maxSpeechQueue + 5 {
			if err := k.b.admit(context.Background(), "q"); err != nil {
				t.Fatal(err)
			}
			subs := k.submitted()
			k.b.Reply(k.b.replyUUID(subs[len(subs)-1]), true, "spoken")
		}
		if len(k.b.speakQ) != maxSpeechQueue {
			t.Fatalf("speech queue holds %d, want %d", len(k.b.speakQ), maxSpeechQueue)
		}
		if k.obs.count("voice.speech.dropped") != 5 {
			t.Fatalf("drops reported = %d, want 5", k.obs.count("voice.speech.dropped"))
		}
	})
	t.Run("pending requests and TTL", func(t *testing.T) {
		clock := newFakeClock()
		k := newKit(clock).prime()
		for range maxPending {
			if err := k.b.admit(context.Background(), "q"); err != nil {
				t.Fatal(err)
			}
		}
		if err := k.b.admit(context.Background(), "q"); err == nil {
			t.Fatal("pending bound was not enforced")
		}
		clock.Advance(pendingTTL + time.Second)
		if err := k.b.admit(context.Background(), "q"); err != nil {
			t.Fatalf("expired requests still occupy the bound: %v", err)
		}
	})
	t.Run("tracks and utterances", func(t *testing.T) {
		k := newKit(nil).prime()
		k.aud.frames = make(chan AudioFrame) // unbuffered: a completed send means the previous frame was processed
		utterances := make(chan []byte)      // nobody reads: every completed utterance must be dropped
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- k.b.inputLoop(ctx, k.aud, utterances) }()
		for i := range maxTracks + 2 {
			k.aud.frames <- AudioFrame{Track: string(rune('a' + i)), PCM: pcm24(1, 0)}
		}
		k.aud.frames <- AudioFrame{Track: "i", PCM: pcm24(1, 0)} // same rejected track again
		for range 14 {
			k.aud.frames <- AudioFrame{Track: "a", PCM: pcm24(1, 3000)}
		}
		for range 40 {
			k.aud.frames <- AudioFrame{Track: "a", PCM: pcm24(1, 0)}
		}
		k.aud.frames <- AudioFrame{Track: "a"} // barrier
		k.aud.frames <- AudioFrame{Track: "a", PCM: make([]byte, maxFrameBytes+1)}
		k.aud.frames <- AudioFrame{Track: "a"} // barrier
		cancel()
		<-done
		if n := k.obs.count("voice.audio.rejected"); n != 3 { // 2 tracks (i, j) once each + oversized frame
			t.Fatalf("rejections = %d, want 3", n)
		}
		if k.obs.count("voice.utterance.dropped") != 1 {
			t.Fatalf("dropped utterances = %d, want 1", k.obs.count("voice.utterance.dropped"))
		}
	})
}

func TestInputLoopKeepsTerminalAudioError(t *testing.T) {
	boom := errors.New("transport reset")
	t.Run("error buffered when frames close", func(t *testing.T) {
		k := newKit(nil).prime()
		k.aud.errs <- boom
		close(k.aud.errs)
		close(k.aud.frames)
		for range 50 { // select order is random; the error must always win
			if err := k.b.inputLoop(context.Background(), k.aud, make(chan []byte, 1)); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the terminal audio error", err)
			}
			k.aud.frames, k.aud.errs = make(chan AudioFrame), make(chan error, 1)
			k.aud.errs <- boom
			close(k.aud.errs)
			close(k.aud.frames)
		}
	})
	t.Run("closed without error", func(t *testing.T) {
		k := newKit(nil).prime()
		close(k.aud.errs)
		close(k.aud.frames)
		err := k.b.inputLoop(context.Background(), k.aud, make(chan []byte, 1))
		if err == nil || !strings.Contains(err.Error(), "ended") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("run reports it", func(t *testing.T) {
		k := newKit(nil)
		r := k.start(t)
		k.aud.errs <- boom
		if err := r.wait(t); !errors.Is(err, boom) {
			t.Fatalf("Run = %v", err)
		}
		if _, _, closes, _, _ := k.aud.snapshot(); closes != 1 {
			t.Fatalf("audio closed %d times", closes)
		}
	})
}

// A fresh playback generation must discard resampler and VAD state even when no
// frame arrived while the output was active.
func TestPlaybackGenerationResetsInputStateWithoutSuppressedFrames(t *testing.T) {
	run := func(t *testing.T, playback bool) int {
		clock := newFakeClock()
		k := newKit(clock).prime()
		k.aud.frames = make(chan AudioFrame)
		utterances := make(chan []byte, 4)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- k.b.inputLoop(ctx, k.aud, utterances) }()
		for range 14 {
			k.aud.frames <- AudioFrame{Track: "t", PCM: pcm24(1, 3000)}
		}
		k.aud.frames <- AudioFrame{Track: "t"} // barrier: every voiced frame is processed
		if playback {
			k.b.beginPlayback()
			k.b.endPlayback()
			clock.Advance(time.Hour) // holdoff long over; no frame was seen while suppressed
		}
		for range 40 {
			k.aud.frames <- AudioFrame{Track: "t", PCM: pcm24(1, 0)}
		}
		k.aud.frames <- AudioFrame{Track: "t"}
		k.aud.frames <- AudioFrame{Track: "t"}
		cancel()
		<-done
		return len(utterances)
	}
	if n := run(t, false); n != 1 {
		t.Fatalf("control: %d utterances, want 1", n)
	}
	if n := run(t, true); n != 0 {
		t.Fatalf("speech started before playback was completed after it (%d utterances)", n)
	}
}

func TestPlaybackPacingBoundsQueueAfterStalls(t *testing.T) {
	clock := newFakeClock()
	k := newKit(clock).prime()
	ctx := context.Background()
	play := func(n int) {
		for range n {
			if err := k.b.play(ctx, k.aud, make([]byte, playChunkBytes)); err != nil {
				t.Fatal(err)
			}
		}
	}

	k.b.beginPlayback()
	clock.Advance(7 * time.Second) // initial provider delay before the first chunk
	t0 := clock.Now()
	play(10)
	_, _, _, lead, _ := k.aud.snapshot()
	if lead > playAhead {
		t.Fatalf("queue reached %v after an initial delay, limit %v", lead, playAhead)
	}
	if sleeps := clock.Sleeps(); len(sleeps) == 0 {
		t.Fatal("playback never paced itself")
	}

	clock.Advance(30 * time.Second) // midstream stall: the old audio has long finished
	before := len(clock.Sleeps())
	play(40)
	_, _, _, lead, _ = k.aud.snapshot()
	if lead > playAhead {
		t.Fatalf("queue reached %v after a midstream stall, limit %v", lead, playAhead)
	}
	if len(clock.Sleeps()) == before {
		t.Fatal("a burst after a stall was not paced")
	}

	// The first run schedules 10 chunks from t0 (1 s); after the 30 s stall the
	// schedule restarts from the clock, so the end exceeds t0+30s+4s, not t0+5s.
	k.b.mu.Lock()
	end := k.b.playEnd
	k.b.mu.Unlock()
	if want := t0.Add(30*time.Second + 4*time.Second); end.Before(want) {
		t.Fatalf("scheduled end %v precedes %v", end, want)
	}

	k.b.endPlayback()
	k.b.mu.Lock()
	quiet, playEnd := k.b.quietEnd, k.b.playEnd
	k.b.mu.Unlock()
	if quiet.Sub(playEnd) != k.b.Holdoff {
		t.Fatalf("holdoff counts from %v, not from the scheduled end %v", quiet, playEnd)
	}
	clock.Advance(playEnd.Sub(clock.Now()) + k.b.Holdoff - time.Nanosecond)
	if _, suppressed := k.b.inputState(); !suppressed {
		t.Fatal("input resumed before the audio was heard plus the holdoff")
	}
	clock.Advance(time.Nanosecond)
	if _, suppressed := k.b.inputState(); suppressed {
		t.Fatal("input stayed suppressed after the holdoff")
	}
}

// speak must schedule from the clock, not from the provider's pace.
func TestSpeakStallsBetweenChunksStayPaced(t *testing.T) {
	clock := newFakeClock()
	k := newKit(clock).prime()
	stream := newAudioStream(0)
	k.tts.next = func() *fakeAudioStream { return stream }
	done := make(chan error, 1)
	go func() {
		done <- k.b.speak(context.Background(), k.aud, speechJob{uuid: strings.Repeat("u", 64), text: "hi"})
	}()

	plays := func() int { n, _, _, _, _ := k.aud.snapshot(); return n }
	stream.chunks <- pcmChunk(2 * playChunkBytes)
	waitFor(t, "first plays", func() bool { return plays() == 2 })
	clock.Advance(20 * time.Second)
	stream.chunks <- pcmChunk(30 * playChunkBytes)
	waitFor(t, "burst plays", func() bool { return plays() == 32 })
	close(stream.chunks)
	close(stream.errs)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, _, lead, _ := k.aud.snapshot(); lead > playAhead {
		t.Fatalf("queued audio reached %v", lead)
	}
	if !stream.isClosed() {
		t.Fatal("TTS stream was not closed")
	}
	if _, suppressed := k.b.inputState(); !suppressed {
		t.Fatal("input is not held off while the scheduled audio is still sounding")
	}
}

func TestSpeakPreservesBytesAcrossOddChunkBoundaries(t *testing.T) {
	k := newKit(nil).prime()
	var all []byte
	for i := range 9999 { // 19998 bytes: not a multiple of the play chunk
		all = append(all, byte(i), byte(i>>8))
	}
	var chunks []speech.AudioChunk
	want := append([]byte(nil), all...)
	for _, size := range []int{1, 4799, 3, 4797, 5000, 20} {
		chunks, all = append(chunks, speech.AudioChunk{PCM: all[:size], SampleRate: audioRate}), all[size:]
	}
	chunks = append(chunks, speech.AudioChunk{PCM: all, SampleRate: audioRate}) // remainder
	k.tts.next = func() *fakeAudioStream { return staticStream(chunks...) }
	if err := k.b.speak(context.Background(), k.aud, speechJob{uuid: strings.Repeat("u", 64), text: "x"}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	k.aud.mu.Lock()
	for _, p := range k.aud.plays {
		got = append(got, p...)
	}
	k.aud.mu.Unlock()
	if !bytes.Equal(got, want) {
		t.Fatalf("played %d bytes, want the %d supplied bytes unchanged", len(got), len(want))
	}
}

func TestSpeakRejectsMalformedCompletions(t *testing.T) {
	cases := map[string]struct {
		stream  func() *fakeAudioStream
		wantErr string
		plays   bool
	}{
		"terminal odd PCM": {func() *fakeAudioStream { return staticStream(pcmChunk(playChunkBytes + 1)) }, "partial PCM sample", true},
		"odd PCM only":     {func() *fakeAudioStream { return staticStream(pcmChunk(3)) }, "partial PCM sample", false},
		"no chunks":        {func() *fakeAudioStream { return staticStream() }, "without audio", false},
		"empty chunks": {func() *fakeAudioStream {
			return staticStream(speech.AudioChunk{SampleRate: audioRate}, speech.AudioChunk{SampleRate: audioRate})
		}, "without audio", false},
		"wrong rate": {func() *fakeAudioStream {
			return staticStream(speech.AudioChunk{PCM: make([]byte, 4), SampleRate: 16000})
		}, "24 kHz", false},
		"too long": {func() *fakeAudioStream { return staticStream(pcmChunk(maxSpeechAudio), pcmChunk(2)) }, "limit", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			clock := newFakeClock()
			k := newKit(clock).prime()
			k.tts.next = c.stream
			err := k.b.speak(context.Background(), k.aud, speechJob{uuid: strings.Repeat("u", 64), text: "x"})
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if plays, _, _, _, _ := k.aud.snapshot(); (plays > 0) != c.plays {
				t.Fatalf("plays = %d, played-expected %v", plays, c.plays)
			}
			if k.b.playing != 0 {
				t.Fatal("playback suppression leaked after a failed speech")
			}
		})
	}
}

func TestSpeakTimeoutIsBounded(t *testing.T) {
	k := newKit(nil).prime()
	k.b.SpeechTimeout = 50 * time.Millisecond
	stream := newAudioStream(1)
	stream.chunks <- pcmChunk(playChunkBytes) // started, then the provider hangs forever
	k.tts.next = func() *fakeAudioStream { return stream }
	start := time.Now()
	err := k.b.speak(context.Background(), k.aud, speechJob{uuid: strings.Repeat("u", 64), text: "x"})
	if err == nil || !strings.Contains(err.Error(), "did not finish") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("per-speech timeout did not bound the call")
	}
	if k.b.playing != 0 || !stream.isClosed() {
		t.Fatal("timeout leaked playback state or the TTS stream")
	}
}

func TestSpeechFailureClearsPlaybackAndNeverChangesTextOutcome(t *testing.T) {
	k := newKit(newFakeClock())
	stream := newAudioStream(2)
	stream.chunks <- pcmChunk(playChunkBytes)
	k.tts.next = func() *fakeAudioStream { return stream }
	k.asr.events = []speech.TranscriptEvent{final("AI go", "", 0)}
	k.b.Holdoff = time.Millisecond
	k.b.Now, k.b.Sleep = nil, nil // real time for Run; the stream failure is the subject
	k.aud.now = time.Now
	k.start(t)
	sendSpeech(k.aud, "t")
	waitFor(t, "request", func() bool { return len(k.submitted()) == 1 })
	m := k.submitted()[0]
	k.b.Reply(k.b.replyUUID(m), true, "done")
	waitFor(t, "first play", func() bool { p, _, _, _, _ := k.aud.snapshot(); return p == 1 })
	stream.errs <- errors.New("tts exploded") // after audio was queued
	waitFor(t, "speech failure report", func() bool { return k.obs.count("voice.speech.failed") == 1 })
	waitFor(t, "playback clear", func() bool { _, c, _, _, _ := k.aud.snapshot(); return c == 1 })
	if len(k.submitted()) != 1 {
		t.Fatal("a speech failure changed what was submitted")
	}
	// The bridge keeps serving: the failure is reported, not replayed.
	time.Sleep(20 * time.Millisecond)
	if len(k.tts.reqs) != 1 {
		t.Fatalf("TTS requested %d times, want exactly one (no implicit replay)", len(k.tts.reqs))
	}
}

func TestRunShutdownClearsPendingPlayback(t *testing.T) {
	clock := newFakeClock() // the clock never advances: scheduled audio is still sounding at shutdown
	k := newKit(clock)
	stream := newAudioStream(1)
	stream.chunks <- pcmChunk(playChunkBytes) // one chunk played, then the provider stalls
	k.tts.next = func() *fakeAudioStream { return stream }
	k.asr.events = []speech.TranscriptEvent{final("AI go", "", 0)}
	r := k.start(t)
	sendSpeech(k.aud, "t")
	waitFor(t, "request", func() bool { return len(k.submitted()) == 1 })
	k.b.Reply(k.b.replyUUID(k.submitted()[0]), true, "reply")
	waitFor(t, "first play", func() bool { p, _, _, _, _ := k.aud.snapshot(); return p == 1 })

	r.stop()
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	_, clears, closes, _, events := k.aud.snapshot()
	if clears != 1 || closes != 1 {
		t.Fatalf("clears=%d closes=%d, events %v", clears, closes, events)
	}
	if events[len(events)-2] != "clear" || events[len(events)-1] != "close" {
		t.Fatalf("playback must be cleared before the session closes: %v", events)
	}
	if !stream.isClosed() {
		t.Fatal("TTS stream leaked past shutdown")
	}
	if k.b.playing != 0 {
		t.Fatal("playback suppression leaked past shutdown")
	}
}

func TestRunShutdownWithIdleBridgeDoesNotClear(t *testing.T) {
	k := newKit(newFakeClock())
	r := k.start(t)
	r.stop()
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if _, clears, closes, _, _ := k.aud.snapshot(); clears != 0 || closes != 1 {
		t.Fatalf("clears=%d closes=%d", clears, closes)
	}
}

func TestRunShutdownCleanupIsBounded(t *testing.T) {
	clock := newFakeClock()
	k := newKit(clock)
	k.aud.clearBlock = true // the session never answers a clear
	stream := newAudioStream(1)
	stream.chunks <- pcmChunk(playChunkBytes)
	k.tts.next = func() *fakeAudioStream { return stream }
	k.asr.events = []speech.TranscriptEvent{final("AI go", "", 0)}
	r := k.start(t)
	sendSpeech(k.aud, "t")
	waitFor(t, "request", func() bool { return len(k.submitted()) == 1 })
	k.b.Reply(k.b.replyUUID(k.submitted()[0]), true, "reply")
	waitFor(t, "first play", func() bool { p, _, _, _, _ := k.aud.snapshot(); return p == 1 })

	start := time.Now()
	r.stop()
	err := r.wait(t)
	if elapsed := time.Since(start); elapsed > clearTimeout+2*time.Second {
		t.Fatalf("shutdown took %v with an unresponsive clear", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if _, clears, closes, _, _ := k.aud.snapshot(); clears != 1 || closes != 1 {
		t.Fatalf("clears=%d closes=%d: the failed clear must be attempted once and the session still closed", clears, closes)
	}
	if k.obs.count("voice.playback.clear_failed") != 1 {
		t.Fatal("the failed clear was not reported")
	}
}

func TestBridgeIsSingleUseAndValidates(t *testing.T) {
	k := newKit(nil)
	k.start(t)
	bad := newKit(nil)
	bad.b.Config.AcceptUnattributedAudio = false
	if err := bad.b.Run(context.Background(), nil); err == nil {
		t.Fatal("an unconfirmed unattributed-audio configuration was accepted")
	}
	if err := k.b.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "single use") {
		t.Fatalf("second Run = %v", err)
	}
}
