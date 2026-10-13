//go:build unix

package tunnel

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the child in its own process group so it and any
// helpers it forks can be signalled together without touching unrelated
// cloudflared processes.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminate(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

func kill(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
