// Package sysctl owns the small set of host tunables changed by NiuNexus.
// Values are saved before the first change and restored on disable/stop.
package sysctl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Forwarding manages net.ipv4.ip_forward.
type Forwarding struct {
	stateFile string
	procPath  string
}

// New creates a forwarding manager writing its original-value marker under
// varDir.
func New(varDir string) *Forwarding {
	return NewWithPaths(varDir, "/proc/sys/net/ipv4/ip_forward")
}

// NewWithPaths allows tests to use a temporary file as the proc entry.
func NewWithPaths(varDir, procPath string) *Forwarding {
	return &Forwarding{
		stateFile: filepath.Join(varDir, "ip_forward.original"),
		procPath:  procPath,
	}
}

// Ensure turns IPv4 forwarding on or restores the saved original value.
func (f *Forwarding) Ensure(enabled bool) error {
	if enabled {
		if _, err := os.Stat(f.stateFile); errors.Is(err, os.ErrNotExist) {
			original, err := os.ReadFile(f.procPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(f.stateFile), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(f.stateFile, []byte(strings.TrimSpace(string(original))+"\n"), 0o600); err != nil {
				return err
			}
		}
		return os.WriteFile(f.procPath, []byte("1\n"), 0o644)
	}

	original, err := os.ReadFile(f.stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.WriteFile(f.procPath, original, 0o644); err != nil {
		return err
	}
	return os.Remove(f.stateFile)
}

// Restore is an alias used during shutdown.
func (f *Forwarding) Restore() error {
	return f.Ensure(false)
}
