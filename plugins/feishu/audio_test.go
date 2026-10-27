package feishu

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	pb "github.com/zhoushoujianwork/Agent-Host/plugins/feishu/internal/audioproto"
)

func drain(ctx context.Context, c *websocket.Conn) {
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Literal bytes are independent of the encoder and pin the reference protocol:
// Frontier service 33555721, method 1, binary/protobuf payload, frame type 0.
func TestGoldenClientFrames(t *testing.T) {
	previous := audioClock
	audioClock = func() time.Time { return time.Date(2026, 10, 27, 1, 2, 3, 0, time.UTC) }
	t.Cleanup(func() { audioClock = previous })
	const id = "11111111-2222-4333-8444-555555555555"
	cases := []struct {
		name  string
		seq   uint64
		event *pb.ClientEvent
		want  string
	}{
		{"create", 1, sessionCreateEvent(id), "0801100018898a80102001320662696e6172793a166170706c69636174696f6e2f782d70726f746f6275664282010a0e73657373696f6e2e637265617465122431313131313131312d323232322d343333332d383434342d3535353535353535353535352214323032362d31302d32375430313a30323a30335a52340a320a300a160a09617564696f2f70636d12057331366c6518c0bb0112160a09617564696f2f70636d12057331366c6518c0bb014a005a2431313131313131312d323232322d343333332d383434342d3535353535353535353535356000"},
		{"append", 2, audioAppendEvent(7, []byte{1, 2, 3, 4}), "0802100018898a80102001320662696e6172793a166170706c69636174696f6e2f782d70726f746f62756642370a15617564696f2e757073747265616d2e617070656e6418072214323032362d31302d32375430313a30323a30335a5a060a04010203044a005a006000"},
		{"clear", 3, audioClearEvent(7, "clear-id"), "0803100018898a80102001320662696e6172793a166170706c69636174696f6e2f782d70726f746f627566423a0a14617564696f2e757073747265616d2e636c6561721208636c6561722d696418072214323032362d31302d32375430313a30323a30335a62004a005a08636c6561722d69646000"},
		{"close", 4, sessionCloseEvent(7, "close-id"), "0804100018898a80102001320662696e6172793a166170706c69636174696f6e2f782d70726f746f62756642350a0d73657373696f6e2e636c6f73651208636c6f73652d696418072214323032362d31302d32375430313a30323a30335a6a0208014a005a08636c6f73652d69646000"},
	}
	for _, tc := range cases {
		got, err := encodeAudioFrame(tc.seq, tc.event)
		if err != nil || !bytes.Equal(got, mustHex(t, tc.want)) {
			t.Errorf("%s: err=%v got=%x", tc.name, err, got)
		}
	}
}

func TestGoldenServerFrames(t *testing.T) {
	created, err := decodeAudioFrame(mustHex(t, "0809100018898a8010200142500a0f73657373696f6e2e6372656174656412057372762d311807523412320a300a160a09617564696f2f70636d12057331366c6518c0bb0112160a09617564696f2f70636d12057331366c6518c0bb016000"))
	if err != nil || created.GetType() != "session.created" || created.GetSessionId() != 7 || !validFormat(created.GetSessionCreated().GetSession().GetMedia().GetAudioDownstreamFormat()) {
		t.Fatalf("created: %v %v", created, err)
	}
	delta, err := decodeAudioFrame(mustHex(t, "0809100018898a8010200142750a16617564696f2e646f776e73747265616d2e64656c746112057372762d3218072214323032362d31302d32375430313a30323a30345aa2013b0a07747261636b2d61120d7061727469636970616e742d311a160a09617564696f2f70636d12057331366c6518c0bb0120b00928643204010002006000"))
	d := delta.GetAudioDownstreamDelta()
	if err != nil || d.GetTrackId() != "track-a" || d.GetSource() != "participant-1" || d.GetPtsMs() != 1200 || d.GetDurationMs() != 100 || !bytes.Equal(d.GetDelta(), []byte{1, 0, 2, 0}) {
		t.Fatalf("delta: %v %v", delta, err)
	}
	// Ack frames (type 1) are skipped, never treated as events.
	if event, err := decodeAudioFrame(mustHex(t, "0809100018898a801020016001")); event != nil || err != nil {
		t.Fatalf("ack: %v %v", event, err)
	}
}

func TestDecodeRejectsBadFrames(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":        nil,
		"garbage":      {0xff, 0xff, 0xff},
		"oversize":     make([]byte, maxAudioMessage+1),
		"unknown type": serverFrame(7, createdEvent(7, "")),
		"bad payload":  mustHex(t, "0809100018898a8010200142030affff6000"),
	} {
		if event, err := decodeAudioFrame(raw); err == nil || event != nil {
			t.Errorf("%s: accepted %v", name, event)
		}
	}
	if event, err := decodeAudioFrame(serverFrame(0, nil)); event != nil || err != nil {
		t.Errorf("empty payload must be skipped: %v %v", event, err)
	}
}

func TestOpenAudioHandshakeAndRead(t *testing.T) {
	var create *pb.ClientEvent
	var frame *pb.Frame
	client, transport := audioFixture(t, func(ctx context.Context, c *websocket.Conn) {
		var err error
		frame, create, err = readClient(ctx, t, c)
		if err != nil {
			return
		}
		sendEvent(ctx, t, c, createdEvent(7, create.GetEventId()))
		sendEvent(ctx, t, c, &pb.ServerEvent{Type: "future.event", SessionId: 7}) // ignored
		if err := c.Write(ctx, websocket.MessageBinary, serverFrame(2, nil)); err != nil {
			return
		}
		sendEvent(ctx, t, c, deltaEvent(7, []byte{1, 0, 2, 0}))
		drain(ctx, c)
	})
	s, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wantArgs := []string{"--profile", "fixture", "api", "GET", "/open-apis/vc/v1/realtime/endpoint", "--params", `{"meeting_id":"` + testMeeting + `"}`, "--as", "bot", "--format", "json"}
	if len(transport.calls) != 1 || !reflect.DeepEqual(transport.calls[0], wantArgs) {
		t.Fatalf("endpoint command: %v", transport.calls)
	}
	if frame.GetService() != 33555721 || frame.GetMethod() != 1 || frame.GetPayloadEncoding() != "binary" || frame.GetPayloadType() != "application/x-protobuf" || frame.GetFrameType() != 0 || frame.GetSeqID() != 1 || frame.GetMsgId() != create.GetEventId() {
		t.Fatalf("frame: %v", frame)
	}
	media := create.GetSessionCreate().GetSession().GetMedia()
	if create.GetSessionId() != 0 || len(create.GetEventId()) != 36 || create.GetCreatedAt() == "" || !validFormat(media.GetAudioUpstreamFormat()) || !validFormat(media.GetAudioDownstreamFormat()) {
		t.Fatalf("create: %v", create)
	}
	if s.SessionID() != 7 || s.Format() != (AudioFormat{Type: "audio/pcm", Encoding: "s16le", SampleRate: 24000}) {
		t.Fatalf("session: %d %v", s.SessionID(), s.Format())
	}
	got, err := s.ReadAudio(testCtx(t))
	want := AudioFrame{TrackID: "track-a", Source: "participant-1", Format: s.Format(), PTSMS: 1200, DurationMS: 100, PCM: []byte{1, 0, 2, 0}, EventID: "srv-2", CreatedAt: "2026-10-27T01:02:04Z"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read: %+v %v", got, err)
	}
}

func TestWriteChunksSerializedAndClear(t *testing.T) {
	type sent struct {
		seq  uint64
		kind string
		size int
		sess int64
	}
	var mu sync.Mutex
	var got []sent
	done := make(chan struct{})
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			frame, event, err := readClient(ctx, t, c)
			if err != nil {
				return
			}
			mu.Lock()
			got = append(got, sent{frame.GetSeqID(), event.GetType(), len(event.GetAudioUpstreamAppend().GetDelta()), event.GetSessionId()})
			n := len(got)
			mu.Unlock()
			if n == 16+1 {
				close(done)
			}
		}
	})
	// 9600 bytes -> two 4800-byte chunks; the largest allowed chunk is never exceeded.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.WriteAudio(testCtx(t), make([]byte, 9600)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.ClearAudio(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAudio(testCtx(t), nil); err != nil { // empty is a no-op
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive every frame")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, f := range got { // session.create (seq 1) was consumed by the handshake
		if f.seq != uint64(i+2) {
			t.Fatalf("frame %d seq %d: wire order must follow sequence order", i, f.seq)
		}
		if i < 16 && (f.kind != "audio.upstream.append" || f.size != 4800 || f.sess != 7) {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if last := got[len(got)-1]; last.kind != "audio.upstream.clear" || last.sess != 7 {
		t.Fatalf("last: %+v", last)
	}
}

func TestWriteSplitsRemainderAndRejectsInvalidPCM(t *testing.T) {
	sizes := make(chan int, 4)
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			_, event, err := readClient(ctx, t, c)
			if err != nil {
				return
			}
			sizes <- len(event.GetAudioUpstreamAppend().GetDelta())
		}
	})
	if err := s.WriteAudio(testCtx(t), make([]byte, 4800+4800+400)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{4800, 4800, 400} {
		if n := <-sizes; n != want {
			t.Fatalf("chunk %d want %d", n, want)
		}
	}
	// Odd and oversize input is rejected, not trimmed, and sends nothing.
	for _, pcm := range [][]byte{make([]byte, 1), make([]byte, 4801), make([]byte, maxWritePCM+2)} {
		if err := s.WriteAudio(testCtx(t), pcm); err == nil {
			t.Fatalf("accepted %d bytes", len(pcm))
		}
	}
	select {
	case n := <-sizes:
		t.Fatalf("rejected write sent %d bytes", n)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.WriteAudio(testCtx(t), make([]byte, 2)); err != nil { // session survives rejection
		t.Fatal(err)
	}
}

func TestServerErrorTerminatesWithoutProviderText(t *testing.T) {
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		sendEvent(ctx, t, c, errorEvent(pb.ErrorCode_ERROR_CODE_MEDIA_SUBSCRIPTION_CONFLICT))
		drain(ctx, c)
	})
	_, err := s.ReadAudio(testCtx(t))
	var serverErr *AudioServerError
	if !errors.As(err, &serverErr) || serverErr.Code != 2002 || !serverErr.Retryable {
		t.Fatalf("error: %v", err)
	}
	noSecret(t, err)
	// Sticky: every later call fails with the same error and nothing is sent.
	if _, again := s.ReadAudio(testCtx(t)); again != err {
		t.Fatalf("not sticky: %v", again)
	}
	if write := s.WriteAudio(testCtx(t), []byte{0, 0}); write != err {
		t.Fatalf("write after error: %v", write)
	}
	if clear := s.ClearAudio(testCtx(t)); clear != err {
		t.Fatalf("clear after error: %v", clear)
	}
	if s.Close() != nil {
		t.Fatal("Close after terminal error must be clean")
	}
}

func TestServerCloseTerminates(t *testing.T) {
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		sendEvent(ctx, t, c, closedEvent(7, pb.ServerCloseReason_IDLE_TIMEOUT))
		drain(ctx, c)
	})
	_, err := s.ReadAudio(testCtx(t))
	var closed *AudioClosedError
	if !errors.As(err, &closed) || closed.Reason != "IDLE_TIMEOUT" {
		t.Fatalf("error: %v", err)
	}
	if _, again := s.ReadAudio(testCtx(t)); again != err {
		t.Fatalf("not sticky: %v", again)
	}
}

func TestReadRejectsProtocolViolations(t *testing.T) {
	cases := map[string]func(context.Context, *websocket.Conn){
		"other session": func(ctx context.Context, c *websocket.Conn) { sendEvent(ctx, t, c, deltaEvent(8, []byte{0, 0})) },
		"odd chunk":     func(ctx context.Context, c *websocket.Conn) { sendEvent(ctx, t, c, deltaEvent(7, []byte{0, 0, 0})) },
		"empty chunk":   func(ctx context.Context, c *websocket.Conn) { sendEvent(ctx, t, c, deltaEvent(7, nil)) },
		"wrong chunk format": func(ctx context.Context, c *websocket.Conn) {
			e := deltaEvent(7, []byte{0, 0})
			e.GetAudioDownstreamDelta().Format.SampleRate = 16000
			sendEvent(ctx, t, c, e)
		},
		"long label": func(ctx context.Context, c *websocket.Conn) {
			e := deltaEvent(7, []byte{0, 0})
			e.GetAudioDownstreamDelta().Source = strings.Repeat("s", maxAudioLabel+1)
			sendEvent(ctx, t, c, e)
		},
		"missing payload": func(ctx context.Context, c *websocket.Conn) {
			sendEvent(ctx, t, c, &pb.ServerEvent{Type: "audio.downstream.delta", SessionId: 7})
		},
		"closed other session": func(ctx context.Context, c *websocket.Conn) { sendEvent(ctx, t, c, closedEvent(9, 1)) },
		"repeated created":     func(ctx context.Context, c *websocket.Conn) { sendEvent(ctx, t, c, createdEvent(7, "")) },
		"text message": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageText, []byte("hello"))
		},
		"malformed frame": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, []byte{0xff, 0xff, 0xff, 0xff})
		},
		"unknown frame type": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, serverFrame(5, deltaEvent(7, []byte{0, 0})))
		},
		"oversize message": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, make([]byte, maxAudioMessage+1))
		},
		"endless acks": func(ctx context.Context, c *websocket.Conn) {
			for i := 0; i < maxSkippedFrames+5; i++ {
				if c.Write(ctx, websocket.MessageBinary, serverFrame(1, nil)) != nil {
					return
				}
			}
		},
		"endless unknown events": func(ctx context.Context, c *websocket.Conn) {
			for i := 0; i < maxSkippedFrames+5; i++ {
				if c.Write(ctx, websocket.MessageBinary, serverFrame(0, &pb.ServerEvent{Type: "noise", SessionId: 7})) != nil {
					return
				}
			}
		},
		"socket closed": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Close(websocket.StatusNormalClosure, "bye "+secretTicket)
		},
	}
	for name, serve := range cases {
		t.Run(name, func(t *testing.T) {
			s := openFixture(t, func(ctx context.Context, c *websocket.Conn) { serve(ctx, c); drain(ctx, c) })
			_, err := s.ReadAudio(testCtx(t))
			if err == nil {
				t.Fatal("violation accepted")
			}
			noSecret(t, err)
			if _, again := s.ReadAudio(testCtx(t)); again != err {
				t.Fatalf("not sticky: %v", again)
			}
			if write := s.WriteAudio(testCtx(t), []byte{0, 0}); write != err {
				t.Fatalf("write after violation: %v", write)
			}
		})
	}
}

func TestOpenAudioRejectsBadSessions(t *testing.T) {
	bad := map[string]func(context.Context, *websocket.Conn, *pb.ClientEvent){
		"server error": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			sendEvent(ctx, t, c, errorEvent(pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED))
		},
		"closed": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			sendEvent(ctx, t, c, closedEvent(0, pb.ServerCloseReason_INTERNAL_ERROR))
		},
		"audio first": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			sendEvent(ctx, t, c, deltaEvent(7, []byte{0, 0}))
		},
		"no session identity": func(ctx context.Context, c *websocket.Conn, e *pb.ClientEvent) {
			sendEvent(ctx, t, c, createdEvent(0, e.GetEventId()))
		},
		"negative identity": func(ctx context.Context, c *websocket.Conn, e *pb.ClientEvent) {
			sendEvent(ctx, t, c, createdEvent(-3, e.GetEventId()))
		},
		"other request": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			sendEvent(ctx, t, c, createdEvent(7, "someone-else"))
		},
		"no media": func(ctx context.Context, c *websocket.Conn, e *pb.ClientEvent) {
			created := createdEvent(7, e.GetEventId())
			created.GetSessionCreated().Session = nil
			sendEvent(ctx, t, c, created)
		},
		"wrong upstream rate": func(ctx context.Context, c *websocket.Conn, e *pb.ClientEvent) {
			created := createdEvent(7, e.GetEventId())
			created.GetSessionCreated().Session.Media.AudioUpstreamFormat.SampleRate = 16000
			sendEvent(ctx, t, c, created)
		},
		"wrong downstream encoding": func(ctx context.Context, c *websocket.Conn, e *pb.ClientEvent) {
			created := createdEvent(7, e.GetEventId())
			created.GetSessionCreated().Session.Media.AudioDownstreamFormat.Encoding = "f32le"
			sendEvent(ctx, t, c, created)
		},
		"no payload": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			sendEvent(ctx, t, c, &pb.ServerEvent{Type: "session.created", SessionId: 7})
		},
		"malformed": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			_ = c.Write(ctx, websocket.MessageBinary, []byte{0xff, 0xff, 0xff})
		},
		"hangup": func(ctx context.Context, c *websocket.Conn, _ *pb.ClientEvent) {
			_ = c.Close(websocket.StatusInternalError, secretTicket)
		},
	}
	for name, respond := range bad {
		t.Run(name, func(t *testing.T) {
			client, _ := audioFixture(t, func(ctx context.Context, c *websocket.Conn) {
				_, event, err := readClient(ctx, t, c)
				if err != nil {
					return
				}
				respond(ctx, c, event)
				drain(ctx, c)
			})
			s, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
			if err == nil || s != nil {
				t.Fatalf("accepted bad session: %v", s)
			}
			noSecret(t, err)
		})
	}
}

func TestOpenAudioSetupCancellationAndTimeout(t *testing.T) {
	serve := func(ctx context.Context, c *websocket.Conn) { drain(ctx, c) } // never answers
	t.Run("cancel", func(t *testing.T) {
		client, _ := audioFixture(t, serve)
		ctx, cancel := context.WithCancel(testCtx(t))
		time.AfterFunc(100*time.Millisecond, cancel)
		if s, err := client.OpenAudio(ctx, testMeeting, AudioConfig{}); !errors.Is(err, context.Canceled) || s != nil {
			t.Fatalf("got %v %v", s, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		client, _ := audioFixture(t, serve)
		if s, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{SetupTimeout: 150 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) || s != nil {
			t.Fatalf("got %v %v", s, err)
		}
	})
	t.Run("already cancelled never starts a command", func(t *testing.T) {
		client, transport := audioFixture(t, serve)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.OpenAudio(ctx, testMeeting, AudioConfig{}); !errors.Is(err, context.Canceled) || len(transport.calls) != 0 {
			t.Fatalf("got %v calls=%d", err, len(transport.calls))
		}
	})
}

func TestOpenAudioInvalidInputNeverInvokesTransport(t *testing.T) {
	client, transport := clientFor(t)
	for name, tc := range map[string]struct {
		meeting string
		config  AudioConfig
	}{
		"short meeting":  {"123", AudioConfig{}},
		"injection":      {testMeeting + " --as user", AudioConfig{}},
		"sample rate":    {testMeeting, AudioConfig{SampleRate: 16000}},
		"setup timeout":  {testMeeting, AudioConfig{SetupTimeout: -1}},
		"close timeout":  {testMeeting, AudioConfig{CloseTimeout: -1}},
		"meeting number": {"123456789", AudioConfig{}},
	} {
		if _, err := client.OpenAudio(testCtx(t), tc.meeting, tc.config); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(transport.calls) != 0 {
		t.Fatalf("transport invoked: %v", transport.calls)
	}
}

func TestReadCancellationEndsSession(t *testing.T) {
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) { drain(ctx, c) })
	ctx, cancel := context.WithCancel(testCtx(t))
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := s.ReadAudio(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	// The socket had to be closed to interrupt the read, so the session is over
	// and later calls report that explicitly rather than reusing a broken socket.
	if _, again := s.ReadAudio(testCtx(t)); again == nil || errors.Is(again, context.Canceled) {
		t.Fatalf("later read: %v", again)
	}
	// A context cancelled before any I/O is a plain rejection and keeps the session.
	s2 := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		sendEvent(ctx, t, c, deltaEvent(7, []byte{0, 0}))
		drain(ctx, c)
	})
	dead, kill := context.WithCancel(context.Background())
	kill()
	if _, err := s2.ReadAudio(dead); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if err := s2.WriteAudio(dead, []byte{0, 0}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := s2.ReadAudio(testCtx(t)); err != nil {
		t.Fatalf("session should survive pre-I/O cancellation: %v", err)
	}
}

func TestCanceledAudioGateDoesNotAcquireOrTerminateSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &AudioSession{terminal: make(chan struct{})}
	gate := make(chan struct{}, 1)
	// A cancellation and a newly released gate can both be ready. Neither
	// selection may allow socket I/O that would terminate the shared session.
	for i := 0; i < 100; i++ {
		if err := s.acquire(ctx, gate); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled acquisition returned %v", err)
		}
		if len(gate) != 0 || s.terminalErr() != nil {
			t.Fatal("canceled acquisition retained the gate or ended the session")
		}
	}
}

func TestBlockedWriteCancellation(t *testing.T) {
	// The peer stops reading, so net.Pipe blocks the socket write.
	release := make(chan struct{})
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(testCtx(t))
	time.AfterFunc(150*time.Millisecond, cancel)
	err := s.WriteAudio(ctx, make([]byte, 4800*40))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if err := s.ClearAudio(testCtx(t)); err == nil {
		t.Fatal("session must be over after an interrupted write")
	}
}

func TestCloseSendsNoticeIsIdempotentAndUnblocksReads(t *testing.T) {
	notice := make(chan *pb.ClientEvent, 2)
	s := openFixture(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			_, event, err := readClient(ctx, t, c)
			if err != nil {
				return
			}
			notice <- event
		}
	})
	readErr := make(chan error, 1)
	go func() { _, err := s.ReadAudio(testCtx(t)); readErr <- err }()
	time.Sleep(100 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, ErrAudioClosed) {
			t.Fatalf("pending read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock ReadAudio")
	}
	event := <-notice
	if event.GetType() != "session.close" || event.GetSessionId() != 7 || event.GetSessionClose().GetReason() != pb.ClientCloseReason_USER_LEFT {
		t.Fatalf("close notice: %v", event)
	}
	if len(notice) != 0 {
		t.Fatal("close notice sent more than once")
	}
	for name, err := range map[string]error{"write": s.WriteAudio(testCtx(t), []byte{0, 0}), "clear": s.ClearAudio(testCtx(t))} {
		if !errors.Is(err, ErrAudioClosed) {
			t.Errorf("%s after close: %v", name, err)
		}
	}
}

func TestCloseIsBounded(t *testing.T) {
	client, _ := audioFixture(t, func(ctx context.Context, c *websocket.Conn) {
		handshake(ctx, t, c)
		<-ctx.Done() // never reads again: the close notice cannot be delivered
	})
	s, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{CloseTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := s.Close(); err == nil {
		t.Fatal("undelivered close notice must be reported")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("Close was not bounded")
	}
	if _, err := s.ReadAudio(testCtx(t)); !errors.Is(err, ErrAudioClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestEndpointParsing(t *testing.T) {
	const url = "wss://example.test/realtime?ticket=" + secretTicket
	good := map[string]string{
		"cli envelope":       `{"ok":true,"data":{"websocket_url":"` + url + `"}}`,
		"raw platform":       `{"code":0,"msg":"success","data":{"websocket_url":"` + url + `"}}`,
		"envelope over raw":  `{"ok":true,"data":{"code":0,"data":{"websocket_url":"` + url + `"}}}`,
		"loopback plaintext": `{"ok":true,"data":{"websocket_url":"ws://127.0.0.1:9/x"}}`,
	}
	for name, raw := range good {
		if got, err := parseAudioEndpoint(Output{Stdout: []byte(raw)}); err != nil || got == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]string{
		"not json":         `nope ` + secretTicket,
		"two documents":    `{"ok":true,"data":{"websocket_url":"` + url + `"}}{}`,
		"ok false":         `{"ok":false,"error":{"type":"auth","message":"` + secretTicket + `"}}`,
		"code nonzero":     `{"code":99991672,"msg":"` + secretTicket + `"}`,
		"inner code":       `{"ok":true,"data":{"code":5,"data":{"websocket_url":"` + url + `"}}}`,
		"inner error url":  `{"ok":true,"data":{"code":5,"websocket_url":"` + url + `"}}`,
		"no discriminator": `{"data":{"websocket_url":"` + url + `"}}`,
		"missing url":      `{"ok":true,"data":{}}`,
		"empty url":        `{"ok":true,"data":{"websocket_url":""}}`,
		"https":            `{"ok":true,"data":{"websocket_url":"https://example.test/x"}}`,
		"remote plaintext": `{"ok":true,"data":{"websocket_url":"ws://example.test/x"}}`,
		"userinfo":         `{"ok":true,"data":{"websocket_url":"wss://user:` + secretTicket + `@example.test/x"}}`,
		"fragment":         `{"ok":true,"data":{"websocket_url":"wss://example.test/x#frag"}}`,
		"no host":          `{"ok":true,"data":{"websocket_url":"wss:///x"}}`,
		"space":            `{"ok":true,"data":{"websocket_url":"wss://example.test/x y"}}`,
		"oversize":         `{"ok":true,"data":{"websocket_url":"wss://example.test/` + strings.Repeat("a", 5000) + `"}}`,
	}
	for name, raw := range bad {
		_, err := parseAudioEndpoint(Output{Stdout: []byte(raw)})
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		noSecret(t, err)
	}
	if _, err := parseAudioEndpoint(Output{Stderr: []byte(good["cli envelope"])}); err == nil {
		t.Error("endpoint must come from stdout only")
	}
}

func TestEndpointFailuresNeverExposeSecrets(t *testing.T) {
	secretOut := Output{Stdout: []byte(`{"ok":true,"data":{"websocket_url":"wss://example.test/x?ticket=` + secretTicket + `"}}`), Stderr: []byte(secretTicket), Started: true}
	t.Run("process error", func(t *testing.T) {
		client, _ := clientFor(t, response{Output: secretOut, err: errors.New("exit 1 " + secretTicket)})
		s, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
		if err == nil || s != nil {
			t.Fatal("process error accepted although stdout looked valid")
		}
		noSecret(t, err)
	})
	t.Run("transport cancellation", func(t *testing.T) {
		client, _ := clientFor(t, response{err: context.DeadlineExceeded})
		ctx, cancel := context.WithCancel(testCtx(t))
		cancel()
		if _, err := client.OpenAudio(ctx, testMeeting, AudioConfig{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("dial failure", func(t *testing.T) {
		audioFixture(t, func(context.Context, *websocket.Conn) {}) // installs the pipe transport
		client, _ := clientFor(t, result(envelopeFor("ws://127.0.0.1:81/x?ticket="+secretTicket)))
		_, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
		if err == nil {
			t.Fatal("dial succeeded")
		}
		noSecret(t, err)
	})
	t.Run("handshake rejected", func(t *testing.T) {
		audioFixture(t, func(context.Context, *websocket.Conn) {})
		client, _ := clientFor(t, result(envelopeFor("ws://127.0.0.1:80/x?ticket=wrong-"+secretTicket)))
		_, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
		if err == nil {
			t.Fatal("handshake succeeded")
		}
		noSecret(t, err)
	})
	t.Run("single attempt", func(t *testing.T) {
		client, transport := clientFor(t, result(envelopeFor("ws://127.0.0.1:81/x")), result(envelopeFor("ws://127.0.0.1:81/x")))
		audioFixture(t, func(context.Context, *websocket.Conn) {})
		_, _ = client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
		if len(transport.calls) != 1 {
			t.Fatalf("endpoint requested %d times; retries are forbidden", len(transport.calls))
		}
	})
}
