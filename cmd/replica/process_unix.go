//go:build unix

package main

import (
	"errors"
	"log"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func signalProcess(cmd *exec.Cmd, force bool) {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		log.Printf("signal replica pid=%d: %v", cmd.Process.Pid, err)
	}
}
