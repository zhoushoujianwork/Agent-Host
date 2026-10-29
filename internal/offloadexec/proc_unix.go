//go:build unix

package offloadexec

import (
	"os"
	"os/exec"
	"syscall"
)

// processGroupsSupported reports that owned process-group cleanup is available.
const processGroupsSupported = true

// configureProcess places the child in its own process group so that signals
// reach only the processes this client created.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup asks the child's process group to stop.
func terminateGroup(p *os.Process) error {
	return syscall.Kill(-p.Pid, syscall.SIGTERM)
}

// killGroup force-stops every remaining member of the child's process group.
// The group id stays reserved while any member is alive, so a live group is
// never confused with an unrelated process.
func killGroup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
