//go:build windows

package tunnel

import (
	"os/exec"
	"strconv"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminate ends the process tree rooted at our child, addressed by PID.
func terminate(cmd *exec.Cmd) {
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

func kill(cmd *exec.Cmd) { cmd.Process.Kill() }
