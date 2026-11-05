package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"

	"agent-host/internal/channelprogress"
)

func runChannelProgressMCP(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	fs := flag.NewFlagSet("channel-progress-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var home, message, run, fingerprint string
	fs.StringVar(&home, "home", "", "Private Host state home")
	fs.StringVar(&message, "message", "", "Current authenticated receipt")
	fs.StringVar(&run, "run", "", "Current business run")
	fs.StringVar(&fingerprint, "fingerprint", "", "Expected run binding")
	if err := fs.Parse(args); err != nil {
		return errors.New("invalid channel-progress-mcp arguments")
	}
	if len(fs.Args()) != 0 || !filepath.IsAbs(home) || message == "" || run == "" || fingerprint == "" {
		return errors.New("channel-progress-mcp requires --home, --message, --run and --fingerprint")
	}
	b, err := channelprogress.Resolve(home, message, run)
	if err != nil {
		return err
	}
	if b.Receipt.Fingerprint != fingerprint {
		return errors.New("channel progress launch binding changed")
	}
	return channelprogress.Serve(ctx, b, in, out)
}
