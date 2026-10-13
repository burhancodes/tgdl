package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePersistenceAndBackup(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)

	data := &StoreData{
		Version: 1,
		Values: map[string]string{
			"LOG_LEVEL": "DEBUG",
		},
	}
	if err := store.Save(data); err != nil {
		t.Fatalf("Save: %v", err)
	}

	settingsPath := filepath.Join(dir, "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("Stat settings.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Values["LOG_LEVEL"] != "DEBUG" {
		t.Errorf("got %q, want DEBUG", loaded.Values["LOG_LEVEL"])
	}

	data.Values["LOG_LEVEL"] = "WARNING"
	if err := store.Save(data); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	bakPath := filepath.Join(dir, "settings.json.bak")
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("read bak: %v", err)
	}
	if !strings.Contains(string(bakData), "DEBUG") {
		t.Errorf("bak file does not contain previous value DEBUG: %s", string(bakData))
	}
}

func TestStoreEncryption(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	pass := "super-strong-passphrase"
	store := NewStore(dir, "", false, pass, reg)

	data := &StoreData{
		Version: 1,
		Values: map[string]string{
			"GOFILE_API_KEY": "secret_key_123456789",
			"LOG_LEVEL":      "INFO",
		},
	}
	if err := store.Save(data); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rawOnDisk, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	rawStr := string(rawOnDisk)
	if strings.Contains(rawStr, "secret_key_123456789") {
		t.Errorf("secret was saved as plaintext on disk!")
	}
	if !strings.Contains(rawStr, "enc:v1:") {
		t.Errorf("expected encrypted prefix enc:v1: in file")
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Values["GOFILE_API_KEY"] != "secret_key_123456789" {
		t.Errorf("got %q, want secret_key_123456789", loaded.Values["GOFILE_API_KEY"])
	}

	wrongStore := NewStore(dir, "", false, "wrong-passphrase", reg)
	if _, err := wrongStore.Load(); err == nil {
		t.Errorf("expected error when loading with wrong passphrase")
	}

	emptyKeyStore := NewStore(dir, "", false, "", reg)
	if _, err := emptyKeyStore.Load(); err == nil {
		t.Errorf("expected error when loading encrypted file without encryption key")
	}
}

func TestStoreSyncEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	initialContent := "# This is a comment\nLOG_LEVEL=INFO\nOTHER_SETTING=foo\n"
	if err := os.WriteFile(envPath, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := NewRegistry()
	store := NewStore(dir, envPath, true, "", reg)

	store.SyncEnv("LOG_LEVEL", "DEBUG", false)

	updated, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(updated)
	if !strings.Contains(content, "# This is a comment") {
		t.Errorf("comment was removed from .env: %s", content)
	}
	if !strings.Contains(content, "OTHER_SETTING=foo") {
		t.Errorf("other setting lost: %s", content)
	}
	if !strings.Contains(content, "LOG_LEVEL=DEBUG") {
		t.Errorf("LOG_LEVEL was not updated to DEBUG: %s", content)
	}

	store.SyncEnv("NEW_KEY", "bar", false)
	updated2, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(updated2), "NEW_KEY=bar") {
		t.Errorf("NEW_KEY not appended to .env: %s", string(updated2))
	}
}
