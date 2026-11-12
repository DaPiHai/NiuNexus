package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	store := New(path)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Data{ProxyPassword: "p", UpstreamPassword: "u"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o", perm)
	}
	data, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if data.ProxyPassword != "p" || data.UpstreamPassword != "u" {
		t.Fatalf("data = %+v", data)
	}
	if err := store.Save(Data{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty save should remove secrets: %v", err)
	}
}
