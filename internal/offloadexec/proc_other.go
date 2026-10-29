//go:build !unix

package offloadexec

import (
	"os"
	"os/exec"
)

// processGroupsSupported is false: without process groups the client cannot
// terminate and reap descendants, so Config.Validate and New reject this
// platform before any process starts. The functions below only keep the
// package compiling for cross-builds.
const processGroupsSupported = false

func configureProcess(*exec.Cmd) {}

func terminateGroup(p *os.Process) error { return p.Kill() }

func killGroup(p *os.Process) { _ = p.Kill() }
