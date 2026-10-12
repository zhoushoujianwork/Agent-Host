package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"agent-host/internal/history"
)

// runHistoryMCP is a private subprocess entry point. Access is derived from the
// authenticated current receipt, never from model-supplied identity arguments.
func runHistoryMCP(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	fs := flag.NewFlagSet("history-mcp", flag.ContinueOnError)
	fs.SetOutput(errout)
	var home, message, fingerprint string
	fs.StringVar(&home, "home", "", "Private Host state home")
	fs.StringVar(&message, "message", "", "Current authenticated receipt")
	fs.StringVar(&fingerprint, "fingerprint", "", "Expected history access binding")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || !filepath.IsAbs(home) || message == "" || fingerprint == "" {
		return errors.New("history-mcp requires --home, --message and --fingerprint")
	}
	binding, err := history.Resolve(home, message)
	if err != nil {
		return err
	}
	if binding.Fingerprint != fingerprint {
		return errors.New("conversation history access changed after assembly")
	}
	return history.Serve(ctx, binding, in, out)
}

func enableChannelHistory(o *options) error {
	o.historyEnabled = true
	if o.noTools {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("cannot resolve built-in history MCP executable")
	}
	o.historyExecutable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return errors.New("cannot resolve built-in history MCP executable")
	}
	return nil
}
