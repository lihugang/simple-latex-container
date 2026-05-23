package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatisticsSaveAndLoad(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	statisticsFilePath := filepath.Join(temporaryDirectory, "statistics.json")

	store, loadError := loadStatistics(statisticsFilePath, []string{"key1", "key2"})
	if loadError != nil {
		testingContext.Fatalf("loadStatistics returned error: %v", loadError)
	}

	store.incrementUsage("key1")
	store.incrementUsage("key1")
	if saveError := store.save(); saveError != nil {
		testingContext.Fatalf("save returned error: %v", saveError)
	}

	reloadedStore, reloadError := loadStatistics(statisticsFilePath, []string{"key1", "key2"})
	if reloadError != nil {
		testingContext.Fatalf("reloading statistics returned error: %v", reloadError)
	}

	if actualValue, expectedValue := reloadedStore.counts["key1"].Count, uint64(2); actualValue != expectedValue {
		testingContext.Fatalf("unexpected count: got %d want %d", actualValue, expectedValue)
	}

	fileContent, readError := os.ReadFile(statisticsFilePath)
	if readError != nil {
		testingContext.Fatal(readError)
	}
	if len(fileContent) == 0 {
		testingContext.Fatal("statistics file is empty")
	}
}
