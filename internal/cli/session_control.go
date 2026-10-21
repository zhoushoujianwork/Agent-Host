package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/event"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

// The private service socket executes Web turns in the owning process. It
// neither fabricates incoming IM messages nor sends replies to channel members.
type sessionControl struct {
	store   *state.Store
	resolve func(context.Context, state.Run) (*host.Host, error)
	timeout time.Duration
	slots   chan struct{}
	mu      sync.Mutex
	closed  bool
	workers sync.WaitGroup
}

func runSessionTakeover(ctx context.Context, h *host.Host, parent state.Session, requestID, text string, sink event.Sink, lease ...func(string) error) error {
	session, err := h.TakeoverSession(ctx, parent, requestID, text)
	if err != nil {
		return err
	}
	if len(lease) > 0 {
		if err := lease[0](session.ID); err != nil {
			return err
		}
	}
	// The session's atomic run claim prevents replay even across process death.
	previous, err := h.Store.WithPartition(session.AgentID, session.AudienceID).LatestSessionRun(ctx, session.ID)
	if err == nil {
		for _, e := range previous.Events {
			if err := sink(e); err != nil {
				return err
			}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = h.Run(ctx, session, text)
	return err
}

func (s *sessionControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, message string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		fail(503, "session service is stopping")
		return
	}
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	if r.Method != http.MethodPost {
		fail(405, "session continuation requires POST")
		return
	}
	var a struct {
		Run     string `json:"run_id"`
		Request string `json:"request_id"`
		Message string `json:"message"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.Run == "" || len(a.Run) > 128 || a.Request == "" || len(a.Request) > 200 || len(a.Message) == 0 || len(a.Message) > 16*1024 {
		fail(400, "provide a bounded session continuation")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(409, "Web continuation capacity is busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	run, err := s.store.ContextRun(ctx, a.Run)
	if err != nil {
		fail(404, "source run unavailable")
		return
	}
	h, err := s.resolve(ctx, run)
	if err != nil {
		fail(409, "original session runtime is unavailable or changed; no execution started")
		return
	}
	parent, err := h.Store.WithPartition(run.AgentID, run.AudienceID).Session(ctx, run.SessionID)
	if err != nil {
		fail(409, "source session unavailable")
		return
	}
	// Source validation happens before headers so a busy/changed source is an
	// explicit conflict. The second lookup is idempotent inside runSessionTakeover.
	if _, err := h.TakeoverSession(ctx, parent, a.Request, a.Message); err != nil {
		fail(409, err.Error())
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(200)
	controller := http.NewResponseController(w)
	sink := func(e event.Event) error {
		if err := json.NewEncoder(w).Encode(e); err != nil {
			return err
		}
		return controller.Flush()
	}
	h.Sink = sink
	h.Config.PrepareFeedback, h.Config.BindFeedback = nil, nil
	if err := runSessionTakeover(ctx, h, parent, a.Request, a.Message, sink); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Session execution interrupted or failed; inspect the recorded outcome before retrying."})
		_ = controller.Flush()
	}
}

func resolveSessionTakeover(ctx context.Context, service *channel.Service, agents agentCatalog, base options, client *jev.Client, run state.Run) (*host.Host, error) {
	if service == nil {
		return nil, errors.New("channel service is starting")
	}
	store := service.Store.WithPartition(run.AgentID, run.AudienceID)
	origin := run
	for depth := 0; origin.Source == nil && depth < 64; depth++ {
		if origin.Room == nil || origin.Room.ParentSessionID == "" {
			return nil, errors.New("missing original channel provenance")
		}
		var err error
		origin, err = store.LatestSessionRun(ctx, origin.Room.ParentSessionID)
		if err != nil {
			return nil, err
		}
	}
	if origin.Source == nil {
		return nil, errors.New("channel provenance exceeds bound")
	}
	message, err := service.Store.ChannelMessage(ctx, origin.Source.ChannelMessageID)
	if err != nil || message.RunID != origin.ID || message.SessionID != origin.SessionID {
		return nil, errors.New("channel source receipt mismatch")
	}
	child, err := resolveAgentChannel(ctx, service, agents, base, client, nil, message)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(run.Projects))
	for _, p := range run.Projects {
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 && run.ProjectID != "" {
		ids = append(ids, run.ProjectID)
	}
	resolved, _, err := child.ResolveProjects(ctx, ids, run.ProjectID)
	if err != nil {
		return nil, err
	}
	h, ok := resolved.(*host.Host)
	if !ok {
		return nil, errors.New("channel runtime cannot continue sessions")
	}
	return h, nil
}
