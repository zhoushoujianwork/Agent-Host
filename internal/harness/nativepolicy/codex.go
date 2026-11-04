package nativepolicy

import (
	"context"
	"errors"
	"fmt"

	"agent-host/internal/harness"
	"github.com/zhoushoujianwork/offload/pkg/execution"
)

// runCodex consumes Codex JSON-RPC evidence directly. It never fabricates
// Claude or legacy exec events from the app-server protocol.
func (a *Adapter) runCodex(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	result := harness.Result{Status: "failed", ExitCode: -1}
	if req.ToolsDisabled || req.Extraction || req.RestrictedReadOnly {
		return result, errors.New("Codex app-server cannot satisfy strict tool-free or restricted execution")
	}
	if err := validateRunDirectories(req.Workspace, req.AdditionalDirectories); err != nil {
		return result, err
	}
	var skillManifest *harness.SkillManifest
	if req.SkillPluginDir != "" {
		manifest, err := validateSkillPlugin(req.SkillPluginDir, req.SkillPluginDigest)
		if err != nil {
			return result, err
		}
		skillManifest = &manifest
	}
	environment, cleanupEnvironment, err := executionEnvironment(req)
	if err != nil {
		return result, err
	}
	defer cleanupEnvironment()
	taskPackage := req.SkillPluginDir
	var skillMount *harness.SkillMount
	if taskPackage != "" {
		taskPackage, err = mountTaskSkillPackage(environment.Root, taskPackage, req.SkillPluginDigest)
		if err != nil {
			return result, err
		}
		skillMount = &harness.SkillMount{Digest: req.SkillPluginDigest, Directory: taskPackage}
	}
	skillRoots, err := approvedSkillRoots(req.NativeProfile, taskPackage)
	if err != nil {
		return result, err
	}
	if emit != nil && len(skillRoots) > 0 {
		provided, message := any(map[string]any{"roots": skillRoots}), "provided Host-selected native Skills"
		if skillManifest != nil {
			provided, message = *skillManifest, "provided task-scoped native Skills"
		}
		if err = emit(harness.Observation{Type: "skills_provided", Message: message, Raw: mustJSON(provided), SkillMount: skillMount}); err != nil {
			return result, err
		}
	}
	native := &execution.NativeControls{CallerContext: true, CodexAppServer: true, PermissionMode: req.PermissionMode, JSONSchema: req.JSONSchema, AllowedTools: append([]string(nil), req.AllowedTools...), SkillRoots: skillRoots}
	for _, server := range req.MCPServers {
		native.MCPServers = append(native.MCPServers, execution.MCPServer{Name: server.Name, Command: server.Command, Args: append([]string(nil), server.Args...)})
	}
	for _, image := range req.Images {
		native.Images = append(native.Images, execution.Image{Name: image.Name, MediaType: image.MediaType, Data: append([]byte(nil), image.Data...)})
	}
	instructions := req.HostInstructions + "\n\nThe Host task working directory is " + req.Workspace + ". Treat the isolated native cwd as private runtime state, not as task scope or source evidence."
	resourceRoots := append([]string{req.Workspace}, req.AdditionalDirectories...)
	sdk := execution.Request{CallerRunID: req.CallerRunID, Task: req.Prompt, Directory: environment.Workspace, EnvironmentDirectory: environment.Root, Instructions: []byte(instructions), Model: req.Model, AdditionalDirectories: resourceRoots, Native: native}
	if req.Resume {
		sdk.NativeSessionID = req.NativeSessionID
	}
	decoder := codexRPCDecoder{expectedSession: req.NativeSessionID, resume: req.Resume, emit: emit}
	sdk.OnEvent = func(raw execution.RawEvent) error {
		if raw.NativeProtocol != "codex-app-server" {
			return fmt.Errorf("Codex returned incompatible native protocol %q", raw.NativeProtocol)
		}
		return decoder.consume(raw.Data)
	}
	r, err := a.library.RunNative(ctx, sdk)
	result.ExitCode, result.Text, result.Model, result.NativeSessionID = r.ExitCode, r.Report, r.ObservedModel, r.NativeSessionID
	result.StructuredOutput = r.StructuredOutput
	result.Evidence = &harness.ExecutionEvidence{RunID: r.RunID, CallerRunID: r.CallerRunID, Profile: r.Profile, Backend: r.Backend, NativeProtocol: r.NativeProtocol, Status: r.Status, ErrorKind: r.ErrorKind, Usage: r.Usage, Raw: r.RawRecord}
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
	if r.NativeProtocol != "codex-app-server" || r.NativeSessionID == "" || r.Report == "" || !decoder.terminal {
		result.Status = "unknown"
		return result, errors.New("Codex app-server completed without compatible session, terminal event or final report")
	}
	if req.Resume && r.NativeSessionID != req.NativeSessionID {
		result.Status = "unknown"
		return result, errors.New("Codex app-server resumed a different native thread")
	}
	result.Status = "completed"
	return result, nil
}
