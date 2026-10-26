package doubao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coder/websocket"
)

type stream struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	errs   chan error
	stop   func() bool
}

func newStream(ctx context.Context, conn *websocket.Conn) *stream {
	ctx, cancel := context.WithCancel(ctx)
	s := &stream{conn: conn, ctx: ctx, cancel: cancel, done: make(chan struct{}), errs: make(chan error, 1)}
	// Also closes the socket while receive is blocked on an output channel.
	s.stop = context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
	return s
}

func (s *stream) Errors() <-chan error { return s.errs }
func (s *stream) Close() error {
	s.cancel()
	_ = s.conn.CloseNow()
	<-s.done
	return nil
}
func (s *stream) finish() {
	s.cancel()
	_ = s.conn.CloseNow()
	s.stop()
	close(s.errs)
	close(s.done)
}
func (s *stream) fail(err error) {
	if s.ctx.Err() == nil {
		s.errs <- err
	}
}
func (s *stream) read() (frame, error) {
	typ, b, err := s.conn.Read(s.ctx)
	if err != nil {
		return frame{}, errors.New("doubao: socket closed before provider final frame")
	}
	if typ != websocket.MessageBinary {
		return frame{}, errors.New("doubao: expected binary message")
	}
	f, err := decode(b)
	if err != nil {
		return frame{}, err
	}
	payload, err := decompress(f.payload, f.compression)
	if err != nil {
		return frame{}, err
	}
	f.payload = payload
	if f.kind == errorResponse {
		return frame{}, fmt.Errorf("doubao: provider error code %d", f.code)
	}
	return f, nil
}

func marshalRequest(request any) ([]byte, error) {
	b, err := json.Marshal(request)
	if err != nil || len(b) > maxMessage-32 {
		return nil, errors.New("doubao: initial request exceeds limit or is invalid")
	}
	payload, err := compress(b)
	if err != nil || len(payload) > maxMessage-32 {
		return nil, errors.New("doubao: initial request exceeds limit or is invalid")
	}
	return payload, nil
}
