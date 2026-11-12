package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"niunexus/internal/config"
)

func writeConfig(t *testing.T, dir string, timeout int) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Settings.ConfirmTimeoutSec = timeout
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTimeout(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Settings.ConfirmTimeoutSec
}

func TestCreateAndRestoreConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	writeConfig(t, configDir, 33)

	manager := NewWithOptions(configDir, varDir, true)
	meta, err := manager.Create("test change", "tester")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !meta.HasConfig {
		t.Fatal("snapshot should contain config")
	}
	if meta.Actor != "tester" || meta.Reason != "test change" {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	writeConfig(t, configDir, 99)
	report, err := manager.Restore(meta.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !report.ConfigRestored {
		t.Fatal("config was not restored")
	}
	if got := readTimeout(t, configDir); got != 33 {
		t.Fatalf("timeout = %d, want 33", got)
	}

	// Restoring must itself create a backup of the overwritten document.
	backups, err := filepath.Glob(filepath.Join(configDir, "backups", "config-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) == 0 {
		t.Fatal("expected a config backup during restore")
	}
}

func TestListNewestFirstAndRestoreLast(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	writeConfig(t, configDir, 10)
	manager := NewWithOptions(configDir, varDir, true)

	first, err := manager.Create("first", "tester")
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, configDir, 20)
	second, err := manager.Create("second", "tester")
	if err != nil {
		t.Fatal(err)
	}

	list, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(list))
	}
	if list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("unexpected order: %#v", list)
	}

	writeConfig(t, configDir, 30)
	if _, err := manager.RestoreLast(); err != nil {
		t.Fatal(err)
	}
	if got := readTimeout(t, configDir); got != 20 {
		t.Fatalf("restore last timeout = %d, want 20", got)
	}
}

func TestRestoreWithoutConfigIsSafe(t *testing.T) {
	root := t.TempDir()
	manager := NewWithOptions(filepath.Join(root, "etc"), filepath.Join(root, "var"), true)
	meta, err := manager.Create("no config", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if meta.HasConfig {
		t.Fatal("unexpected config in snapshot")
	}
	report, err := manager.Restore(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.ConfigRestored {
		t.Fatal("config should not be restored")
	}
}

func TestSecretsSnapshotRestore(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(configDir, "secrets.json")
	if err := os.WriteFile(secretsPath, []byte(`{"proxyPassword":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewWithOptions(configDir, varDir, true)
	meta, err := manager.Create("secrets", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.HasSecrets {
		t.Fatal("snapshot did not capture secrets")
	}
	if err := os.WriteFile(secretsPath, []byte(`{"proxyPassword":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Restore(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.SecretsRestored {
		t.Fatal("secrets were not restored")
	}
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"proxyPassword":"old"}` {
		t.Fatalf("secrets = %s", data)
	}
}

func TestRestoreLastWithoutSnapshots(t *testing.T) {
	root := t.TempDir()
	manager := NewWithOptions(filepath.Join(root, "etc"), filepath.Join(root, "var"), true)
	if _, err := manager.RestoreLast(); err == nil {
		t.Fatal("expected an error when no snapshots exist")
	}
}
