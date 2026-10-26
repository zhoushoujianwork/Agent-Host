package example

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

type asrProviderFunc func(context.Context, speech.ASRConfig) (speech.ASRStream, error)

func (f asrProviderFunc) Open(ctx context.Context, cfg speech.ASRConfig) (speech.ASRStream, error) {
	return f(ctx, cfg)
}

type ttsProviderFunc func(context.Context, speech.TTSRequest) (speech.AudioStream, error)

func (f ttsProviderFunc) Synthesize(ctx context.Context, req speech.TTSRequest) (speech.AudioStream, error) {
	return f(ctx, req)
}

type recognition struct {
	events           chan speech.TranscriptEvent
	errs             chan error
	writeErr, endErr error
	end              func()
}

func (s *recognition) Events() <-chan speech.TranscriptEvent    { return s.events }
func (s *recognition) Errors() <-chan error                     { return s.errs }
func (s *recognition) WriteAudio(context.Context, []byte) error { return s.writeErr }
func (s *recognition) EndUtterance(context.Context) error {
	if s.end != nil {
		s.end()
	}
	return s.endErr
}
func (s *recognition) Close() error { return nil }
func recognized(events []speech.TranscriptEvent, err error) *recognition {
	s := &recognition{events: make(chan speech.TranscriptEvent, len(events)), errs: make(chan error, 1)}
	for _, event := range events {
		s.events <- event
	}
	close(s.events)
	if err != nil {
		s.errs <- err
	}
	close(s.errs)
	return s
}

type playback struct {
	chunks chan speech.AudioChunk
	errs   chan error
}

func (s *playback) Chunks() <-chan speech.AudioChunk { return s.chunks }
func (s *playback) Errors() <-chan error             { return s.errs }
func (s *playback) Close() error                     { return nil }
func synthesized(chunks []speech.AudioChunk, err error) *playback {
	s := &playback{chunks: make(chan speech.AudioChunk, len(chunks)), errs: make(chan error, 1)}
	for _, chunk := range chunks {
		s.chunks <- chunk
	}
	close(s.chunks)
	if err != nil {
		s.errs <- err
	}
	close(s.errs)
	return s
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestTurnAggregatesDefiniteSegmentsAndPlaysChunks(t *testing.T) {
	events := []speech.TranscriptEvent{
		{Text: "ignored partial", SegmentID: "a"},
		{Text: "hello", SegmentID: "a", Final: true},
		{Text: "world", SegmentID: "b", Final: true},
		{Text: "Hello", SegmentID: "a", Final: true},
		{Text: "old timed", StartMS: 100, EndMS: 200, Final: true},
		{Text: "timed", StartMS: 100, EndMS: 250, Final: true},
		{Text: "tail", Final: true}, {Text: "tail", Final: true},
		{Text: "   ", Final: true}, {Text: "ignored later partial"},
	}
	var gotText string
	var gotPCM []byte
	asr := asrProviderFunc(func(ctx context.Context, cfg speech.ASRConfig) (speech.ASRStream, error) {
		if cfg.SessionID != "session" || cfg.Encoding != "pcm_s16le" || cfg.SampleRate != 16000 {
			t.Error(cfg)
		}
		return recognized(events, nil), nil
	})
	tts := ttsProviderFunc(func(ctx context.Context, req speech.TTSRequest) (speech.AudioStream, error) {
		if req.Text != "agent answer" || req.SessionID != "session" || req.TurnID != "turn" || req.Voice != "voice" || req.SampleRate != 24000 || req.Encoding != "pcm_s16le" {
			t.Error(req)
		}
		return synthesized([]speech.AudioChunk{{PCM: []byte{1, 2}, SampleRate: 24000}, {PCM: []byte{3, 4}, SampleRate: 24000}}, nil), nil
	})
	err := Turn(testContext(t), asr, tts, []byte{0, 0}, "session", "turn", "voice",
		func(ctx context.Context, text string) (string, error) { gotText = text; return "agent answer", nil },
		func(ctx context.Context, chunk speech.AudioChunk) error {
			if chunk.SampleRate != 24000 {
				t.Error(chunk)
			}
			gotPCM = append(gotPCM, chunk.PCM...)
			return nil
		})
	if err != nil || gotText != "Hello world timed tail" || !bytes.Equal(gotPCM, []byte{1, 2, 3, 4}) {
		t.Fatalf("text=%q pcm=%v err=%v", gotText, gotPCM, err)
	}
}

func TestTurnZeroStartTimestampRevisions(t *testing.T) {
	asr := asrProviderFunc(func(context.Context, speech.ASRConfig) (speech.ASRStream, error) {
		return recognized([]speech.TranscriptEvent{{Text: "old", StartMS: 0, EndMS: 20, Final: true}, {Text: "revised", StartMS: 0, EndMS: 30, Final: true}}, nil), nil
	})
	tts := ttsProviderFunc(func(context.Context, speech.TTSRequest) (speech.AudioStream, error) {
		return synthesized(nil, nil), nil
	})
	err := Turn(testContext(t), asr, tts, nil, "session", "turn", "voice", func(ctx context.Context, text string) (string, error) {
		if text != "revised" {
			t.Errorf("got transcript %q", text)
		}
		return "answer", nil
	}, func(context.Context, speech.AudioChunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}

func TestTurnRejectsAbsentDefiniteTranscript(t *testing.T) {
	for _, events := range [][]speech.TranscriptEvent{nil, {{Text: "partial"}}, {{Text: "   ", Final: true}}} {
		agentCalls, ttsCalls := 0, 0
		asr := asrProviderFunc(func(context.Context, speech.ASRConfig) (speech.ASRStream, error) { return recognized(events, nil), nil })
		tts := ttsProviderFunc(func(context.Context, speech.TTSRequest) (speech.AudioStream, error) {
			ttsCalls++
			return synthesized(nil, nil), nil
		})
		err := Turn(testContext(t), asr, tts, nil, "session", "turn", "voice", func(context.Context, string) (string, error) { agentCalls++; return "answer", nil }, func(context.Context, speech.AudioChunk) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "definite transcript") || agentCalls != 0 || ttsCalls != 0 {
			t.Fatalf("agent=%d tts=%d err=%v", agentCalls, ttsCalls, err)
		}
	}
}

func TestTurnPropagatesErrors(t *testing.T) {
	for _, stage := range []string{"open", "write", "end", "asr", "agent", "synthesize", "audio", "play"} {
		t.Run(stage, func(t *testing.T) {
			want := errors.New(stage + " failure")
			agentCalls, ttsCalls := 0, 0
			asr := asrProviderFunc(func(context.Context, speech.ASRConfig) (speech.ASRStream, error) {
				if stage == "open" {
					return nil, want
				}
				var remote error
				if stage == "asr" {
					remote = want
				}
				s := recognized([]speech.TranscriptEvent{{Text: "final", Final: true}}, remote)
				if stage == "write" {
					s.writeErr = want
				}
				if stage == "end" {
					s.endErr = want
				}
				return s, nil
			})
			tts := ttsProviderFunc(func(context.Context, speech.TTSRequest) (speech.AudioStream, error) {
				ttsCalls++
				if stage == "synthesize" {
					return nil, want
				}
				var remote error
				if stage == "audio" {
					remote = want
				}
				return synthesized([]speech.AudioChunk{{PCM: []byte{0, 0}, SampleRate: 24000}}, remote), nil
			})
			err := Turn(testContext(t), asr, tts, nil, "session", "turn", "voice", func(context.Context, string) (string, error) {
				agentCalls++
				if stage == "agent" {
					return "", want
				}
				return "answer", nil
			}, func(context.Context, speech.AudioChunk) error {
				if stage == "play" {
					return want
				}
				return nil
			})
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if (stage == "open" || stage == "write" || stage == "end" || stage == "asr") && (agentCalls != 0 || ttsCalls != 0) {
				t.Fatal("advanced after ASR failure")
			}
			if stage == "agent" && ttsCalls != 0 {
				t.Fatal("advanced after Agent failure")
			}
		})
	}
}

func TestTurnPropagatesCancellation(t *testing.T) {
	for _, stage := range []string{"recognition", "agent", "play"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			agentCalls, ttsCalls := 0, 0
			asr := asrProviderFunc(func(context.Context, speech.ASRConfig) (speech.ASRStream, error) {
				s := recognized([]speech.TranscriptEvent{{Text: "final", Final: true}}, nil)
				if stage == "recognition" {
					s.end = cancel
				}
				return s, nil
			})
			tts := ttsProviderFunc(func(context.Context, speech.TTSRequest) (speech.AudioStream, error) {
				ttsCalls++
				return synthesized([]speech.AudioChunk{{PCM: []byte{0, 0}, SampleRate: 24000}}, nil), nil
			})
			err := Turn(ctx, asr, tts, nil, "session", "turn", "voice", func(context.Context, string) (string, error) {
				agentCalls++
				if stage == "agent" {
					cancel()
				}
				return "answer", nil
			}, func(context.Context, speech.AudioChunk) error { cancel(); return nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			if stage == "recognition" && agentCalls != 0 {
				t.Fatal("Agent invoked after cancellation")
			}
			if stage != "play" && ttsCalls != 0 {
				t.Fatal("TTS invoked after cancellation")
			}
		})
	}
}
