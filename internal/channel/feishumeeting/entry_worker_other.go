//go:build !unix

package feishumeeting

import (
	"context"
	"errors"
	"os/exec"
)

func entryCommand(context.Context, string, ...string) (*exec.Cmd, func(), error) {
	return nil, nil, errors.New("Feishu entry supervision requires Unix process groups")
}

func EntryWorker([]string) int { return 125 }
