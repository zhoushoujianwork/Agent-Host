package doubao

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// A real HTTP server and WebSocket handshake over net.Pipe avoid needing
// loopback-listener privileges in restricted developer/test environments.
// These tests are intentionally not parallel: the production client uses the
// standard default HTTP transport, replaced only for the lifetime of a fixture.
func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	previous := http.DefaultTransport
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
	http.DefaultTransport = transport
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(func() { server.Close(); transport.CloseIdleConnections(); http.DefaultTransport = previous })
	return server
}

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
