//go:build unix

package storage

import (
	"errors"
	"os"
	"syscall"
)

// syncDir flushes directory metadata so a committed name survives a crash.
func syncDir(path string) {
	if d, err := os.Open(path); err == nil {
		d.Sync()
		d.Close()
	}
}

// IsNoSpace reports whether err means the disk or quota is full.
func IsNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
