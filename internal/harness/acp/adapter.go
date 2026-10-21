package acp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/harness/claude"
	"agent-host/internal/harness/process"
)

type Adapter struct {
	config Config
	native string
}

func New(config Config, native string) *Adapter { return &Adapter{config: config, native: native} }
func (a *Adapter) Identity() harness.Identity {
	return harness.Identity{Agent: "local-claude-acp", Component: "harness.acp", Transport: "acp", Engine: a.config.Engine, Version: a.config.AdapterVersion}
}

type sessionBinding struct {
	Session  string `json:"session_id"`
	Identity string `json:"identity"`
}

func (a *Adapter) binding(req harness.Request) (string, string, error) {
	if req.NativeProfile == "" || !filepath.IsAbs(req.NativeProfile) || req.NativeSessionID == "" {
		return "", "", errors.New("ACP requires an explicit native profile and Host session binding")
	}
	key := sha256.Sum256([]byte(req.NativeSessionID))
	type serverIdentity struct {
		Name, Command string
		Args          []string
		Authority     string `json:",omitempty"`
	}
	servers := make([]serverIdentity, len(req.MCPServers))
	for i, server := range req.MCPServers {
		servers[i] = serverIdentity{Name: server.Name, Command: server.Command, Args: server.Args}
		if server.SessionKey != "" {
			servers[i].Args, servers[i].Authority = nil, server.SessionKey
		}
	}
	identity, _ := json.Marshal(struct {
		Config                                 Config
		Native, Profile, Workspace, Permission string
		ToolsDisabled                          bool
		Allowed, Directories                   []string
		MCP                                    []serverIdentity
	}{a.config, a.native, req.NativeProfile, req.Workspace, req.PermissionMode, req.ToolsDisabled, req.AllowedTools, req.AdditionalDirectories, servers})
	digest := sha256.Sum256(identity)
	return filepath.Join(req.NativeProfile, "agent-host-acp", "sessions", hex.EncodeToString(key[:])+".json"), hex.EncodeToString(digest[:]), nil
}

func saveBinding(path string, value sessionBinding) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// These files are not native transcripts. They only bind a Host-owned key to
	// the opaque ACP identity and must not be redirected through a symlink.
	for parent := dir; filepath.Base(parent) != "agent-host-acp"; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("ACP binding directory must be private and regular")
		}
	}
	info, err := os.Lstat(filepath.Dir(dir))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("ACP binding root must be private and regular")
	}
	raw, _ := json.Marshal(value)
	file, err := os.CreateTemp(dir, ".binding-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	// A new session must never overwrite a binding from an uncertain prior run.
	return os.Link(file.Name(), path)
}

func loadBinding(path, identity string) (sessionBinding, error) {
	var b sessionBinding
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return b, errors.New("compatible ACP session binding is unavailable; start a new conversation")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	if json.Unmarshal(raw, &b) != nil || b.Identity != identity || b.Session == "" || len(b.Session) > 512 || strings.ContainsAny(b.Session, "\x00\r\n") {
		return b, errors.New("ACP session binding is incompatible; start a new conversation")
	}
	return b, nil
}

func (a *Adapter) Run(ctx context.Context, req harness.Request, emit func(harness.Observation) error) (result harness.Result, runErr error) {
	result.Status = "failed"
	defer func() {
		if runErr != nil {
			result.Error = runErr.Error()
			kind := harness.FailureProvider
			if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				result.Status = "canceled"
				kind = harness.FailureCanceled
				if errors.Is(runErr, context.DeadlineExceeded) {
					kind = harness.FailureDeadline
				}
			}
			runErr = &harness.ExecutionError{Kind: kind, Cause: runErr}
		}
	}()
	options, err := claude.New(a.native).ACPOptions(ctx, req, emit)
	if err != nil {
		return result, err
	}
	path, identity, err := a.binding(req)
	if err != nil {
		return result, err
	}
	var previous sessionBinding
	if req.Resume {
		previous, err = loadBinding(path, identity)
		if err != nil {
			return result, err
		}
	} else if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("ACP binding already exists or cannot be inspected; start a new conversation")
	}
	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(childCtx, a.config.Command, a.config.Args...)
	cmd.Dir = req.Workspace
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CLAUDE_CONFIG_DIR" && key != "CLAUDE_CODE_EXECUTABLE" && key != "CLAUDECODE" && key != "CLAUDE_CODE_DISABLE_AUTO_MEMORY" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+req.NativeProfile, "CLAUDE_CODE_EXECUTABLE="+a.native, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return result, err
	}
	// Drain diagnostics without projecting configuration, account data or tokens.
	diagnostic := &diagnostics{}
	cmd.Stderr = diagnostic
	cmd.WaitDelay = 2 * time.Second
	if err = process.Bind(cmd); err != nil {
		input.Close()
		output.Close()
		return result, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return result, fmt.Errorf("start ACP agent: %w", err)
	}
	frameLimit := 16 << 20
	for _, image := range req.Images {
		frameLimit += base64.StdEncoding.EncodedLen(len(image.Data))
	}
	conn := connect(input, output, frameLimit)
	conn.diagnostics = diagnostic
	conn.abort = func() { process.Stop(cmd) }
	p := &projection{tools: map[string]*toolState{}, emit: emit}
	initializedSession := ""
	conn.native = func(raw json.RawMessage) error {
		var frame struct {
			Session string          `json:"sessionId"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			return errors.New("invalid ACP native initialization envelope")
		}
		var header struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Parent  string `json:"parent_tool_use_id"`
		}
		if json.Unmarshal(frame.Message, &header) != nil {
			return errors.New("invalid ACP native initialization body")
		}
		if header.Type != "system" || header.Subtype != "init" || header.Parent != "" {
			return nil
		}
		if frame.Session == "" || (conn.session != "" && frame.Session != conn.session) {
			return errors.New("native initialization has an incompatible ACP session")
		}
		model, projected, validation := claude.ValidateACPInitialization(frame.Message, req.MCPServers)
		if emitErr := p.send(harness.Observation{Type: "native_configuration", NativeSessionID: frame.Session, Model: model, Raw: projected}); emitErr != nil {
			return emitErr
		}
		if validation != nil {
			_, _ = diagnostic.Write(frame.Message)
			return validation
		}
		initializedSession = frame.Session
		p.model = model
		return nil
	}
	defer func() {
		close(conn.done)
		process.Stop(cmd)
		input.Close()
		_ = cmd.Wait()
		if saveErr := diagnostic.save(req.NativeProfile); saveErr != nil {
			runErr = errors.Join(runErr, errors.New("ACP private diagnostic evidence could not be saved"))
		}
	}()
	var initialized struct {
		Protocol int `json:"protocolVersion"`
		Agent    struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
		Capabilities struct {
			Load   bool `json:"loadSession"`
			Prompt struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
			Sessions map[string]json.RawMessage `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	setupCtx, setupCancel := context.WithTimeout(ctx, 45*time.Second)
	defer setupCancel()
	err = conn.call(setupCtx, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "agent-host", "version": "1"}, "clientCapabilities": map[string]any{}}, &initialized)
	if err != nil {
		return result, err
	}
	if initialized.Protocol != 1 || initialized.Agent.Name != "@agentclientprotocol/claude-agent-acp" || initialized.Agent.Version != a.config.AdapterVersion {
		return result, errors.New("ACP agent identity/version does not match the configured supported Claude bridge")
	}
	if len(req.Images) > 0 && !initialized.Capabilities.Prompt.Image {
		return result, errors.New("ACP agent does not support image input")
	}
	if len(req.AdditionalDirectories) > 0 && initialized.Capabilities.Sessions["additionalDirectories"] == nil {
		return result, errors.New("ACP agent does not support required additional directories")
	}
	servers := []map[string]any{}
	for _, server := range req.MCPServers {
		args := server.Args
		if args == nil {
			args = []string{}
		}
		servers = append(servers, map[string]any{"name": server.Name, "command": server.Command, "args": args, "env": []any{}})
	}
	params := map[string]any{"cwd": req.Workspace, "mcpServers": servers, "_meta": map[string]any{"claudeCode": map[string]any{"options": options, "emitRawSDKMessages": []map[string]string{{"type": "system", "subtype": "init"}}}}}
	if len(req.AdditionalDirectories) > 0 {
		params["additionalDirectories"] = req.AdditionalDirectories
	}
	method := "session/new"
	if req.Resume {
		params["sessionId"] = previous.Session
		if initialized.Capabilities.Sessions["resume"] != nil {
			method = "session/resume"
		} else if initialized.Capabilities.Load {
			method = "session/load"
		} else {
			return result, errors.New("ACP agent cannot reconnect to stored sessions")
		}
	}
	var sessionRaw json.RawMessage
	if err = conn.call(setupCtx, method, params, &sessionRaw); err != nil {
		return result, err
	}
	var session struct {
		ID    string `json:"sessionId"`
		Modes struct {
			Available []struct {
				ID string `json:"id"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	if json.Unmarshal(sessionRaw, &session) != nil {
		return result, errors.New("invalid ACP session setup")
	}
	if req.Resume {
		session.ID = previous.Session
	}
	if session.ID == "" || len(session.ID) > 512 || strings.ContainsAny(session.ID, "\x00\r\n") {
		return result, errors.New("ACP did not return a valid native session ID")
	}
	conn.session = session.ID
	if initializedSession != "" && initializedSession != session.ID {
		return result, errors.New("ACP session setup mismatches native initialization")
	}
	conn.permission = req.PermissionMode
	if req.PermissionMode != "" && req.PermissionMode != "default" {
		supported := false
		for _, mode := range session.Modes.Available {
			if mode.ID == req.PermissionMode {
				supported = true
			}
		}
		if !supported {
			return result, errors.New("ACP does not expose the required native permission mode")
		}
		if err = conn.call(setupCtx, "session/set_mode", map[string]string{"sessionId": session.ID, "modeId": req.PermissionMode}, nil); err != nil {
			return result, err
		}
	}
	if !req.Resume {
		if err = saveBinding(path, sessionBinding{Session: session.ID, Identity: identity}); err != nil {
			return result, err
		}
	}
	p.session = session.ID
	if p.model == "" {
		p.model = currentModel(sessionRaw)
	}
	raw, _ := json.Marshal(map[string]any{"protocol": "acp", "engine": a.config.Engine, "adapter_version": a.config.AdapterVersion, "session_id": session.ID, "host_native_binding": req.NativeSessionID, "resumed": req.Resume})
	if err = p.send(harness.Observation{Type: "init", Model: p.model, NativeSessionID: session.ID, Raw: raw}); err != nil {
		return result, err
	}
	blocks := []map[string]string{{"type": "text", "text": req.Prompt}}
	for _, image := range req.Images {
		blocks = append(blocks, map[string]string{"type": "image", "mimeType": image.MediaType, "data": base64.StdEncoding.EncodeToString(image.Data)})
	}
	conn.update = func(raw json.RawMessage) error {
		var header struct {
			Kind string `json:"sessionUpdate"`
		}
		_ = json.Unmarshal(raw, &header)
		if initializedSession != session.ID && (header.Kind == "agent_message_chunk" || header.Kind == "tool_call" || header.Kind == "tool_call_update") {
			return errors.New("ACP business output arrived without admitted native initialization")
		}
		return p.update(raw)
	}
	conn.prompting = true
	var response struct {
		Stop string `json:"stopReason"`
	}
	result.Status = "unknown"
	err = conn.call(ctx, "session/prompt", map[string]any{"sessionId": session.ID, "prompt": blocks}, &response)
	if err != nil && ctx.Err() != nil {
		err = errors.Join(ctx.Err(), err)
	}
	result.Text, result.Model, result.NativeSessionID = p.text, p.model, session.ID
	if err != nil {
		return result, err
	}
	if initializedSession != session.ID {
		return result, errors.New("ACP prompt returned without admitted native initialization")
	}
	raw, _ = json.Marshal(map[string]any{"protocol": "acp", "session_id": session.ID, "stopReason": response.Stop})
	if err = p.send(harness.Observation{Type: "result", Model: p.model, NativeSessionID: session.ID, Raw: raw}); err != nil {
		return result, err
	}
	switch response.Stop {
	case "end_turn":
		result.Status = "completed"
		return result, nil
	case "cancelled":
		result.Status = "canceled"
		return result, context.Canceled
	default:
		result.Status = "failed"
		return result, fmt.Errorf("ACP prompt ended without completion (%s)", response.Stop)
	}
}
