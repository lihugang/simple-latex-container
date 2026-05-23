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

type StatsEntry struct {
	Count uint64 `json:"count"`
}

type StatisticsStore struct {
	path   string
	mu     sync.Mutex
	counts map[string]StatsEntry
}

func LoadStatistics(path string, apiKeys []string) (*StatisticsStore, error) {
	store := &StatisticsStore{
		path:   path,
		counts: make(map[string]StatsEntry, len(apiKeys)),
	}

	for _, key := range apiKeys {
		store.counts[key] = StatsEntry{}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("read statistics: %w", err)
	}

	var persisted map[string]StatsEntry
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("parse statistics: %w", err)
	}

	for key, entry := range persisted {
		store.counts[key] = entry
	}

	for _, key := range apiKeys {
		if _, ok := store.counts[key]; !ok {
			store.counts[key] = StatsEntry{}
		}
	}

	return store, nil
}

func (s *StatisticsStore) Increment(apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.counts[apiKey]
	entry.Count++
	s.counts[apiKey] = entry
}

func (s *StatisticsStore) Save() error {
	s.mu.Lock()
	snapshot := make(map[string]StatsEntry, len(s.counts))
	for key, entry := range s.counts {
		snapshot[key] = entry
	}
	s.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal statistics: %w", err)
	}
	data = append(data, '\n')

	tmpPath := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create statistics dir: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write statistics temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace statistics file: %w", err)
	}

	return nil
}

func (s *StatisticsStore) RunAutoSave(ctx context.Context, interval time.Duration, logger *log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Save(); err != nil && logger != nil {
				logger.Printf("periodic statistics save failed: %v", err)
			}
		}
	}
}
