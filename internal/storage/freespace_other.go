//go:build !(linux || darwin || freebsd || windows)

package storage

import "errors"

// FreeSpace is not implemented on this platform.
func FreeSpace(string) (uint64, error) { return 0, errors.ErrUnsupported }
