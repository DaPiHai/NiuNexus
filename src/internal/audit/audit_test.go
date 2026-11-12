package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAndReadNewestFirst(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"first", "second", "third"} {
		if err := logger.Log(Entry{User: "admin", Action: action, Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := logger.Read(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Action != "third" || entries[1].Action != "second" {
		t.Fatalf("unexpected order: %#v", entries)
	}
	info, err := os.Stat(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("audit permissions = %o, want 600", perm)
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var logger *Logger
	if err := logger.Log(Entry{User: "x", Action: "y"}); err != nil {
		t.Fatalf("nil log: %v", err)
	}
	entries, err := logger.Read(10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("nil read: %v %d", err, len(entries))
	}
}

func TestSanitizeRemovesSecrets(t *testing.T) {
	got := Sanitize("proxy password=supersecret token=abc123 ok")
	if strings.Contains(got, "supersecret") || strings.Contains(got, "abc123") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("non-secret content lost: %q", got)
	}
}
