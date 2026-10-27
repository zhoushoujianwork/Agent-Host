package feishu

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	pb "github.com/zhoushoujianwork/Agent-Host/plugins/feishu/internal/audioproto"
	"google.golang.org/protobuf/proto"
)

const secretTicket = "SECRET-TICKET-9f3a"

// A real HTTP server and WebSocket handshake over net.Pipe avoid needing
// loopback-listener privileges in restricted developer/test environments.
// Audio tests are intentionally not parallel because they replace the
// package-level handshake transport for the lifetime of a fixture.
type pipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (p *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-p.connections:
		return c, nil
	case <-p.done:
		return nil, net.ErrClosed
	}
}
func (p *pipeListener) Close() error   { p.once.Do(func() { close(p.done) }); return nil }
func (p *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }

func envelopeFor(url string) string {
	return `{"ok":true,"data":{"websocket_url":"` + url + `"}}`
}

// audioFixture serves one WebSocket connection with serve and returns a client
// whose scripted transport hands out that endpoint (containing a secret ticket).
func audioFixture(t *testing.T, serve func(context.Context, *websocket.Conn)) (*MeetingClient, *scriptedTransport) {
	t.Helper()
	listener := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "127.0.0.1:80" {
			return nil, errors.New("test dial refused")
		}
		client, server := net.Pipe()
		select {
		case listener.connections <- server:
			return client, nil
		case <-ctx.Done():
			_ = client.Close()
			_ = server.Close()
			return nil, ctx.Err()
		case <-listener.done:
			_ = client.Close()
			_ = server.Close()
			return nil, net.ErrClosed
		}
	}}
	previous := audioRoundTripper
	audioRoundTripper = transport
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != secretTicket {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(4 << 20)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		serve(ctx, conn)
	})}}
	server.Start()
	t.Cleanup(func() { server.Close(); transport.CloseIdleConnections(); audioRoundTripper = previous })
	return clientFor(t, result(envelopeFor("ws://127.0.0.1:80/realtime?ticket="+secretTicket)))
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func pcmFormat() *pb.AudioFormat {
	return &pb.AudioFormat{Type: "audio/pcm", Encoding: "s16le", SampleRate: 24000}
}

func createdEvent(session int64, clientID string) *pb.ServerEvent {
	return &pb.ServerEvent{Type: "session.created", EventId: "srv-1", SessionId: session,
		Payload: &pb.ServerEvent_SessionCreated{SessionCreated: &pb.SessionCreated{ClientEventId: clientID,
			Session: &pb.Session{Media: &pb.Media{AudioUpstreamFormat: pcmFormat(), AudioDownstreamFormat: pcmFormat()}}}}}
}

func deltaEvent(session int64, pcm []byte) *pb.ServerEvent {
	return &pb.ServerEvent{Type: "audio.downstream.delta", EventId: "srv-2", SessionId: session, CreatedAt: "2026-10-27T01:02:04Z",
		Payload: &pb.ServerEvent_AudioDownstreamDelta{AudioDownstreamDelta: &pb.AudioDownstreamDelta{
			TrackId: "track-a", Source: "participant-1", Format: pcmFormat(), PtsMs: 1200, DurationMs: 100, Delta: pcm}}}
}

func errorEvent(code pb.ErrorCode) *pb.ServerEvent {
	return &pb.ServerEvent{Type: "error", SessionId: 7, Payload: &pb.ServerEvent_Error{Error: &pb.Error{
		Code: code, Message: "provider text " + secretTicket, Retryable: true, Details: map[string]string{"k": secretTicket}}}}
}

func closedEvent(session int64, reason pb.ServerCloseReason) *pb.ServerEvent {
	return &pb.ServerEvent{Type: "session.closed", SessionId: session,
		Payload: &pb.ServerEvent_SessionClosed{SessionClosed: &pb.SessionClosed{SessionId: session, Reason: reason}}}
}

// serverFrame wraps a server event like the service does.
func serverFrame(frameType int32, event *pb.ServerEvent) []byte {
	var payload []byte
	if event != nil {
		payload, _ = proto.Marshal(event)
	}
	frame := &pb.Frame{SeqID: proto.Uint64(9), LogID: proto.Uint64(0), Service: proto.Int32(audioFrontierService),
		Method: proto.Int32(audioFrontierMethod), Payload: payload, FrameType: proto.Int32(frameType)}
	b, _ := proto.Marshal(frame)
	return b
}

func sendEvent(ctx context.Context, t *testing.T, c *websocket.Conn, event *pb.ServerEvent) {
	t.Helper()
	if err := c.Write(ctx, websocket.MessageBinary, serverFrame(0, event)); err != nil {
		t.Logf("server write: %v", err)
	}
}

// readClient reads and strictly decodes one client frame.
func readClient(ctx context.Context, t *testing.T, c *websocket.Conn) (*pb.Frame, *pb.ClientEvent, error) {
	t.Helper()
	typ, raw, err := c.Read(ctx)
	if err != nil {
		return nil, nil, err
	}
	if typ != websocket.MessageBinary {
		t.Error("client sent a non-binary message")
	}
	var frame pb.Frame
	var event pb.ClientEvent
	if err := proto.Unmarshal(raw, &frame); err != nil {
		t.Errorf("frame: %v", err)
	} else if err := proto.Unmarshal(frame.GetPayload(), &event); err != nil {
		t.Errorf("event: %v", err)
	}
	return &frame, &event, nil
}

// handshake performs the standard server side: read session.create, answer.
func handshake(ctx context.Context, t *testing.T, c *websocket.Conn) {
	t.Helper()
	_, event, err := readClient(ctx, t, c)
	if err != nil {
		t.Errorf("server read: %v", err)
		return
	}
	if event.GetType() != "session.create" {
		t.Errorf("first event %q", event.GetType())
	}
	if err := c.Write(ctx, websocket.MessageBinary, serverFrame(1, nil)); err != nil { // ack frame is skipped
		return
	}
	sendEvent(ctx, t, c, createdEvent(7, event.GetEventId()))
}

func openFixture(t *testing.T, serve func(context.Context, *websocket.Conn)) *AudioSession {
	t.Helper()
	client, _ := audioFixture(t, func(ctx context.Context, c *websocket.Conn) {
		handshake(ctx, t, c)
		serve(ctx, c)
	})
	s, err := client.OpenAudio(testCtx(t), testMeeting, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), secretTicket) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "ticket")) {
		t.Fatalf("error exposes endpoint or provider text: %q", err)
	}
}
