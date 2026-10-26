package doubao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/coder/websocket"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

type ASRProvider struct{ Config Config }

var _ speech.ASRProvider = ASRProvider{}

func (p ASRProvider) Open(ctx context.Context, cfg speech.ASRConfig) (speech.ASRStream, error) {
	if err := p.Config.validate(false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.SessionID) == "" || len(cfg.SessionID) > 256 {
		return nil, errors.New("doubao: ASR session ID is required (max 256 bytes)")
	}
	if cfg.Encoding != "pcm_s16le" || cfg.SampleRate != 16000 {
		return nil, errors.New("doubao: ASR requires pcm_s16le at 16000 Hz mono")
	}
	if len(cfg.Hotwords) > 1024 {
		return nil, errors.New("doubao: too many ASR hotwords")
	}
	for _, word := range cfg.Hotwords {
		if len(word) > 1024 {
			return nil, errors.New("doubao: ASR hotword exceeds limit")
		}
	}
	if len(cfg.Locale) > 64 {
		return nil, errors.New("doubao: ASR locale exceeds limit")
	}
	model := p.Config.ASRModel
	if model == "" {
		model = "bigmodel"
	}
	options := map[string]any{"reqid": cfg.SessionID, "model_name": model, "sequence": 1, "show_utterances": true, "result_type": "single", "enable_punc": true, "enable_itn": true}
	if len(cfg.Hotwords) > 0 {
		words := make([]map[string]string, 0, len(cfg.Hotwords))
		for _, word := range cfg.Hotwords {
			words = append(words, map[string]string{"word": word})
		}
		b, _ := json.Marshal(map[string]any{"hotwords": words})
		options["context"] = string(b)
	}
	request := map[string]any{
		"user":    map[string]any{"uid": cfg.SessionID},
		"audio":   map[string]any{"format": "pcm", "rate": cfg.SampleRate, "bits": 16, "channel": 1, "language": cfg.Locale},
		"request": options,
	}
	conn, err := p.Config.start(ctx, false, request)
	if err != nil {
		return nil, err
	}
	s := &asrStream{stream: newStream(ctx, conn), events: make(chan speech.TranscriptEvent, 16), writeGate: make(chan struct{}, 1)}
	go s.receive()
	return s, nil
}

type asrStream struct {
	*stream
	events    chan speech.TranscriptEvent
	writeGate chan struct{}
	ended     bool // protected by writeGate
}

func (s *asrStream) Events() <-chan speech.TranscriptEvent { return s.events }

// lockWrite makes both queueing and socket I/O cancelable by either context.
func (s *asrStream) lockWrite(ctx context.Context) (context.Context, func(), error) {
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	cleanup := func() { stop(); cancel() }
	select {
	case s.writeGate <- struct{}{}:
		if call.Err() != nil || s.ctx.Err() != nil {
			err := call.Err()
			if err == nil {
				err = context.Canceled
			}
			<-s.writeGate
			cleanup()
			return nil, nil, err
		}
		return call, func() { <-s.writeGate; cleanup() }, nil
	case <-call.Done():
		cleanup()
		return nil, nil, call.Err()
	}
}
func (s *asrStream) WriteAudio(ctx context.Context, pcm []byte) error {
	if len(pcm)%2 != 0 {
		return errors.New("doubao: ASR PCM must be sample aligned")
	}
	call, unlock, err := s.lockWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if s.ended {
		return errors.New("doubao: ASR input already completed")
	}
	for len(pcm) > 0 {
		n := min(len(pcm), maxAudioChunk)
		payload, err := compress(pcm[:n])
		if err != nil {
			return errors.New("doubao: audio compression failed")
		}
		if err = s.conn.Write(call, websocket.MessageBinary, encode(frame{kind: audioRequest, compression: 1, payload: payload})); err != nil {
			return safeError(call, "audio write failed")
		}
		pcm = pcm[n:]
	}
	return nil
}
func (s *asrStream) EndUtterance(ctx context.Context) error {
	call, unlock, err := s.lockWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if s.ended {
		return nil
	}
	payload, err := compress(nil)
	if err != nil {
		return errors.New("doubao: audio compression failed")
	}
	if err = s.conn.Write(call, websocket.MessageBinary, encode(frame{kind: audioRequest, flags: 2, compression: 1, payload: payload})); err != nil {
		return safeError(call, "input completion failed")
	}
	s.ended = true
	return nil
}

func (s *asrStream) receive() {
	defer s.finish()
	defer close(s.events)
	for {
		f, err := s.read()
		if err != nil {
			s.fail(err)
			return
		}
		if f.kind != fullResponse || f.serialization != 1 {
			s.fail(errors.New("doubao: unexpected ASR response"))
			return
		}
		var response struct {
			Code   int `json:"code"`
			Result *struct {
				Text       string `json:"text"`
				Utterances []struct {
					ID       string `json:"id"`
					Text     string `json:"text"`
					Start    int64  `json:"start_time"`
					End      int64  `json:"end_time"`
					Definite bool   `json:"definite"`
				} `json:"utterances"`
			} `json:"result"`
		}
		if json.Unmarshal(f.payload, &response) != nil {
			s.fail(errors.New("doubao: malformed ASR JSON"))
			return
		}
		if response.Code != 0 && response.Code != 1000 && response.Code != 20000000 {
			s.fail(fmt.Errorf("doubao: ASR provider code %d", response.Code))
			return
		}
		if response.Result == nil {
			s.fail(errors.New("doubao: ASR response is missing result"))
			return
		}
		for _, u := range response.Result.Utterances {
			event := speech.TranscriptEvent{Text: u.Text, SegmentID: u.ID, Final: u.Definite, StartMS: u.Start, EndMS: u.End}
			select {
			case s.events <- event:
			case <-s.ctx.Done():
				return
			}
		}
		if len(response.Result.Utterances) == 0 && response.Result.Text != "" {
			select {
			case s.events <- speech.TranscriptEvent{Text: response.Result.Text, Final: f.flags == 2 || f.flags == 3}:
			case <-s.ctx.Done():
				return
			}
		}
		if f.flags == 2 || f.flags == 3 {
			return
		}
	}
}
