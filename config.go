package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Listen  string   `json:"listen"`
	APIKeys []string `json:"api_keys"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}

	if len(cfg.APIKeys) == 0 {
		return Config{}, errors.New("config.api_keys must not be empty")
	}

	seen := make(map[string]struct{}, len(cfg.APIKeys))
	for i, key := range cfg.APIKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			return Config{}, fmt.Errorf("config.api_keys[%d] must not be empty", i)
		}
		if _, ok := seen[key]; ok {
			return Config{}, fmt.Errorf("config.api_keys[%d] duplicates another key", i)
		}
		seen[key] = struct{}{}
		cfg.APIKeys[i] = key
	}

	return cfg, nil
}
