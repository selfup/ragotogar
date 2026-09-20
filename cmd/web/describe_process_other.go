//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

func configureDescribeProcess(cmd *exec.Cmd) error {
	return errors.New("browser describe runs require Unix process groups (macOS or Linux)")
}
