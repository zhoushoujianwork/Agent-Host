package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

// The managed Host remains the sole database writer. The browser projection
// connects through an owner-private socket and never receives model credentials.
type memoryControl struct {
	external    *externalMemoryControl
	store       *state.Store
	publish     func(context.Context, state.Candidate, string) (state.Memory, error)
	restructure func(context.Context, state.Memory, string) (state.Memory, error)
	mu          sync.Mutex
}

type memoryAction struct {
	Action   string `json:"action"`
	ID       string `json:"id"`
	Agent    string `json:"agent_id"`
	Audience string `json:"audience_id"`
	Note     string `json:"note"`
	Revision int    `json:"revision"`
}

func (m *memoryControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/external-memory" && m.external != nil {
		m.external.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, err error) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	if r.Method != http.MethodPost || r.URL.Path != "/memory-action" {
		fail(405, errors.New("unsupported memory operation"))
		return
	}
	var a memoryAction
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		fail(400, errors.New("invalid memory action"))
		return
	}
	if d.Decode(new(any)) != io.EOF || a.ID == "" || a.Agent == "" || a.Audience != "owner" || len(a.ID) > 128 || len(a.Agent) > 128 || len(a.Note) > 2000 {
		fail(400, errors.New("provide a bounded owner memory action"))
		return
	}
	if a.Action != "accept" && a.Action != "reject" && a.Action != "invalidate" && a.Action != "restructure" {
		fail(400, errors.New("unknown memory action"))
		return
	}
	if (a.Action == "reject" || a.Action == "invalidate") && strings.TrimSpace(a.Note) == "" {
		fail(400, errors.New("provide a reason for rejection or disabling"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.store.WithPartition(a.Agent, "owner")
	var result any
	var err error
	if a.Action == "invalidate" || a.Action == "restructure" {
		if a.Revision < 1 {
			fail(400, errors.New("provide the displayed memory revision"))
			return
		}
		if a.Action == "restructure" {
			var current state.Memory
			current, err = s.Memory(ctx, a.ID)
			if err == nil && (current.Revision != a.Revision || m.restructure == nil) {
				err = errors.New("experience changed or restructuring is unavailable")
			}
			if err == nil {
				result, err = m.restructure(ctx, current, a.Note)
			}
		} else {
			err = s.InvalidateMemoryRevision(ctx, a.ID, a.Note, a.Revision)
			if err == nil {
				result, err = s.Memory(ctx, a.ID)
			}
		}
	} else {
		var c state.Candidate
		c, err = s.GetCandidate(ctx, a.ID)
		if err == nil && c.Status != "pending" {
			err = errors.New("candidate changed; refresh before acting")
		}
		if err == nil {
			var run state.Run
			run, err = s.Run(ctx, c.RunID)
			finished := false
			for _, e := range run.Events {
				finished = finished || e.Type == "run.finished"
			}
			if err == nil && !finished {
				err = errors.New("source run is still finalizing; refresh before acting")
			}
		}
		if err == nil && a.Action == "accept" {
			if strings.TrimSpace(a.Note) == "" {
				a.Note = "Published through the local memory workbench"
			}
			result, err = m.publish(ctx, c, a.Note)
		} else if err == nil {
			err = s.RejectCandidate(ctx, a.ID, a.Note)
			if err == nil {
				result, err = s.GetCandidate(ctx, a.ID)
			}
		}
	}
	if err != nil {
		fail(409, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func startMemoryControl(ctx context.Context, home string, control *memoryControl) (func(), error) {
	path := filepath.Join(home, "memory-control.sock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("memory control path is not a socket")
		}
		// The caller already owns the runtime lock, so this can only be stale.
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	server := &http.Server{Handler: control, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	return func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		<-done
	}, nil
}

func startManagedMemoryControl(ctx context.Context, store *state.Store, o options, client *jev.Client) (func(), error) {
	external := newExternalMemoryControl(store, o, client)
	stop, err := startMemoryControl(ctx, o.home, &memoryControl{external: external, store: store, publish: func(ctx context.Context, c state.Candidate, note string) (state.Memory, error) {
		selected := o
		selected.agentID, selected.audienceID, selected.scope = c.AgentID, "owner", c.Scope
		return composeHost(store, selected, client, nil).AcceptExperience(ctx, c.ID, note, nil)
	}, restructure: func(ctx context.Context, m state.Memory, note string) (state.Memory, error) {
		selected := o
		selected.agentID, selected.audienceID, selected.scope = m.AgentID, "owner", m.Scope
		return composeHost(store, selected, client, nil).RestructureExperience(ctx, m.ID, m.Revision, note)
	}})
	if err != nil {
		return nil, err
	}
	stopExternal := external.start(ctx)
	return func() { stop(); stopExternal() }, nil
}
