//go:build !unix

package claude

import (
	"errors"
	"os/exec"
)

func bindProcessGroup(*exec.Cmd) error {
	return errors.New("Claude adapter currently requires Unix process-group supervision")
}

func stopProcessGroup(*exec.Cmd) {}
