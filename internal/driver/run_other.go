//go:build !unix

package driver

import "os/exec"

func setProcessGroup(*exec.Cmd)  {}
func killProcessGroup(*exec.Cmd) {}
