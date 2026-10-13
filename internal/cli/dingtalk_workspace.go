package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"agent-host/internal/dingtalkworkspace"
	"agent-host/internal/harness"
)

const dingtalkWorkspaceUsage = `Built-in DingTalk personal workspace (official DWS package; no dws executable).

  agent-host dingtalk-workspace check --config ABSOLUTE_PATH
  agent-host dingtalk-workspace login --config ABSOLUTE_PATH [--device]
  agent-host dingtalk-workspace status --config ABSOLUTE_PATH
  agent-host chat --dingtalk-workspace-config ABSOLUTE_PATH [options]

Configuration: exact profile corpId:userId, private state_directory and services.
DingTalk channel owner tasks automatically mount this MCP. workspace_config in
the bot JSON selects the file; default is data-dingtalk-workspace.json beside bot JSON.
Group tasks do not receive the owner's workspace. check is local-only.
login uses official browser/device authorization and encrypted credential storage.
status reads the configured profile; it never sends messages or runs tasks.
Unknown write outcomes must be inspected, never automatically replayed.
`

func runDingTalkWorkspace(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, dingtalkWorkspaceUsage)
		return err
	}
	command := args[0]
	if command != "check" && command != "login" && command != "status" && command != "serve" {
		return errors.New("dingtalk-workspace requires check, login or status")
	}
	fs := flag.NewFlagSet("dingtalk-workspace "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var path, fp, profile string
	var device bool
	fs.StringVar(&path, "config", "", "Private DingTalk workspace configuration")
	if command == "login" {
		fs.BoolVar(&device, "device", false, "Use official device authorization")
	}
	if command == "serve" {
		fs.StringVar(&fp, "fingerprint", "", "Pinned binding")
		fs.StringVar(&profile, "profile", "", "Expected bot owner identity")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if path == "" || fs.NArg() != 0 {
		return errors.New("provide --config and documented options only")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if command == "serve" {
		return dingtalkworkspace.Serve(ctx, dingtalkworkspace.Service{Path: path, Fingerprint: fp, ExpectedProfile: profile, Run: dingtalkworkspace.Process(executable)}, in, out)
	}
	c, binding, err := dingtalkworkspace.Load(path)
	if err != nil {
		return err
	}
	if command == "check" {
		return json.NewEncoder(out).Encode(map[string]any{"status": "local_configuration_valid", "profile": c.Profile, "services": c.Services, "official_version": dingtalkworkspace.Version, "authentication_verified": false})
	}
	if command == "login" {
		return dingtalkworkspace.Login(ctx, executable, path, binding, device, out, errout)
	}
	raw, err := dingtalkworkspace.Process(executable)(ctx, dingtalkworkspace.Request{Config: path, Fingerprint: binding, Args: []string{"auth", "status", "--profile", c.Profile, "--readonly", "--format", "json"}})
	if err != nil {
		return err
	}
	_, err = out.Write(append(raw, '\n'))
	return err
}

func resolveDingTalkWorkspace(o *options) error {
	o.dingtalkWorkspaceFingerprint = ""
	if o.noTools || o.audienceID != "" && o.audienceID != "owner" {
		return nil
	}
	path := o.dingtalkWorkspaceConfig
	profile := ""
	if o.dingtalkConfig != "" {
		bot, err := loadBotConfig(o.dingtalkConfig)
		if err != nil {
			return err
		}
		profile = bot.CorpID + ":" + bot.OwnerUserID
		if path == "" {
			path = bot.WorkspaceConfig
		}
		if path == "" {
			path = filepath.Join(filepath.Dir(o.dingtalkConfig), "data-dingtalk-workspace.json")
		}
	}
	if path == "" {
		return nil
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	_, fp, _, err := dingtalkworkspace.Binding(path, profile)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	o.dingtalkWorkspaceFingerprint = fp
	o.mcpServers = append(o.mcpServers, harness.MCPServer{Name: "dingtalk", Command: executable, Args: []string{"dingtalk-workspace", "serve", "--config", path, "--fingerprint", fp, "--profile", profile}})
	return nil
}
