// Package event defines the renderer-independent execution event protocol.
package event

import "time"

const Version = 1

// Event is an append-only observation. Seq is assigned by the durable journal.
// Delta carries output text; Message describes work without being model output.
type Event struct {
	Version   int            `json:"version"`
	Seq       int64          `json:"seq"`
	At        time.Time      `json:"at"`
	SessionID string         `json:"session_id"`
	RunID     string         `json:"run_id"`
	Type      string         `json:"type"`
	Phase     string         `json:"phase"`
	Component string         `json:"component"`
	Status    string         `json:"status,omitempty"`
	Agent     string         `json:"agent,omitempty"`
	Model     string         `json:"model,omitempty"`
	Message   string         `json:"message,omitempty"`
	Delta     string         `json:"delta,omitempty"`
	ElapsedMS int64          `json:"elapsed_ms"`
	Data      map[string]any `json:"data,omitempty"`
}

type Sink func(Event) error
