//go:build linux || darwin || freebsd

package storage

import "syscall"

// FreeSpace returns bytes available to unprivileged users at path. It is
// advisory: other writers can consume the space before an upload finishes.
func FreeSpace(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
