package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"

	"agent-host/internal/componentconfig"
	"agent-host/internal/email"
	"agent-host/internal/harness"
)

const emailUsage = `Managed multi-account email MCP (Gmail API and personal 163 IMAP/SMTP).

  agent-host email check --config ABSOLUTE_PATH
  agent-host email check --config ABSOLUTE_PATH --account ACCOUNT_ID --live
  agent-host email serve --config ABSOLUTE_PATH --fingerprint FINGERPRINT
  agent-host chat --email-config ABSOLUTE_PATH [options]

check is local-only unless --live is supplied. Live checks never send mail.
Configuration and credential files must be owner-private. Register one unified
MCP for tasks; enabled datasource plugins are global. AI lists accounts and
supplies account_id to tools.
serve is an internal stdio entry point. Sending requires send_enabled and an
operation_id; pending/unknown submission outcomes are never automatically retried.
`

func runEmail(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, e := io.WriteString(out, emailUsage)
		return e
	}
	command := args[0]
	if command != "check" && command != "serve" {
		return errors.New("email requires check or serve")
	}
	fs := flag.NewFlagSet("email "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var path, account, fingerprint string
	var live bool
	fs.StringVar(&path, "config", "", "Private email configuration")
	if command == "serve" {
		fs.StringVar(&fingerprint, "fingerprint", "", "Pinned configuration")
	} else {
		fs.StringVar(&account, "account", "", "Account ID for live check")
		fs.BoolVar(&live, "live", false, "Check remote authentication without reading or sending messages")
	}
	if e := fs.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || path == "" {
		return errors.New("provide --config and documented options only")
	}
	if command == "serve" {
		return email.Serve(ctx, path, fingerprint, in, out)
	}
	c, fp, e := email.Load(path)
	if e != nil {
		return e
	}
	if live {
		if account == "" {
			return errors.New("live check requires --account")
		}
		raw, _ := json.Marshal(map[string]string{"account_id": account})
		result, e := (email.Service{Path: path, Fingerprint: fp}).Call(ctx, "check_account", raw)
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(result)
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "local_configuration_valid", "accounts": c.Catalog(), "fingerprint": fp, "remote_verified": false})
}
func resolveEmail(o *options) error {
	global := componentconfig.DataSourceID(o.emailConfig) == "email"
	if !global && o.pluginNames != nil && !slices.Contains(o.pluginNames, "email") {
		return nil
	}
	if o.emailConfig == "" || o.noTools {
		return nil
	}
	_, fp, e := email.Load(o.emailConfig)
	if e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	exe, e = filepath.EvalSymlinks(exe)
	if e != nil {
		return e
	}
	o.emailFingerprint = fp
	o.mcpServers = append(o.mcpServers, harness.MCPServer{Name: "email", Command: exe, Args: []string{"email", "serve", "--config", o.emailConfig, "--fingerprint", fp}})
	return nil
}
