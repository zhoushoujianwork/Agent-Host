package feishu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
	pb "github.com/zhoushoujianwork/Agent-Host/plugins/feishu/internal/audioproto"
)

// AudioFormat describes one direction of meeting audio. This implementation only
// negotiates and accepts 24 kHz mono signed little-endian PCM.
type AudioFormat struct {
	Type       string // "audio/pcm"
	Encoding   string // "s16le"
	SampleRate int    // 24000
}

// AudioConfig selects optional bounds. The zero value is valid. SampleRate may
// only be zero or 24000 because the supplied reference protocol uses 24 kHz in
// both directions.
type AudioConfig struct {
	SampleRate int
	// SetupTimeout bounds the endpoint request, WebSocket handshake and
	// session negotiation together. Zero means 15 seconds.
	SetupTimeout time.Duration
	// CloseTimeout bounds delivery of the close notice. Zero means 5 seconds.
	CloseTimeout time.Duration
}

// AudioFrame is one downstream chunk of meeting audio with its server metadata.
// PCM is owned by the caller. Format reports the negotiated format when the
// server omitted it from the event.
type AudioFrame struct {
	TrackID    string
	Source     string
	Format     AudioFormat
	PTSMS      uint64
	DurationMS uint32
	PCM        []byte
	EventID    string
	CreatedAt  string
}

// ErrAudioClosed is returned after the local application closed the session.
var ErrAudioClosed = errors.New("Feishu audio session is closed")

// AudioServerError reports a server error event. The provider message and
// details are intentionally not retained.
type AudioServerError struct {
	Code      int32
	Retryable bool
}

func (e *AudioServerError) Error() string {
	return fmt.Sprintf("Feishu audio server error (code %d)", e.Code)
}

// AudioClosedError reports that the server ended the audio session.
type AudioClosedError struct{ Reason string }

func (e *AudioClosedError) Error() string {
	return "Feishu audio session closed by server (" + e.Reason + ")"
}

// audioRoundTripper is a test seam for the WebSocket handshake; nil selects the
// default transport.
var audioRoundTripper http.RoundTripper

var (
	errAudioInterrupted = errors.New("Feishu audio session was terminated by context cancellation")
	errAudioDial        = errors.New("Feishu audio connection could not be established")
)

// AudioSession is one bidirectional audio connection. The first terminal event
// (server error or close, protocol violation, cancellation during I/O, local
// Close) ends it permanently; there is no reconnect, rejoin or replay.
// ReadAudio calls are serialized with each other, and WriteAudio/ClearAudio/Close
// are serialized with each other. Reading and writing may run concurrently.
type AudioSession struct {
	conn         *websocket.Conn
	sessionID    int64
	format       AudioFormat
	closeTimeout time.Duration

	readGate, writeGate chan struct{}
	seq                 uint64 // guarded by writeGate

	done      chan struct{}
	terminal  chan struct{} // closed once err is set
	err       error         // guarded by terminal closure
	failGuard chan struct{}
	closeGate chan struct{}
	closeErr  error
}

// OpenAudio fetches the realtime endpoint for a meeting the bot already joined
// through the configured profile, opens the WebSocket and negotiates the session.
// It makes one attempt. The signed endpoint URL is never returned, stored or
// included in errors. Close the session when finished; closing does not leave
// the meeting (use Leave).
func (c *MeetingClient) OpenAudio(ctx context.Context, meetingID string, config AudioConfig) (*AudioSession, error) {
	if !meetingPattern.MatchString(meetingID) {
		return nil, errors.New("Feishu audio requires a long meeting ID")
	}
	if config.SampleRate != 0 && config.SampleRate != audioSampleRate {
		return nil, errors.New("Feishu audio supports only 24000 Hz")
	}
	if config.SetupTimeout < 0 || config.CloseTimeout < 0 {
		return nil, errors.New("Feishu audio timeouts must not be negative")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	setup := config.SetupTimeout
	if setup == 0 {
		setup = 15 * time.Second
	}
	closeTimeout := config.CloseTimeout
	if closeTimeout == 0 {
		closeTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, setup)
	defer cancel()

	out, runErr := c.transport.Run(ctx, endpointArguments(c.config.Profile, meetingID)...)
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Feishu realtime endpoint request failed")
	}
	endpoint, err := parseAudioEndpoint(out)
	if err != nil {
		return nil, err
	}
	// Redirects would forward the signed URL; refuse to follow them.
	client := &http.Client{Transport: audioRoundTripper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errAudioDial // Dial errors embed the URL; never wrap them.
	}
	conn.SetReadLimit(maxAudioMessage)
	s := &AudioSession{
		conn: conn, closeTimeout: closeTimeout,
		format:   AudioFormat{Type: audioMediaType, Encoding: audioMediaEncoding, SampleRate: audioSampleRate},
		readGate: make(chan struct{}, 1), writeGate: make(chan struct{}, 1),
		done: make(chan struct{}), terminal: make(chan struct{}), failGuard: make(chan struct{}, 1), closeGate: make(chan struct{}, 1),
	}
	if err := s.negotiate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *AudioSession) negotiate(ctx context.Context) error {
	requestID := audioID()
	frame, err := encodeAudioFrame(s.nextSeq(), sessionCreateEvent(requestID))
	if err != nil {
		s.fail(err)
		return err
	}
	if err := s.conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		return s.ioFailure(ctx, err)
	}
	for ignored := 0; ; ignored++ {
		if ignored > maxSkippedFrames {
			return s.fail(protocolError("no session.created received"))
		}
		event, err := s.next(ctx)
		if err != nil {
			return err
		}
		switch event.GetType() {
		case "session.created":
			return s.acceptCreated(event, requestID)
		case "error":
			return s.failServer(event)
		case "session.closed":
			return s.failClosed(event)
		case "audio.downstream.delta":
			return s.fail(protocolError("audio before session.created"))
		}
	}
}

func (s *AudioSession) acceptCreated(event *pb.ServerEvent, requestID string) error {
	created := event.GetSessionCreated()
	if created == nil {
		return s.fail(protocolError("session.created has no payload"))
	}
	if event.GetSessionId() <= 0 {
		return s.fail(protocolError("session.created has no session identity"))
	}
	if id := created.GetClientEventId(); id != "" && id != requestID {
		return s.fail(protocolError("session.created answers a different request"))
	}
	media := created.GetSession().GetMedia()
	if !validFormat(media.GetAudioUpstreamFormat()) || !validFormat(media.GetAudioDownstreamFormat()) {
		return s.fail(protocolError("negotiated media is not 24 kHz mono s16le PCM"))
	}
	s.sessionID = event.GetSessionId()
	return nil
}

func validFormat(f *pb.AudioFormat) bool {
	return f != nil && f.GetType() == audioMediaType && f.GetEncoding() == audioMediaEncoding && f.GetSampleRate() == audioSampleRate
}

// SessionID returns the server-assigned session identity.
func (s *AudioSession) SessionID() int64 { return s.sessionID }

// Format returns the negotiated format of both directions.
func (s *AudioSession) Format() AudioFormat { return s.format }

func (s *AudioSession) nextSeq() uint64 { s.seq++; return s.seq }

// terminalErr returns the sticky terminal error, if any.
func (s *AudioSession) terminalErr() error {
	select {
	case <-s.terminal:
		return s.err
	default:
		return nil
	}
}

// fail records the first terminal error, closes the socket immediately (this
// also unblocks in-flight I/O) and returns the recorded error.
func (s *AudioSession) fail(err error) error {
	select {
	case s.failGuard <- struct{}{}:
		s.err = err
		close(s.terminal)
		_ = s.conn.CloseNow()
	default:
		<-s.terminal
	}
	return s.err
}

func (s *AudioSession) failServer(event *pb.ServerEvent) error {
	e := event.GetError()
	if e == nil {
		return s.fail(protocolError("error event has no payload"))
	}
	return s.fail(&AudioServerError{Code: int32(e.GetCode()), Retryable: e.GetRetryable()})
}

func (s *AudioSession) failClosed(event *pb.ServerEvent) error {
	c := event.GetSessionClosed()
	if c == nil {
		return s.fail(protocolError("session.closed has no payload"))
	}
	if s.sessionID != 0 && c.GetSessionId() != 0 && c.GetSessionId() != s.sessionID {
		return s.fail(protocolError("session.closed names a different session"))
	}
	return s.fail(&AudioClosedError{Reason: closeReasonName(c.GetReason())})
}

// ioFailure classifies a socket error. Cancellation closes the socket (that is
// how the WebSocket library unblocks I/O), so it ends the session.
func (s *AudioSession) ioFailure(ctx context.Context, err error) error {
	if terminal := s.terminalErr(); terminal != nil {
		return terminal
	}
	if ctx.Err() != nil {
		s.fail(errAudioInterrupted)
		return ctx.Err()
	}
	if status := websocket.CloseStatus(err); status >= 0 {
		return s.fail(fmt.Errorf("Feishu audio connection closed (status %d)", int(status)))
	}
	return s.fail(errors.New("Feishu audio connection failed"))
}

// next reads one non-skipped server event and enforces frame limits.
func (s *AudioSession) next(ctx context.Context) (*pb.ServerEvent, error) {
	for skipped := 0; ; skipped++ {
		if skipped > maxSkippedFrames {
			return nil, s.fail(protocolError("too many non-event frames"))
		}
		kind, raw, err := s.conn.Read(ctx)
		if err != nil {
			return nil, s.ioFailure(ctx, err)
		}
		if kind != websocket.MessageBinary {
			return nil, s.fail(protocolError("expected a binary message"))
		}
		event, err := decodeAudioFrame(raw)
		if err != nil {
			return nil, s.fail(err)
		}
		if event != nil {
			return event, nil
		}
	}
}

// acquire takes a serialization gate. A terminal session wins over a free gate
// so that callers see the sticky error instead of using a closed socket.
func (s *AudioSession) acquire(ctx context.Context, gate chan struct{}) error {
	if err := s.terminalErr(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := s.terminalErr(); err != nil {
			<-gate
			return err
		}
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.terminal:
		return s.err
	}
}

// ReadAudio returns the next downstream audio chunk. Server error and close
// events, protocol violations and socket failures end the session and are
// returned now and by every later call. Cancelling ctx while waiting for audio
// also ends the session, because the socket must be closed to interrupt the read.
func (s *AudioSession) ReadAudio(ctx context.Context) (AudioFrame, error) {
	if err := s.acquire(ctx, s.readGate); err != nil {
		return AudioFrame{}, err
	}
	defer func() { <-s.readGate }()
	for ignored := 0; ; ignored++ {
		if err := s.terminalErr(); err != nil {
			return AudioFrame{}, err
		}
		if err := ctx.Err(); err != nil {
			return AudioFrame{}, err
		}
		if ignored > maxSkippedFrames {
			return AudioFrame{}, s.fail(protocolError("too many non-audio events"))
		}
		event, err := s.next(ctx)
		if err != nil {
			return AudioFrame{}, err
		}
		switch event.GetType() {
		case "audio.downstream.delta":
			return s.audioFrame(event)
		case "error":
			return AudioFrame{}, s.failServer(event)
		case "session.closed":
			return AudioFrame{}, s.failClosed(event)
		case "session.created":
			return AudioFrame{}, s.fail(protocolError("unexpected repeated session.created"))
		}
		// Unknown event types are ignored for forward compatibility (bounded).
	}
}

func (s *AudioSession) audioFrame(event *pb.ServerEvent) (AudioFrame, error) {
	delta := event.GetAudioDownstreamDelta()
	if delta == nil {
		return AudioFrame{}, s.fail(protocolError("audio event has no payload"))
	}
	if event.GetSessionId() != s.sessionID {
		return AudioFrame{}, s.fail(protocolError("audio event names a different session"))
	}
	format := s.format
	if f := delta.GetFormat(); f != nil {
		if !validFormat(f) {
			return AudioFrame{}, s.fail(protocolError("audio chunk is not 24 kHz mono s16le PCM"))
		}
	}
	pcm := delta.GetDelta()
	if len(pcm) == 0 || len(pcm)%2 != 0 {
		return AudioFrame{}, s.fail(protocolError("audio chunk is empty or not sample aligned"))
	}
	if len(delta.GetTrackId()) > maxAudioLabel || len(delta.GetSource()) > maxAudioLabel {
		return AudioFrame{}, s.fail(protocolError("audio metadata exceeds its limit"))
	}
	return AudioFrame{
		TrackID: delta.GetTrackId(), Source: delta.GetSource(), Format: format,
		PTSMS: delta.GetPtsMs(), DurationMS: delta.GetDurationMs(), PCM: pcm,
		EventID: event.GetEventId(), CreatedAt: event.GetCreatedAt(),
	}, nil
}

// WriteAudio sends signed 16-bit little-endian mono 24 kHz PCM into the meeting.
// Odd byte counts and inputs over 1 MiB are rejected before anything is sent.
// Input is split into ordered chunks of at most 4800 bytes. An empty slice is a
// no-op. Cancelling ctx during socket I/O ends the session and may leave earlier
// chunks of this call already sent; waiting for the write lock is cancelable
// without effect on the session.
func (s *AudioSession) WriteAudio(ctx context.Context, pcm []byte) error {
	if len(pcm)%2 != 0 {
		return errors.New("Feishu audio PCM must contain whole 16-bit samples")
	}
	if len(pcm) > maxWritePCM {
		return errors.New("Feishu audio write exceeds 1 MiB")
	}
	if len(pcm) == 0 {
		return s.terminalErr()
	}
	return s.send(ctx, func(seq func() uint64) ([][]byte, error) {
		var frames [][]byte
		for start := 0; start < len(pcm); start += maxUpstreamChunk {
			end := min(start+maxUpstreamChunk, len(pcm))
			frame, err := encodeAudioFrame(seq(), audioAppendEvent(s.sessionID, pcm[start:end]))
			if err != nil {
				return nil, err
			}
			frames = append(frames, frame)
		}
		return frames, nil
	})
}

// ClearAudio discards upstream audio already queued by the service (barge-in).
func (s *AudioSession) ClearAudio(ctx context.Context) error {
	return s.send(ctx, func(seq func() uint64) ([][]byte, error) {
		frame, err := encodeAudioFrame(seq(), audioClearEvent(s.sessionID, audioID()))
		return [][]byte{frame}, err
	})
}

func (s *AudioSession) send(ctx context.Context, build func(func() uint64) ([][]byte, error)) error {
	if err := s.terminalErr(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.acquire(ctx, s.writeGate); err != nil {
		return err
	}
	defer func() { <-s.writeGate }()
	frames, err := build(s.nextSeq)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err := s.conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
			return s.ioFailure(ctx, err)
		}
	}
	return nil
}

// Close sends a best-effort close notice, bounded by CloseTimeout, then closes
// the socket without waiting for a peer handshake. It is idempotent, does not
// leave the meeting, and unblocks pending reads and writes. The returned error
// reports only an undelivered close notice.
func (s *AudioSession) Close() error {
	select {
	case s.closeGate <- struct{}{}:
	default:
		<-s.done // another Close is in progress or finished
		return s.closeErr
	}
	defer close(s.done)
	if s.terminalErr() != nil {
		s.closeErr = nil
		_ = s.conn.CloseNow()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.closeTimeout)
	defer cancel()
	select {
	case s.writeGate <- struct{}{}:
		frame, err := encodeAudioFrame(s.nextSeq(), sessionCloseEvent(s.sessionID, audioID()))
		if err == nil {
			err = s.conn.Write(ctx, websocket.MessageBinary, frame)
		}
		<-s.writeGate
		if err != nil {
			s.closeErr = errors.New("Feishu audio close notice was not delivered")
		}
	case <-ctx.Done():
		s.closeErr = errors.New("Feishu audio close notice was not delivered")
	}
	s.fail(ErrAudioClosed)
	return s.closeErr
}
