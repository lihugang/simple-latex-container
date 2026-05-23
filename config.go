package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// serviceConfig contains all runtime options that are loaded from config.json.
// The file is intentionally small because this service only needs a listen
// address and a fixed in-memory list of allowed API keys.
type serviceConfig struct {
	Listen      string   `json:"listen"`
	ApiKeys     []string `json:"apiKeys"`
	PdfToPngDpi int      `json:"pdfToPngDpi"`
}

// loadConfig reads the JSON configuration file, applies defaults, and rejects
// malformed or duplicate API keys before the HTTP server starts.
func loadConfig(filePath string) (serviceConfig, error) {
	fileContent, readError := os.ReadFile(filePath)
	if readError != nil {
		return serviceConfig{}, fmt.Errorf("read config: %w", readError)
	}

	var config serviceConfig
	if unmarshalError := json.Unmarshal(fileContent, &config); unmarshalError != nil {
		return serviceConfig{}, fmt.Errorf("parse config: %w", unmarshalError)
	}

	if config.Listen == "" {
		config.Listen = ":8080"
	}
	if config.PdfToPngDpi == 0 {
		config.PdfToPngDpi = 450
	}
	if config.PdfToPngDpi <= 0 {
		return serviceConfig{}, errors.New("config.pdfToPngDpi must be greater than 0")
	}

	if len(config.ApiKeys) == 0 {
		return serviceConfig{}, errors.New("config.apiKeys must not be empty")
	}

	seenApiKeys := make(map[string]struct{}, len(config.ApiKeys))
	for index, apiKey := range config.ApiKeys {
		trimmedApiKey := strings.TrimSpace(apiKey)
		if trimmedApiKey == "" {
			return serviceConfig{}, fmt.Errorf("config.apiKeys[%d] must not be empty", index)
		}
		if _, alreadyExists := seenApiKeys[trimmedApiKey]; alreadyExists {
			return serviceConfig{}, fmt.Errorf("config.apiKeys[%d] duplicates another key", index)
		}

		seenApiKeys[trimmedApiKey] = struct{}{}
		config.ApiKeys[index] = trimmedApiKey
	}

	return config, nil
}
