//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func configureChildProcess(cmd *exec.Cmd) {
}

func signalChildTree(cmd *exec.Cmd, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	if err := cmd.Process.Signal(sig); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
