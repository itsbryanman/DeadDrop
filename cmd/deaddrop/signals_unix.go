//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipe keeps a vanished stdout reader from killing us. In a
// pipeline like `deaddrop receive --json | jq`, Ctrl+C stops jq too; Go
// would then exit on SIGPIPE at the next write, skipping the cleanup of
// in-progress uploads. With SIGPIPE ignored the write just fails.
func ignoreBrokenPipe() { signal.Ignore(syscall.SIGPIPE) }
