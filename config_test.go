package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(`{"api_keys":[" key1 ","key2"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Listen != ":8080" {
		t.Fatalf("unexpected listen address: %q", cfg.Listen)
	}
	if got, want := cfg.APIKeys[0], "key1"; got != want {
		t.Fatalf("unexpected trimmed key: got %q want %q", got, want)
	}
}

func TestLoadConfigRejectsDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(`{"api_keys":["dup","dup"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected duplicate key error")
	}
}
