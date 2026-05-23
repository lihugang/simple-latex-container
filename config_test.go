package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsAndValidation(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	configFilePath := filepath.Join(temporaryDirectory, "config.json")

	if writeError := os.WriteFile(configFilePath, []byte(`{"apiKeys":[" key1 ","key2"]}`), 0o644); writeError != nil {
		testingContext.Fatal(writeError)
	}

	config, loadError := loadConfig(configFilePath)
	if loadError != nil {
		testingContext.Fatalf("loadConfig returned error: %v", loadError)
	}

	if config.Listen != ":8080" {
		testingContext.Fatalf("unexpected listen address: %q", config.Listen)
	}
	if actualValue, expectedValue := config.ApiKeys[0], "key1"; actualValue != expectedValue {
		testingContext.Fatalf("unexpected trimmed key: got %q want %q", actualValue, expectedValue)
	}
}

func TestLoadConfigRejectsDuplicateKeys(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	configFilePath := filepath.Join(temporaryDirectory, "config.json")

	if writeError := os.WriteFile(configFilePath, []byte(`{"apiKeys":["dup","dup"]}`), 0o644); writeError != nil {
		testingContext.Fatal(writeError)
	}

	if _, loadError := loadConfig(configFilePath); loadError == nil {
		testingContext.Fatal("expected duplicate key error")
	}
}
