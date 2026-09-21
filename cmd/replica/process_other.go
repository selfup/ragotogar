//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

func configureProcess(*exec.Cmd) error {
	return errors.New("-spawn requires Unix process groups (macOS or Linux)")
}
func signalProcess(*exec.Cmd, bool) {}
