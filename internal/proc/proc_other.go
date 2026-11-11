//go:build !unix && !windows

package proc

import (
	"errors"
	"os/exec"
)

// Alive cannot tell on this platform and says no.
func Alive(pid int) bool { return false }

// StartDetached is not supported on this platform.
func StartDetached(cmd *exec.Cmd) error { return errors.New("detached processes need Unix or Windows") }
