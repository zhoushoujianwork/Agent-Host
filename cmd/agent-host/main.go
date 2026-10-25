package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/cli"
	"agent-host/internal/dingtalkworkspace"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == feishumeeting.EntryWorkerFlag {
		os.Exit(feishumeeting.EntryWorker(os.Args[2:]))
	}
	if len(os.Args) == 2 && os.Args[1] == dingtalkworkspace.WorkerFlag {
		code, err := dingtalkworkspace.Worker(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agent-host:", err)
		}
		os.Exit(code)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent-host:", err)
		os.Exit(1)
	}
}
