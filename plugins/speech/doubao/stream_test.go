package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
)

func fixture(t *testing.T, serve func(context.Context, *websocket.Conn, *http.Request)) Config {
	t.Helper()
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(maxMessage)
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		serve(ctx, conn, r)
	}))
	t.Cleanup(server.Close)
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	return Config{ASREndpoint: endpoint + "/api/v3/sauc/bigmodel", TTSEndpoint: endpoint + "/api/v1/tts/ws_binary", APIKeyV2: "test-key", AppID: "test-app", AccessToken: "test-token", ResourceID: "asr-resource", TTSCluster: "test-cluster"}
}
func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func asrConfig() speech.ASRConfig {
	return speech.ASRConfig{SessionID: "session", Encoding: "pcm_s16le", SampleRate: 16000, Locale: "zh-CN", Hotwords: []string{"TRIZ"}}
}
func ttsRequest() speech.TTSRequest {
	return speech.TTSRequest{SessionID: "session", TurnID: "turn", Voice: "voice", Text: "hello", Encoding: "pcm_s16le", SampleRate: 24000}
}
func readFrame(t *testing.T, ctx context.Context, c *websocket.Conn) frame {
	t.Helper()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Error(err)
		return frame{}
	}
	if typ != websocket.MessageBinary {
		t.Error("expected binary frame")
	}
	f, err := decode(b)
	if err != nil {
		t.Error(err)
		return frame{}
	}
	f.payload, err = decompress(f.payload, f.compression)
	if err != nil {
		t.Error(err)
	}
	return f
}
func send(t *testing.T, ctx context.Context, c *websocket.Conn, f frame) {
	t.Helper()
	if err := c.Write(ctx, websocket.MessageBinary, encode(f)); err != nil {
		t.Error(err)
	}
}
func take[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("unexpected channel close")
		}
		return v
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var zero T
	return zero
}
func drain[T any](t *testing.T, ctx context.Context, ch <-chan T) []T {
	t.Helper()
	var out []T
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, v)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return out
		}
	}
}
func checkRequest(t *testing.T, f frame) map[string]any {
	t.Helper()
	if f.kind != fullRequest || f.serialization != 1 || f.compression != 1 {
		t.Errorf("bad request framing: %+v", f)
	}
	var req map[string]any
	if err := json.Unmarshal(f.payload, &req); err != nil {
		t.Error(err)
	}
	return req
}

func TestASRWireAndUtteranceVersusProviderFinal(t *testing.T) {
	final := make(chan struct{})
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" || r.Header.Get("X-Api-Resource-Id") != "asr-resource" || r.Header.Get("X-Api-Connect-Id") == "" {
			t.Error("missing ASR authentication")
		}
		req := checkRequest(t, readFrame(t, ctx, c))
		audio := req["audio"].(map[string]any)
		if audio["format"] != "pcm" || audio["rate"] != float64(16000) || audio["bits"] != float64(16) || audio["channel"] != float64(1) {
			t.Error(audio)
		}
		opts := req["request"].(map[string]any)
		if opts["model_name"] != "bigmodel" || !strings.Contains(opts["context"].(string), "TRIZ") {
			t.Error(opts)
		}
		for _, want := range []int{12800, 2} {
			f := readFrame(t, ctx, c)
			if f.kind != audioRequest || f.flags != 0 || f.compression != 1 || len(f.payload) != want {
				t.Errorf("audio %+v size=%d", f, len(f.payload))
			}
		}
		f := readFrame(t, ctx, c)
		if f.kind != audioRequest || f.flags != 2 || len(f.payload) != 0 {
			t.Error("missing explicit input completion")
		}
		send(t, ctx, c, frame{kind: fullResponse, serialization: 1, payload: []byte(`{"result":{"utterances":[{"id":"1","text":"hel","definite":false,"start_time":10,"end_time":20}]}}`)})
		send(t, ctx, c, frame{kind: fullResponse, serialization: 1, payload: []byte(`{"result":{"utterances":[{"id":"1","text":"hello","definite":true,"start_time":10,"end_time":30}]}}`)})
		select {
		case <-final:
		case <-ctx.Done():
			return
		}
		send(t, ctx, c, frame{kind: fullResponse, flags: 3, sequence: -1, serialization: 1, payload: []byte(`{"result":{}}`)})
	})
	ctx := testContext(t)
	s, err := (ASRProvider{cfg}).Open(ctx, asrConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.WriteAudio(ctx, make([]byte, 12802)); err != nil {
		t.Fatal(err)
	}
	if err = s.EndUtterance(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EndUtterance(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.WriteAudio(ctx, []byte{0, 0}); err == nil {
		t.Fatal("write after completion accepted")
	}
	a, b := take(t, ctx, s.Events()), take(t, ctx, s.Events())
	if a.Final || a.Text != "hel" || !b.Final || b.Text != "hello" || b.SegmentID != "1" || b.StartMS != 10 || b.EndMS != 30 {
		t.Fatalf("events %+v %+v", a, b)
	}
	select {
	case <-s.Events():
		t.Fatal("definite utterance terminated provider stream")
	default:
	}
	close(final)
	if got := drain(t, ctx, s.Events()); len(got) != 0 {
		t.Fatal(got)
	}
	if errs := drain(t, ctx, s.Errors()); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestTTSStreamsBeforeFinal(t *testing.T) {
	final := make(chan struct{})
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer;test-token" || r.Header.Get("X-Api-Key") != "" {
			t.Error("wrong legacy TTS headers")
		}
		req := checkRequest(t, readFrame(t, ctx, c))
		audio := req["audio"].(map[string]any)
		app := req["app"].(map[string]any)
		options := req["request"].(map[string]any)
		if audio["encoding"] != "pcm" || audio["rate"] != float64(24000) || audio["voice_type"] != "voice" || app["token"] != "test-token" || options["operation"] != "submit" || options["text"] != "hello" {
			t.Error(req)
		}
		// Literal ACK bytes deliberately do not share the encoder under test.
		if err := c.Write(ctx, websocket.MessageBinary, []byte{0x11, 0xb0, 0, 0}); err != nil {
			t.Error(err)
			return
		}
		payload, _ := compress([]byte{1, 2, 3, 4})
		send(t, ctx, c, frame{kind: audioResponse, flags: 1, sequence: 1, compression: 1, payload: payload})
		select {
		case <-final:
		case <-ctx.Done():
			return
		}
		// Legacy flags2 final: signed -2 sequence, length 2, PCM bytes 5 and 6.
		if err := c.Write(ctx, websocket.MessageBinary, []byte{0x11, 0xb2, 0, 0, 0xff, 0xff, 0xff, 0xfe, 0, 0, 0, 2, 5, 6}); err != nil {
			t.Error(err)
		}
	})
	ctx := testContext(t)
	s, err := (TTSProvider{cfg}).Synthesize(ctx, ttsRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := take(t, ctx, s.Chunks())
	if !bytes.Equal(first.PCM, []byte{1, 2, 3, 4}) || first.SampleRate != 24000 {
		t.Fatal(first)
	}
	close(final) // Receipt of the first chunk is required before the final frame exists.
	chunks := drain(t, ctx, s.Chunks())
	if len(chunks) != 1 || !bytes.Equal(chunks[0].PCM, []byte{5, 6}) {
		t.Fatal(chunks)
	}
	if errs := drain(t, ctx, s.Errors()); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestCloseDoesNotCompleteASRInput(t *testing.T) {
	observed := make(chan bool, 1)
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		checkRequest(t, readFrame(t, ctx, c))
		_, _, err := c.Read(ctx)
		observed <- err != nil
	})
	ctx := testContext(t)
	s, err := (ASRProvider{cfg}).Open(ctx, asrConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if !take(t, ctx, observed) {
		t.Fatal("Close sent an implicit input frame")
	}
	if errs := drain(t, ctx, s.Errors()); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestCancellationUnderBackpressure(t *testing.T) {
	for _, asr := range []bool{true, false} {
		for _, parent := range []bool{true, false} {
			name := "tts"
			if asr {
				name = "asr"
			}
			if parent {
				name += "/parent"
			} else {
				name += "/close"
			}
			t.Run(name, func(t *testing.T) {
				sent := make(chan struct{})
				cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
					readFrame(t, ctx, c)
					capacity := 8
					if asr {
						capacity = 16
					}
					for i := 0; i < 64; i++ {
						f := frame{kind: audioResponse, flags: 1, sequence: int32(i + 1), payload: []byte{0, 0}}
						if asr {
							f = frame{kind: fullResponse, serialization: 1, payload: []byte(`{"result":{"text":"partial"}}`)}
						}
						if err := c.Write(ctx, websocket.MessageBinary, encode(f)); err != nil {
							return
						}
						if i == capacity {
							close(sent)
						}
					}
					_, _, _ = c.Read(ctx)
				})
				ctx := testContext(t)
				owned, cancel := context.WithCancel(ctx)
				defer cancel()
				var closeStream func() error
				var errs <-chan error
				var events <-chan speech.TranscriptEvent
				var chunks <-chan speech.AudioChunk
				if asr {
					s, err := (ASRProvider{cfg}).Open(owned, asrConfig())
					if err != nil {
						t.Fatal(err)
					}
					closeStream = s.Close
					errs = s.Errors()
					events = s.Events()
				} else {
					s, err := (TTSProvider{cfg}).Synthesize(owned, ttsRequest())
					if err != nil {
						t.Fatal(err)
					}
					closeStream = s.Close
					errs = s.Errors()
					chunks = s.Chunks()
				}
				select {
				case <-sent:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if parent {
					cancel()
				}
				closed := make(chan struct{})
				go func() { _ = closeStream(); _ = closeStream(); close(closed) }()
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("Close blocked on output backpressure")
				}
				if asr {
					drain(t, ctx, events)
				} else {
					drain(t, ctx, chunks)
				}
				if got := drain(t, ctx, errs); len(got) != 0 {
					t.Fatal(got)
				}
			})
		}
	}
}

func TestRemoteFailuresAreTerminalAndSanitized(t *testing.T) {
	for _, asr := range []bool{true, false} {
		for _, kind := range []string{"premature-normal-close", "provider", "malformed", "gzip", "oversized", "text", "wrong-type", "bad-json", "unaligned", "bomb"} {
			name := "tts/"
			if asr {
				name = "asr/"
			}
			t.Run(name+kind, func(t *testing.T) {
				cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
					readFrame(t, ctx, c)
					var b []byte
					switch kind {
					case "premature-normal-close":
						_ = c.Close(websocket.StatusNormalClosure, "test-token https://secret.invalid/?signed=private")
						return
					case "provider":
						b = encode(frame{kind: errorResponse, code: 45000000, payload: []byte("test-token private remote credentials")})
					case "malformed":
						b = []byte{0x11}
					case "gzip":
						b = encode(frame{kind: fullResponse, serialization: 1, compression: 1, payload: []byte("test-token")})
					case "oversized":
						b = make([]byte, maxMessage+1)
					case "text":
						_ = c.Write(ctx, websocket.MessageText, []byte("test-token"))
						return
					case "wrong-type":
						b = encode(frame{kind: fullRequest})
					case "bad-json":
						b = encode(frame{kind: fullResponse, serialization: 1, payload: []byte("test-token")})
					case "unaligned":
						b = encode(frame{kind: audioResponse, flags: 3, sequence: -1, payload: []byte{1}})
					case "bomb":
						payload, _ := compress(make([]byte, maxMessage+1))
						b = encode(frame{kind: fullResponse, serialization: 1, compression: 1, payload: payload})
					}
					_ = c.Write(ctx, websocket.MessageBinary, b)
				})
				ctx := testContext(t)
				var errs []error
				if asr {
					s, err := (ASRProvider{cfg}).Open(ctx, asrConfig())
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					drain(t, ctx, s.Events())
					errs = drain(t, ctx, s.Errors())
				} else {
					s, err := (TTSProvider{cfg}).Synthesize(ctx, ttsRequest())
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					drain(t, ctx, s.Chunks())
					errs = drain(t, ctx, s.Errors())
				}
				if len(errs) != 1 {
					t.Fatalf("errors: %v", errs)
				}
				if strings.Contains(errs[0].Error(), "test-token") || strings.Contains(errs[0].Error(), "private") || strings.Contains(errs[0].Error(), "secret.invalid") {
					t.Fatal("remote detail leaked", errs)
				}
			})
		}
	}
}

func TestRejectsBeforeDial(t *testing.T) {
	var calls atomic.Int32
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	base := Config{ASREndpoint: endpoint, TTSEndpoint: endpoint + "/api/v1/tts/ws_binary", APIKeyV2: "key", AppID: "app", AccessToken: "token", ASRCluster: "asr", TTSCluster: "tts"}
	for _, change := range []func(*Config, *speech.ASRConfig){
		func(c *Config, r *speech.ASRConfig) { r.Encoding = "mp3" },
		func(c *Config, r *speech.ASRConfig) { r.SampleRate = 24000 },
		func(c *Config, r *speech.ASRConfig) { r.SessionID = "" },
		func(c *Config, r *speech.ASRConfig) { c.APIKeyV2 = ""; c.AppID = "" },
		func(c *Config, r *speech.ASRConfig) { c.ASREndpoint = "https://invalid" },
		func(c *Config, r *speech.ASRConfig) { c.ASRCluster = "" },
		func(c *Config, r *speech.ASRConfig) { c.APIKeyV2 = "bad\nheader" },
		func(c *Config, r *speech.ASRConfig) { c.ConnectTimeout = -1 },
	} {
		c, r := base, asrConfig()
		change(&c, &r)
		s, err := (ASRProvider{c}).Open(context.Background(), r)
		if err == nil || s != nil {
			t.Fatal("accepted invalid ASR")
		}
	}
	for _, change := range []func(*Config, *speech.TTSRequest){
		func(c *Config, r *speech.TTSRequest) { r.Encoding = "mp3" },
		func(c *Config, r *speech.TTSRequest) { r.SampleRate = 16000 },
		func(c *Config, r *speech.TTSRequest) { r.Text = "" },
		func(c *Config, r *speech.TTSRequest) { r.Text = strings.Repeat("a", 10001) },
		func(c *Config, r *speech.TTSRequest) { r.Voice = "" },
		func(c *Config, r *speech.TTSRequest) { r.TurnID = "" },
		func(c *Config, r *speech.TTSRequest) { r.SessionID = "" },
		func(c *Config, r *speech.TTSRequest) { c.AccessToken = "" },
		func(c *Config, r *speech.TTSRequest) { c.AppID = "" },
		func(c *Config, r *speech.TTSRequest) { c.TTSCluster = "" },
		func(c *Config, r *speech.TTSRequest) { c.TTSEndpoint = endpoint + "/api/v3/tts/bidirection" },
	} {
		c, r := base, ttsRequest()
		change(&c, &r)
		s, err := (TTSProvider{c}).Synthesize(context.Background(), r)
		if err == nil || s != nil {
			t.Fatal("accepted invalid TTS")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("validation made %d network calls", calls.Load())
	}
}

func TestSetupTimeoutAndDialErrorSanitization(t *testing.T) {
	release := make(chan struct{})
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		http.Error(w, "test-token private signed URL", 401)
	}))
	defer server.Close()
	defer close(release)
	cfg := Config{ASREndpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/?signature=test-token", APIKeyV2: "test-token", ResourceID: "asr", ConnectTimeout: 30 * time.Millisecond}
	start := time.Now()
	s, err := (ASRProvider{cfg}).Open(context.Background(), asrConfig())
	if s != nil || err != context.DeadlineExceeded || time.Since(start) > time.Second {
		t.Fatalf("%v %v", s, err)
	}
	cfg.ASREndpoint = "ws://127.0.0.1:1/?signature=test-token"
	_, err = (ASRProvider{cfg}).Open(testContext(t), asrConfig())
	if err == nil || strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "signature") {
		t.Fatal(err)
	}
}

func TestTTSOutputLimit(t *testing.T) {
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		readFrame(t, ctx, c)
		// Compress on the wire so each message remains below 1 MiB.
		payload, _ := compress(make([]byte, maxMessage))
		for i := 0; i < 9; i++ {
			if err := c.Write(ctx, websocket.MessageBinary, encode(frame{kind: audioResponse, flags: 1, sequence: int32(i + 1), compression: 1, payload: payload})); err != nil {
				return
			}
		}
	})
	ctx := testContext(t)
	s, err := (TTSProvider{cfg}).Synthesize(ctx, ttsRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chunks := drain(t, ctx, s.Chunks())
	if len(chunks) != 8 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	errs := drain(t, ctx, s.Errors())
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "8 MiB") {
		t.Fatal(errs)
	}
}

func TestASRBlockedAndQueuedWritesCancel(t *testing.T) {
	release := make(chan struct{})
	ready := make(chan struct{})
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		readFrame(t, ctx, c)
		close(ready)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	defer close(release)
	ctx := testContext(t)
	s0, err := (ASRProvider{cfg}).Open(ctx, asrConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := s0.(*asrStream)
	defer s.Close()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = s.WriteAudio(ctx, []byte{1}); err == nil {
		t.Fatal("accepted unaligned PCM")
	}
	// Hold the write gate, then prove caller deadlines cancel queued operations.
	s.writeGate <- struct{}{}
	call, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err = s.WriteAudio(call, []byte{0, 0}); err != context.DeadlineExceeded {
		t.Fatalf("queued write: %v", err)
	}
	<-s.writeGate
	writeDone := make(chan error, 1)
	go func() { writeDone <- s.WriteAudio(ctx, make([]byte, 12800)) }()
	// Wait until the first writer holds the gate, then keep a second operation
	// queued until its deadline. With a nonreading net.Pipe peer the first
	// write cannot finish during this interval.
	for len(s.writeGate) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	queued, queuedCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer queuedCancel()
	if err := s.EndUtterance(queued); err != context.DeadlineExceeded {
		t.Fatalf("queued completion: %v", err)
	}
	// The peer deliberately never reads audio, so net.Pipe blocks socket writes.
	// Close must cancel both the read loop and that independent write context.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("write unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write leaked")
	}
}

func TestInitialWriteUsesConnectTimeout(t *testing.T) {
	release := make(chan struct{})
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		// WebSocket upgrade succeeds, but the initial application request is not read.
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	defer close(release)
	cfg.ConnectTimeout = 20 * time.Millisecond
	start := time.Now()
	s, err := (ASRProvider{cfg}).Open(context.Background(), asrConfig())
	if s != nil || err != context.DeadlineExceeded || time.Since(start) > time.Second {
		t.Fatalf("initial request: %v %v", s, err)
	}
}

func TestASRLegacyAuthAndFinalText(t *testing.T) {
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		if r.Header.Get("X-Api-App-Key") != "test-app" || r.Header.Get("X-Api-Access-Key") != "test-token" || r.Header.Get("X-Api-Key") != "" {
			t.Error("wrong legacy ASR headers")
		}
		readFrame(t, ctx, c)
		send(t, ctx, c, frame{kind: fullResponse, flags: 2, serialization: 1, payload: []byte(`{"result":{"text":"done"}}`)})
	})
	cfg.APIKeyV2 = ""
	ctx := testContext(t)
	s, err := (ASRProvider{cfg}).Open(ctx, asrConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events := drain(t, ctx, s.Events())
	if len(events) != 1 || !events[0].Final || events[0].Text != "done" {
		t.Fatal(events)
	}
	if errs := drain(t, ctx, s.Errors()); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestTTSEmptyProviderFinalIsError(t *testing.T) {
	cfg := fixture(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		readFrame(t, ctx, c)
		// A valid acknowledgement followed by a final frame with no PCM.
		_ = c.Write(ctx, websocket.MessageBinary, []byte{0x11, 0xb0, 0, 0})
		_ = c.Write(ctx, websocket.MessageBinary, []byte{0x11, 0xb3, 0, 0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	})
	ctx := testContext(t)
	stream, err := (TTSProvider{cfg}).Synthesize(ctx, ttsRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if chunks := drain(t, ctx, stream.Chunks()); len(chunks) != 0 {
		t.Fatal(chunks)
	}
	errs := drain(t, ctx, stream.Errors())
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "without audio") {
		t.Fatalf("errors: %v", errs)
	}
}
