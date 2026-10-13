//go:build windows

package terminal

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

const enableVirtualTerminalProcessing = 0x0004

// isTerminal reports whether f is a console, enabling ANSI escape handling
// on it when possible.
func isTerminal(f *os.File) bool {
	var mode uint32
	h := f.Fd()
	if r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	procSetConsoleMode.Call(h, uintptr(mode|enableVirtualTerminalProcessing))
	return true
}
