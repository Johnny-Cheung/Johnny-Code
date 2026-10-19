//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tool

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Setpgid makes the direct child the process-group leader. Use its PID
	// directly because the child may already have exited when cancellation
	// reaches Cmd.Cancel while descendants still hold the pipes open.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
