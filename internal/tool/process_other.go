//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package tool

import "os/exec"

func configureProcess(*exec.Cmd) {}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
