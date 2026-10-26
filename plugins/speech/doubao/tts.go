package doubao

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

type TTSProvider struct{ Config Config }

var _ speech.TTSProvider = TTSProvider{}

func (p TTSProvider) Synthesize(ctx context.Context, req speech.TTSRequest) (speech.AudioStream, error) {
	if err := p.Config.validate(true); err != nil {
		return nil, err
	}
	if req.Encoding != "pcm_s16le" || req.SampleRate != 24000 {
		return nil, errors.New("doubao: TTS requires pcm_s16le at 24000 Hz mono")
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.TurnID) == "" || len(req.SessionID) > 256 || len(req.TurnID) > 256 {
		return nil, errors.New("doubao: TTS session and turn IDs are required (max 256 bytes)")
	}
	if strings.TrimSpace(req.Voice) == "" || len(req.Voice) > 256 {
		return nil, errors.New("doubao: TTS voice is required (max 256 bytes)")
	}
	if strings.TrimSpace(req.Text) == "" || len(req.Text) > 10000 || !utf8.ValidString(req.Text) {
		return nil, errors.New("doubao: TTS text must be valid UTF-8, nonempty and at most 10000 bytes")
	}
	request := map[string]any{
		"app":     map[string]any{"appid": p.Config.AppID, "token": p.Config.AccessToken, "cluster": p.Config.TTSCluster},
		"user":    map[string]any{"uid": req.SessionID},
		"audio":   map[string]any{"voice_type": req.Voice, "encoding": "pcm", "rate": 24000},
		"request": map[string]any{"reqid": req.TurnID, "text": req.Text, "operation": "submit"},
	}
	conn, err := p.Config.start(ctx, true, request)
	if err != nil {
		return nil, err
	}
	s := &ttsStream{stream: newStream(ctx, conn), chunks: make(chan speech.AudioChunk, 8)}
	go s.receive()
	return s, nil
}

type ttsStream struct {
	*stream
	chunks chan speech.AudioChunk
}

func (s *ttsStream) Chunks() <-chan speech.AudioChunk { return s.chunks }
func (s *ttsStream) receive() {
	defer s.finish()
	defer close(s.chunks)
	total := 0
	for {
		f, err := s.read()
		if err != nil {
			s.fail(err)
			return
		}
		if f.kind != audioResponse || f.serialization != 0 {
			s.fail(errors.New("doubao: unexpected TTS response"))
			return
		}
		if f.flags == 0 {
			continue
		} // Header-only legacy acknowledgement.
		if len(f.payload)%2 != 0 {
			s.fail(errors.New("doubao: TTS PCM is not sample aligned"))
			return
		}
		total += len(f.payload)
		if total > maxTTSOutput {
			s.fail(errors.New("doubao: TTS output exceeds 8 MiB"))
			return
		}
		if len(f.payload) > 0 {
			select {
			case s.chunks <- speech.AudioChunk{PCM: f.payload, SampleRate: 24000}:
			case <-s.ctx.Done():
				return
			}
		}
		if f.final() {
			if total == 0 {
				s.fail(errors.New("doubao: provider completed without audio"))
			}
			return
		}
	}
}
