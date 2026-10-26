// Package speech defines caller-owned streaming speech interfaces. It contains
// no provider implementation, model loop, transport or configuration discovery.
package speech

import "context"

// ASRConfig describes raw signed little-endian PCM16 mono input.
type ASRConfig struct {
	SessionID, Locale, Encoding string
	SampleRate                  int
	Hotwords                    []string
}

// TranscriptEvent.Final marks a definite utterance, not provider completion.
// The caller must drain Events and Errors until closed to observe completion.
type TranscriptEvent struct {
	Text, SegmentID string
	Final           bool
	StartMS, EndMS  int64
}

type ASRProvider interface {
	Open(context.Context, ASRConfig) (ASRStream, error)
}

// ASRStream owns one input session. EndUtterance explicitly completes input;
// Close instead cancels immediately and never sends an input-completion frame.
// Concurrent writes are serialized. Callers should drain both output channels.
type ASRStream interface {
	WriteAudio(context.Context, []byte) error
	EndUtterance(context.Context) error
	Events() <-chan TranscriptEvent
	Errors() <-chan error
	Close() error
}

type TTSRequest struct {
	SessionID, TurnID, Voice, Text string
	SampleRate                     int
	Encoding                       string
}

// AudioChunk is raw signed little-endian PCM16 mono, never a file container.
type AudioChunk struct {
	PCM        []byte
	SampleRate int
}

type TTSProvider interface {
	Synthesize(context.Context, TTSRequest) (AudioStream, error)
}

// AudioStream emits bounded chunks before synthesis completes. Close is
// idempotent immediate cancellation; the caller owns playback and interruption.
// Errors closes after the terminal error (if any); local cancellation is quiet.
type AudioStream interface {
	Chunks() <-chan AudioChunk
	Errors() <-chan error
	Close() error
}
