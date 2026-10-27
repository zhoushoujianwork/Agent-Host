package experience

import "fmt"

// LimitError rejects the entire payload before model processing. Original
// evidence remains owned by the caller; no partial candidate may be published.
type LimitError struct {
	Stage       string `json:"stage"`
	ActualBytes int    `json:"actual_bytes"`
	LimitBytes  int    `json:"limit_bytes"`
	Unprocessed string `json:"unprocessed"`
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("experience %s exceeds %d bytes (actual %d); no truncation performed; entire payload unprocessed; retained evidence requires explicit post-processing review", e.Stage, e.LimitBytes, e.ActualBytes)
}

func payloadLimit(stage string, actual, limit int) *LimitError {
	return &LimitError{Stage: stage, ActualBytes: actual, LimitBytes: limit, Unprocessed: "entire_payload"}
}
