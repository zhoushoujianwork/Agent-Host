package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

type packet struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type received struct {
	packet packet
	err    error
}
type connection struct {
	native      func(json.RawMessage) error
	diagnostics io.Writer
	abort       func()
	input       io.Writer
	frames      chan received
	done        chan struct{}
	mu          sync.Mutex
	seq         int
	session     string
	prompting   bool
	permission  string
	update      func(json.RawMessage) error
}

func connect(input io.Writer, output io.Reader, limit int) *connection {
	c := &connection{input: input, frames: make(chan received), done: make(chan struct{})}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 64<<10), limit)
		for scanner.Scan() {
			var p packet
			err := json.Unmarshal(scanner.Bytes(), &p)
			if err != nil {
				err = errors.New("invalid ACP JSON-RPC frame")
			}
			select {
			case c.frames <- received{p, err}:
			case <-c.done:
				return
			}
			if err != nil {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case c.frames <- received{err: err}:
		case <-c.done:
		}
	}()
	return c
}

func (c *connection) write(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = c.input.Write(raw)
	return err
}

func (c *connection) call(ctx context.Context, method string, params any, result any) error {
	callDone := make(chan struct{})
	stopWatch := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			if c.abort != nil {
				c.abort()
			}
		case <-callDone:
		}
	})
	defer func() { close(callDone); stopWatch() }()
	c.seq++
	id := c.seq
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("write ACP request: %w", err)
	}
	canceled := ctx.Done()
	var grace <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-canceled:
			canceled = nil
			if !c.prompting || c.session == "" {
				return ctx.Err()
			}
			if err := c.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": c.session}}); err != nil {
				return errors.Join(ctx.Err(), err)
			}
			timer = time.NewTimer(3 * time.Second)
			grace = timer.C
		case <-grace:
			return ctx.Err()
		case r := <-c.frames:
			if r.err != nil {
				return fmt.Errorf("ACP stream ended before %s response: %w", method, r.err)
			}
			p := r.packet
			if p.Method != "" {
				if len(p.ID) > 0 {
					if err := c.clientRequest(p, ctx.Err() != nil); err != nil {
						return err
					}
				} else if p.Method == "_claude/sdkMessage" && c.native != nil {
					if err := c.native(p.Params); err != nil {
						return err
					}
				} else if p.Method == "session/update" && c.prompting {
					var n struct {
						SessionID string          `json:"sessionId"`
						Update    json.RawMessage `json:"update"`
					}
					if json.Unmarshal(p.Params, &n) != nil || n.SessionID != c.session {
						return errors.New("ACP update has an incompatible session binding")
					}
					if c.update != nil {
						if err := c.update(n.Update); err != nil {
							return err
						}
					}
				}
				continue
			}
			if string(p.ID) != fmt.Sprint(id) {
				return errors.New("ACP response has an unexpected request ID")
			}
			if p.Error != nil {
				if c.diagnostics != nil {
					raw, _ := json.Marshal(map[string]any{"source": "acp_rpc_error", "method": method, "error": p.Error})
					_, _ = c.diagnostics.Write(append(raw, '\n'))
				}
				return fmt.Errorf("ACP %s failed (code %d)", method, p.Error.Code)
			}
			if len(p.Result) == 0 {
				return errors.New("ACP response has no result")
			}
			if result != nil && json.Unmarshal(p.Result, result) != nil {
				return errors.New("invalid ACP result payload")
			}
			return nil
		}
	}
}

func (c *connection) clientRequest(p packet, canceled bool) error {
	if p.Method != "session/request_permission" {
		_ = c.write(map[string]any{"jsonrpc": "2.0", "id": p.ID, "error": map[string]any{"code": -32601, "message": "Host does not support this client method"}})
		return errors.New("ACP agent requested an unsupported client capability")
	}
	var request struct {
		SessionID string `json:"sessionId"`
		Options   []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(p.Params, &request) != nil || request.SessionID != c.session {
		return errors.New("ACP permission request has an incompatible session binding")
	}
	outcome := map[string]string{"outcome": "cancelled"}
	if !canceled && c.permission == "bypassPermissions" {
		for _, option := range request.Options {
			if option.Kind == "allow_once" {
				outcome = map[string]string{"outcome": "selected", "optionId": option.ID}
				break
			}
		}
	}
	if c.update != nil {
		raw, _ := json.Marshal(map[string]any{"sessionUpdate": "permission_request", "request": safeEvidence(p.Params), "outcome": outcome})
		if err := c.update(raw); err != nil {
			_ = c.write(map[string]any{"jsonrpc": "2.0", "id": p.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
			return err
		}
	}
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": p.ID, "result": map[string]any{"outcome": outcome}}); err != nil {
		return err
	}
	if outcome["outcome"] == "cancelled" && !canceled {
		return errors.New("ACP tool permission requires an interactive approval path; this entry has none")
	}
	return nil
}
