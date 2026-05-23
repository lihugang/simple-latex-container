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
	if config.PdfToPngDpi != 450 {
		testingContext.Fatalf("unexpected default pdfToPngDpi: %d", config.PdfToPngDpi)
	}
	if actualValue, expectedValue := config.ApiKeys[0], "key1"; actualValue != expectedValue {
		testingContext.Fatalf("unexpected trimmed key: got %q want %q", actualValue, expectedValue)
	}
}

func TestLoadConfigAcceptsExplicitPdfToPngDpi(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	configFilePath := filepath.Join(temporaryDirectory, "config.json")

	if writeError := os.WriteFile(configFilePath, []byte(`{"apiKeys":["key1"],"pdfToPngDpi":600}`), 0o644); writeError != nil {
		testingContext.Fatal(writeError)
	}

	config, loadError := loadConfig(configFilePath)
	if loadError != nil {
		testingContext.Fatalf("loadConfig returned error: %v", loadError)
	}
	if config.PdfToPngDpi != 600 {
		testingContext.Fatalf("unexpected explicit pdfToPngDpi: %d", config.PdfToPngDpi)
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

func TestLoadConfigRejectsNonPositivePdfToPngDpi(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	configFilePath := filepath.Join(temporaryDirectory, "config.json")

	if writeError := os.WriteFile(configFilePath, []byte(`{"apiKeys":["key1"],"pdfToPngDpi":-1}`), 0o644); writeError != nil {
		testingContext.Fatal(writeError)
	}

	if _, loadError := loadConfig(configFilePath); loadError == nil {
		testingContext.Fatal("expected pdfToPngDpi validation error")
	}
}
