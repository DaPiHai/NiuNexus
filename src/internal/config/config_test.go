package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg := Defaults()
	if err := Validate(cfg); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	if !cfg.Settings.SafeMode {
		t.Fatal("safe mode should default to true")
	}
	if cfg.Settings.ConfirmTimeoutSec != 120 {
		t.Fatalf("unexpected timeout: %d", cfg.Settings.ConfirmTimeoutSec)
	}
}

func TestUpdateSettingsPersistsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	store := Open(dir)

	safe := false
	timeout := 30
	lang := "en"
	level := "debug"
	auto := false
	ifaces := []string{"eth1", " eth0 ", "eth0"}
	cfg, err := store.UpdateSettings(SettingsPatch{
		SafeMode:            &safe,
		ConfirmTimeoutSec:   &timeout,
		Language:            &lang,
		LogLevel:            &level,
		AutoApplyOnStart:    &auto,
		ProtectedInterfaces: &ifaces,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if cfg.Settings.SafeMode || cfg.Settings.ConfirmTimeoutSec != 30 || cfg.Settings.Language != "en" {
		t.Fatalf("patch not applied: %+v", cfg.Settings)
	}
	if got := cfg.Settings.ProtectedInterfaces; len(got) != 2 || got[0] != "eth0" || got[1] != "eth1" {
		t.Fatalf("interfaces not normalized: %#v", got)
	}

	path := filepath.Join(dir, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config permissions = %o, want 600", perm)
	}
	var onDisk Config
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if onDisk.Settings.ConfirmTimeoutSec != 30 {
		t.Fatalf("disk value = %d", onDisk.Settings.ConfirmTimeoutSec)
	}

	// A second write must back up the first document.
	timeout = 45
	if _, err := store.UpdateSettings(SettingsPatch{ConfirmTimeoutSec: &timeout}); err != nil {
		t.Fatalf("second update: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "backups", "config-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %d, want 1", len(backups))
	}
}

func TestInvalidSettingsRejected(t *testing.T) {
	dir := t.TempDir()
	store := Open(dir)

	zero := 1
	if _, err := store.UpdateSettings(SettingsPatch{ConfirmTimeoutSec: &zero}); err == nil {
		t.Fatal("expected timeout validation error")
	}
	badLang := "fr"
	if _, err := store.UpdateSettings(SettingsPatch{Language: &badLang}); err == nil {
		t.Fatal("expected language validation error")
	}
	badIface := []string{"bad name!"}
	if _, err := store.UpdateSettings(SettingsPatch{ProtectedInterfaces: &badIface}); err == nil {
		t.Fatal("expected interface validation error")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid updates must not create config.json: %v", err)
	}
}

func TestInstallOptionsSeedDefaults(t *testing.T) {
	dir := t.TempDir()
	options := []byte(`{"safeMode": false, "language": "en"}`)
	if err := os.WriteFile(filepath.Join(dir, "install-options.json"), options, 0o600); err != nil {
		t.Fatal(err)
	}
	store := Open(dir)
	cfg := store.Config()
	if cfg.Settings.SafeMode {
		t.Fatal("install option safeMode=false was not applied")
	}
	if cfg.Settings.Language != "en" {
		t.Fatalf("language = %q", cfg.Settings.Language)
	}
}

func TestCorruptConfigFallsBackWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := []byte("{not-json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := Open(dir)
	if len(store.Warnings()) == 0 {
		t.Fatal("expected a warning for corrupt config")
	}
	if !store.Config().Settings.SafeMode {
		t.Fatal("fallback should use safe defaults")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("load must not rewrite a corrupt config file")
	}
}
