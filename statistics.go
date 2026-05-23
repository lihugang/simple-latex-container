package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// statisticsEntry is the persisted value for one API key.
// The shape is intentionally simple so that statistics.json remains easy to
// inspect and edit manually if recovery is ever needed.
type statisticsEntry struct {
	Count uint64 `json:"count"`
}

// statisticsStore keeps request counters in memory and periodically flushes
// them to disk. A mutex protects the in-memory map because both the HTTP
// request path and the background save loop access it concurrently.
type statisticsStore struct {
	filePath string
	mutex    sync.Mutex
	counts   map[string]statisticsEntry
}

// loadStatistics initializes the statistics store from disk if the file exists.
// Missing files are treated as a normal first-start condition.
func loadStatistics(filePath string, apiKeys []string) (*statisticsStore, error) {
	store := &statisticsStore{
		filePath: filePath,
		counts:   make(map[string]statisticsEntry, len(apiKeys)),
	}

	for _, apiKey := range apiKeys {
		store.counts[apiKey] = statisticsEntry{}
	}

	fileContent, readError := os.ReadFile(filePath)
	if readError != nil {
		if os.IsNotExist(readError) {
			return store, nil
		}
		return nil, fmt.Errorf("read statistics: %w", readError)
	}

	var persistedCounts map[string]statisticsEntry
	if unmarshalError := json.Unmarshal(fileContent, &persistedCounts); unmarshalError != nil {
		return nil, fmt.Errorf("parse statistics: %w", unmarshalError)
	}

	for apiKey, entry := range persistedCounts {
		store.counts[apiKey] = entry
	}

	for _, apiKey := range apiKeys {
		if _, exists := store.counts[apiKey]; !exists {
			store.counts[apiKey] = statisticsEntry{}
		}
	}

	return store, nil
}

// incrementUsage records one authenticated POST /code request for the given
// API key. The method intentionally does not care whether the request later
// hits cache, compiles successfully, or fails during LaTeX execution.
func (store *statisticsStore) incrementUsage(apiKey string) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	entry := store.counts[apiKey]
	entry.Count++
	store.counts[apiKey] = entry
}

// save writes the current in-memory counters to statistics.json by first
// writing a temporary file and then renaming it into place. This prevents
// partially written JSON from replacing the last known good file.
func (store *statisticsStore) save() error {
	store.mutex.Lock()
	countSnapshot := make(map[string]statisticsEntry, len(store.counts))
	for apiKey, entry := range store.counts {
		countSnapshot[apiKey] = entry
	}
	store.mutex.Unlock()

	encodedContent, marshalError := json.MarshalIndent(countSnapshot, "", "  ")
	if marshalError != nil {
		return fmt.Errorf("marshal statistics: %w", marshalError)
	}
	encodedContent = append(encodedContent, '\n')

	temporaryFilePath := store.filePath + ".tmp"
	if makeDirectoryError := os.MkdirAll(filepath.Dir(store.filePath), 0o755); makeDirectoryError != nil {
		return fmt.Errorf("create statistics dir: %w", makeDirectoryError)
	}
	if writeError := os.WriteFile(temporaryFilePath, encodedContent, 0o644); writeError != nil {
		return fmt.Errorf("write statistics temp file: %w", writeError)
	}
	if renameError := os.Rename(temporaryFilePath, store.filePath); renameError != nil {
		return fmt.Errorf("replace statistics file: %w", renameError)
	}

	return nil
}

// runAutoSave flushes statistics on a fixed interval until the parent context
// is cancelled. Save failures are logged and the loop continues so a transient
// disk issue does not stop future save attempts.
func (store *statisticsStore) runAutoSave(requestContext context.Context, saveInterval time.Duration, logger *log.Logger) {
	saveTicker := time.NewTicker(saveInterval)
	defer saveTicker.Stop()

	for {
		select {
		case <-requestContext.Done():
			return
		case <-saveTicker.C:
			if saveError := store.save(); saveError != nil && logger != nil {
				logger.Printf("periodic statistics save failed: %v", saveError)
			}
		}
	}
}
