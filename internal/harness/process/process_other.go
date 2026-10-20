//go:build !unix

package process

import (
	"errors"
	"os/exec"
)

func Bind(*exec.Cmd) error {
	return errors.New("native adapter currently requires Unix process-group supervision")
}

func Stop(*exec.Cmd) {}
