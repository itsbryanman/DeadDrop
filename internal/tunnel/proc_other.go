//go:build !unix && !windows

package tunnel

import "os/exec"

func configureProcess(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) { cmd.Process.Kill() }

func kill(cmd *exec.Cmd) { cmd.Process.Kill() }
