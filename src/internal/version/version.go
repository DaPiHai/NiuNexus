// Package version carries build-time version information.
package version

var (
	// Version is injected at build time with -ldflags.
	Version = "0.1.0-dev"
	// BuildTime is injected at build time with -ldflags.
	BuildTime = "unknown"
)
