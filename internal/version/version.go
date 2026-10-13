// Package version holds build metadata.
package version

import "runtime/debug"

// Version is set at link time:
//
//	-ldflags "-X github.com/itsbryanman/DeadDrop/internal/version.Version=v0.1.0"
var Version = "dev"

// String returns the version, falling back to module build info for
// binaries produced by `go install`.
func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}
