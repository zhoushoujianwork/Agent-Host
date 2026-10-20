package claude

import (
	"agent-host/internal/harness/process"
	"os/exec"
)

func bindProcessGroup(cmd *exec.Cmd) error { return process.Bind(cmd) }
func stopProcessGroup(cmd *exec.Cmd)       { process.Stop(cmd) }
