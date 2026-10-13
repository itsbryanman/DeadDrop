//go:build windows

package storage

import (
	"errors"
	"syscall"
	"unsafe"
)

// syncDir is a no-op: Windows does not support flushing directory handles
// opened this way, and NTFS journals the rename metadata.
func syncDir(string) {}

const (
	errorHandleDiskFull syscall.Errno = 39
	errorDiskFull       syscall.Errno = 112
)

// IsNoSpace reports whether err means the disk or quota is full.
func IsNoSpace(err error) bool {
	return errors.Is(err, errorDiskFull) || errors.Is(err, errorHandleDiskFull)
}

var procGetDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// FreeSpace returns bytes available to the current user at path.
func FreeSpace(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var avail uint64
	r, _, callErr := procGetDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), 0, 0)
	if r == 0 {
		return 0, callErr
	}
	return avail, nil
}
