// Package version holds build-time version metadata for the grabber CLI.
//
// The exported values are injected at build time via the linker, e.g.:
//
//	go build -ldflags "-X github.com/liamg/grabber/internal/version.Version=v1.2.3" ./cmd/grabber
//
// For non-release builds (go run, go install, plain go build) the defaults
// below apply.
package version

import (
	"fmt"
	"runtime"
)

// These are overridden at build time via -ldflags "-X ...".
var (
	Version = "dev"     // release tag, e.g. "v1.2.3"
	Commit  = "none"    // git commit SHA
	Date    = "unknown" // build timestamp (RFC 3339)
)

// String returns a human-readable, multi-line version summary.
func String() string {
	return fmt.Sprintf(
		"grabber %s\n  commit:   %s\n  built:    %s\n  go:       %s\n  platform: %s/%s",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH,
	)
}
