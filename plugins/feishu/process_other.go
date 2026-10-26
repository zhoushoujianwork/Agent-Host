//go:build !unix

package feishu

import (
	"errors"
	"os/exec"
)

func bindCLIProcessGroup(*exec.Cmd) error {
	return errors.New("Feishu CLI transport currently requires Unix process-group supervision")
}

func stopCLIProcessGroup(*exec.Cmd) {}
