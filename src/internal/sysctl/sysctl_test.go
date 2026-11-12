package sysctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForwardingToggleAndRestore(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "ip_forward")
	if err := os.WriteFile(proc, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewWithPaths(dir, proc)
	if err := manager.Ensure(true); err != nil {
		t.Fatal(err)
	}
	value, _ := os.ReadFile(proc)
	if strings.TrimSpace(string(value)) != "1" {
		t.Fatalf("forwarding = %q", value)
	}
	state := filepath.Join(dir, "ip_forward.original")
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if err := manager.Ensure(false); err != nil {
		t.Fatal(err)
	}
	value, _ = os.ReadFile(proc)
	if strings.TrimSpace(string(value)) != "0" {
		t.Fatalf("forwarding not restored: %q", value)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("state file should be removed: %v", err)
	}
}
