package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"agent-host/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent-host:", err)
		os.Exit(1)
	}
}
