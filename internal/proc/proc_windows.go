//go:build windows

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	detachedProcess                = 0x00000008
	createBreakawayFromJob         = 0x01000000
)

// Alive reports whether a process with this pid exists and has not exited.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// Access denied means the process exists but belongs to someone else.
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// StartDetached starts cmd without a console and outside the caller's job, so it outlives the caller.
// When the caller's job does not allow breakaway, it starts inside the job instead.
func StartDetached(cmd *exec.Cmd) error {
	base := uint32(syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: base | createBreakawayFromJob}
	if err := cmd.Start(); err == nil {
		return nil
	}
	c2 := exec.Command(cmd.Path, cmd.Args[1:]...)
	c2.Env, c2.Dir, c2.Stdin, c2.Stdout, c2.Stderr, c2.ExtraFiles = cmd.Env, cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.ExtraFiles
	c2.SysProcAttr = &syscall.SysProcAttr{CreationFlags: base}
	if err := c2.Start(); err != nil {
		return err
	}
	*cmd = *c2
	return nil
}
