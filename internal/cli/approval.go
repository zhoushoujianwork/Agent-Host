package cli

import (
	"agent-host/internal/approval"
	"context"
	"errors"
	"flag"
	"io"
)

func runApprovalMCP(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	fs := flag.NewFlagSet("approval-mcp", flag.ContinueOnError)
	fs.SetOutput(errout)
	directory := fs.String("directory", "", "Host-created private proposal directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *directory == "" || len(fs.Args()) != 0 {
		return errors.New("approval-mcp requires only --directory")
	}
	return approval.Serve(ctx, *directory, in, out)
}
