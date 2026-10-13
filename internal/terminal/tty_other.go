//go:build !unix && !windows

package terminal

import "os"

func isTerminal(*os.File) bool { return false }
