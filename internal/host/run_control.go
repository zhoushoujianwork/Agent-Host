package host

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Each execution owns its endpoint. No process IDs, service-wide signals or
// shared runtime locks are used to cancel a sibling CLI/channel execution.
func startRunControl(home, runID string, cancel context.CancelFunc) (func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := ID() + ID()
	var stopping atomic.Bool
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Run-Token")), []byte(token)) != 1 {
			http.Error(w, "invalid run token", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/stop" || r.Method != http.MethodPost {
			http.Error(w, "use POST /stop", http.StatusMethodNotAllowed)
			return
		}
		if stopping.CompareAndSwap(false, true) {
			cancel()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": runID, "status": "stopping"})
	})
	path := filepath.Join(home, ".run-control-"+runID+".json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	err = json.NewEncoder(file).Encode(map[string]any{"version": 1, "run_id": runID, "port": listener.Addr().(*net.TCPAddr).Port, "token": token})
	err = errors.Join(err, file.Close())
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	go func() { _ = server.Serve(listener) }()
	return func() {
		_ = os.Remove(path)
		ctx, done := context.WithTimeout(context.Background(), 4*time.Second)
		defer done()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
		}
	}, nil
}
