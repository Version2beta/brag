//go:build darwin || linux

package main

import (
	"os/exec"
	"syscall"
)

func supportsProcessGroupCleanup() bool { return true }

func configureDynamicProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
