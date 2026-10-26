// Package example demonstrates a caller-owned ASR -> Agent -> TTS turn.
package example

import (
	"context"
	"errors"
	"strings"

	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

// Turn accepts a complete PCM16/16kHz mono utterance. The application supplies
// its own Agent/model reply and PCM16/24kHz mono playback callbacks. For live
// input the application can replace the input goroutine with its audio source.
// This is an educational turn policy, not a core orchestration API.
func Turn(ctx context.Context, asr speech.ASRProvider, tts speech.TTSProvider,
	input []byte, session, turn, voice string,
	reply func(context.Context, string) (string, error),
	play func(context.Context, speech.AudioChunk) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	recognition, err := asr.Open(ctx, speech.ASRConfig{SessionID: session, Encoding: "pcm_s16le", SampleRate: 16000})
	if err != nil {
		return err
	}
	defer recognition.Close()
	inputDone := make(chan error, 1)
	go func() {
		err := recognition.WriteAudio(ctx, input)
		if err == nil {
			err = recognition.EndUtterance(ctx)
		}
		inputDone <- err
	}()
	// Keep draining while input is being written: output backpressure must not
	// prevent the application from submitting the explicit input completion.
	events, errs := recognition.Events(), recognition.Errors()
	var finals []speech.TranscriptEvent
	for events != nil || errs != nil || inputDone != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if !event.Final || strings.TrimSpace(event.Text) == "" {
				continue
			}
			// Keep first-seen order and replace revisions of identified segments.
			index := -1
			for i, previous := range finals {
				same := false
				switch {
				case event.SegmentID != "":
					same = previous.SegmentID == event.SegmentID
				case event.StartMS != 0 || event.EndMS != 0:
					same = previous.SegmentID == "" && (previous.StartMS != 0 || previous.EndMS != 0) && previous.StartMS == event.StartMS
				default:
					same = previous.SegmentID == "" && previous.StartMS == 0 && previous.EndMS == 0 && previous.Text == event.Text
				}
				if same {
					index = i
					break
				}
			}
			if index >= 0 {
				finals[index] = event
			} else {
				finals = append(finals, event)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return err
			}
		case err := <-inputDone:
			inputDone = nil
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(finals) == 0 {
		return errors.New("example: no nonempty definite transcript")
	}
	segments := make([]string, len(finals))
	for i, event := range finals {
		segments[i] = event.Text
	}
	// Agent/model execution is entirely caller-owned and happens between ASR/TTS.
	answer, err := reply(ctx, strings.Join(segments, " "))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	audio, err := tts.Synthesize(ctx, speech.TTSRequest{SessionID: session, TurnID: turn, Voice: voice, Text: answer, Encoding: "pcm_s16le", SampleRate: 24000})
	if err != nil {
		return err
	}
	defer audio.Close()
	chunks, audioErrors := audio.Chunks(), audio.Errors()
	for chunks != nil || audioErrors != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if err := play(ctx, chunk); err != nil {
				return err
			}
		case err, ok := <-audioErrors:
			if !ok {
				audioErrors = nil
				continue
			}
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}
