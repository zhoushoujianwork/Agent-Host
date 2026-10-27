package channel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"agent-host/internal/runcompletion"
	"agent-host/internal/state"
)

// earlyReply is the per-message state shared by the Host execution-ready
// handler and the post-run tail. The handler is the only place that decides to
// make the one early delivery attempt; the tail never repeats it, even when the
// attempt failed or its outcome is unknown.
type earlyReply struct {
	mu        sync.Mutex
	notice    string
	attempted bool
	err       error
}

type earlyReplyKey struct{}

func withEarlyReply(ctx context.Context, e *earlyReply) context.Context {
	return context.WithValue(ctx, earlyReplyKey{}, e)
}

// setReplyNotice carries routing/session-renewal notices to the early handler
// without changing the Host interface. It is a no-op without an early reply.
func setReplyNotice(ctx context.Context, notice string) {
	if e, _ := ctx.Value(earlyReplyKey{}).(*earlyReply); e != nil {
		e.mu.Lock()
		e.notice = notice
		e.mu.Unlock()
	}
}

func (e *earlyReply) started() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempted
}

func (e *earlyReply) failure() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// handler records successful business processing and attempts the reply before
// Host memory and experience closeout. It never retries and never touches the
// native conversation. Hosts that do not support the callback never call it, so
// the legacy post-run path remains the fallback.
func (s *Service) earlyReplyHandler(m state.ChannelMessage, e *earlyReply) runcompletion.Handler {
	return func(_ context.Context, run state.Run) error {
		if run.ExecutionStatus != "completed" {
			return nil
		}
		if run.Source == nil || run.Source.ChannelMessageID != m.ID {
			return errors.New("execution-ready run does not belong to this channel message")
		}
		e.mu.Lock()
		if e.attempted {
			e.mu.Unlock()
			return nil
		}
		e.attempted = true
		notice := e.notice
		e.mu.Unlock()
		reply := resultTextWithNotice(run, nil, notice, replyBodyBudget(m))
		err := s.finishAndDeliver(m, "completed", "", reply, false, run.ID)
		e.mu.Lock()
		e.err = err
		e.mu.Unlock()
		return err
	}
}

// closeoutFailed keeps a post-delivery closeout failure visible without
// rewriting the already recorded message outcome or sending a second answer.
func (s *Service) closeoutFailed(m state.ChannelMessage, runID string, cause error) error {
	return s.observe("channel.closeout", fmt.Sprintf("message=%s run=%s status=failed error=%q", m.ID, runID, cause.Error()))
}

// finishAndDeliver is the shared tail: record the message outcome, commit the
// exact reply text, then make one delivery attempt. Outcome recording and the
// attempt must remain possible after a turn timeout, so it owns its context.
func (s *Service) finishAndDeliver(m state.ChannelMessage, status, cause, reply string, waiting bool, runID string) error {
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer finishCancel()
	if !waiting {
		if err := s.Store.FinishChannelMessage(finishCtx, m.ID, status, cause); err != nil {
			return fmt.Errorf("finish channel input: %w", err)
		}
	}
	if err := s.observe("channel.processed", fmt.Sprintf("message=%s status=%s run=%s", m.ID, status, runID)); err != nil {
		return err
	}
	message := replyForMessage(m, reply)
	if m.ReplyTarget != nil {
		message.Text = apiReplyText(reply, replyBodyBudget(m))
	}
	if err := s.Store.PrepareChannelReply(finishCtx, m.ID, message.Text); err != nil {
		return fmt.Errorf("prepare channel reply: %w", err)
	}
	return s.deliverChannelReply(finishCtx, m, message)
}
