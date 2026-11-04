package nativepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"agent-host/internal/harness"
	"github.com/zhoushoujianwork/offload/pkg/execution"
)

// runCodex consumes native Codex evidence directly; Claude-shaped synthetic
// events would obscure the source protocol and are deliberately not generated.
func (a *Adapter) runCodex(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	result := harness.Result{Status: "failed", ExitCode: -1}
	if req.NativeProfile != "" {
		entries, err := os.ReadDir(filepath.Join(req.NativeProfile, "skills"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if len(entries) > 0 {
			return result, errors.New("Codex Host execution cannot mount configured native Skills yet")
		}
	}
	if err := validateRunDirectories(req.Workspace, req.AdditionalDirectories); err != nil {
		return result, err
	}
	native := &execution.NativeControls{CallerContext: true, PermissionMode: req.PermissionMode, RestrictedReadOnly: req.RestrictedReadOnly, Extraction: req.Extraction, AllowedTools: req.AllowedTools, SkillDirectory: req.SkillPluginDir, JSONSchema: req.JSONSchema, ForkFromSessionID: req.ForkFromNativeSessionID}
	for _, server := range req.MCPServers {
		native.MCPServers = append(native.MCPServers, execution.MCPServer{Name: server.Name, Command: server.Command, Args: server.Args})
	}
	for _, image := range req.Images {
		native.Images = append(native.Images, execution.Image{Name: image.Name, MediaType: image.MediaType, Data: image.Data})
	}
	sdk := execution.Request{CallerRunID: req.CallerRunID, Task: req.Prompt, Directory: req.Workspace, Instructions: []byte(req.HostInstructions), Model: req.Model, AdditionalDirectories: req.AdditionalDirectories, Native: native, ToolsDisabled: req.ToolsDisabled}
	if req.Resume {
		sdk.NativeSessionID = req.NativeSessionID
	}
	observedSession := ""
	sdk.OnEvent = func(raw execution.RawEvent) error {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				ID        string          `json:"id"`
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Server    string          `json:"server"`
				Tool      string          `json:"tool"`
				Arguments json.RawMessage `json:"arguments"`
				Result    json.RawMessage `json:"result"`
				Status    string          `json:"status"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw.Data, &event); err != nil {
			return err
		}
		observation := harness.Observation{Type: "codex." + event.Type, Raw: raw.Data}
		switch event.Type {
		case "thread.started":
			if event.ThreadID == "" || req.Resume && event.ThreadID != req.NativeSessionID || observedSession != "" && observedSession != event.ThreadID {
				return errors.New("Codex reported an incompatible native session")
			}
			observedSession = event.ThreadID
			observation.Type = "init"
			observation.NativeSessionID = event.ThreadID
		case "item.started", "item.completed":
			if event.Item.Type == "mcp_tool_call" {
				stage := "call"
				observation.Type = "tool_start"
				if event.Type == "item.completed" {
					stage = "result"
					observation.Type = "tool_result"
				}
				observation.Tool = &harness.ToolObservation{ID: event.Item.ID, Name: "mcp__" + event.Item.Server + "__" + event.Item.Tool, Stage: stage, Input: event.Item.Arguments, Result: event.Item.Result, IsError: event.Item.Status == "failed"}
			}
			if event.Item.Type == "command_execution" || event.Item.Type == "file_change" || event.Item.Type == "web_search" {
				var envelope struct {
					Item json.RawMessage `json:"item"`
				}
				if err := json.Unmarshal(raw.Data, &envelope); err != nil {
					return err
				}
				stage := "call"
				observation.Type = "tool_start"
				tool := &harness.ToolObservation{ID: event.Item.ID, Name: "codex." + event.Item.Type, Stage: stage, Input: envelope.Item}
				if event.Type == "item.completed" {
					tool.Stage = "result"
					tool.Result = envelope.Item
					tool.IsError = event.Item.Status == "failed"
					observation.Type = "tool_result"
				}
				observation.Tool = tool
			}
			if event.Item.Type == "agent_message" && event.Type == "item.completed" {
				observation.Type = "text_delta"
				observation.Delta = event.Item.Text
			}
		case "turn.completed":
			observation.Type = "result"
		}
		if emit != nil {
			return emit(observation)
		}
		return nil
	}
	r, err := a.library.RunNative(ctx, sdk)
	result.ExitCode, result.Text, result.Model, result.NativeSessionID = r.ExitCode, r.Report, r.ObservedModel, r.NativeSessionID
	result.Evidence = &harness.ExecutionEvidence{RunID: r.RunID, CallerRunID: r.CallerRunID, Profile: r.Profile, Backend: r.Backend, Status: r.Status, ErrorKind: r.ErrorKind, Usage: r.Usage, Raw: r.RawRecord}
	if r.HistoryRecorded {
		result.Evidence.HistoryStatus = "saved"
	} else if r.HistoryError != nil {
		result.Evidence.HistoryStatus = "failed"
		result.Evidence.HistoryError = r.HistoryError.Error()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Status = "canceled"
		}
		result.Error = err.Error()
		return result, err
	}
	if r.NativeSessionID == "" || r.Report == "" {
		result.Status = "unknown"
		return result, errors.New("Codex completed without session or final report")
	}
	result.Status = "completed"
	return result, nil
}
