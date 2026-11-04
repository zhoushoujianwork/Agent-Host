package offloadchannel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type canceledRecord struct {
	Caller      string `json:"caller"`
	CallerRunID string `json:"caller_run_id"`
	Session     string `json:"session"`
	Cleanup     string `json:"cleanup"`
	ErrorKind   string `json:"error_kind"`
	StopReason  string `json:"stop_reason"`
}

func confirmedCanceled(raw []byte, runID, session string) bool {
	var r canceledRecord
	return json.Unmarshal(raw, &r) == nil && r.Caller == "agh" && r.CallerRunID == "agh:"+runID &&
		r.Session != "" && r.Session == session && r.Cleanup == "" && (r.ErrorKind == "canceled" || r.StopReason == "canceled")
}

func canceledBinding(result harness.Result, runID string) bool {
	return result.Evidence != nil && confirmedCanceled(result.Evidence.Raw, runID, result.NativeSessionID)
}

// The exception is explicit and tied to the latest canceled local-owner run.
// Normal channel routing retains SessionCanResume's rejection behavior.
func (h *Host) canResume(ctx context.Context, st *state.Store, id string) error {
	err := st.SessionCanResume(ctx, id)
	if h.Config.ResumeCanceledRun == "" {
		return err
	}
	var cause *state.SessionResumeError
	if !errors.As(err, &cause) || cause.ExecutionStatus != "canceled" || cause.RunID != h.Config.ResumeCanceledRun {
		return errors.New("explicit canceled resume does not match the latest canceled execution")
	}
	last, e := st.LatestSessionRun(ctx, id)
	if e != nil {
		return e
	}
	session, e := st.Session(ctx, id)
	if e != nil {
		return e
	}
	if last.Source != nil || last.Room != nil || last.AudienceID != state.OwnerAudienceID || last.ExecutionStatus != "canceled" {
		return errors.New("only a canceled direct owner chat can use this resume entry point")
	}
	for _, event := range last.Events {
		if event.Type != "offload.result" {
			continue
		}
		var raw []byte
		switch v := event.Data["raw"].(type) {
		case []byte:
			raw = v
		case json.RawMessage:
			raw = v
		case map[string]any:
			raw, _ = json.Marshal(v)
		case string:
			raw, _ = base64.StdEncoding.DecodeString(v)
		}
		if confirmedCanceled(raw, last.ID, session.NativeSessionID) {
			return nil
		}
	}
	return errors.New("canceled execution lacks confirmed native session and cleanup evidence")
}
