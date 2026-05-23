package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatisticsSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "statistics.json")

	store, err := LoadStatistics(path, []string{"k1", "k2"})
	if err != nil {
		t.Fatalf("LoadStatistics returned error: %v", err)
	}

	store.Increment("k1")
	store.Increment("k1")
	if err := store.Save(); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, err := LoadStatistics(path, []string{"k1", "k2"})
	if err != nil {
		t.Fatalf("reloading statistics returned error: %v", err)
	}

	if got, want := loaded.counts["k1"].Count, uint64(2); got != want {
		t.Fatalf("unexpected count: got %d want %d", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("statistics file is empty")
	}
}
