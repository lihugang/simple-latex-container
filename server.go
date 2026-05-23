package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type App struct {
	apiKeys    map[string]struct{}
	stats      *StatisticsStore
	compiler   CompileProcessor
	resultsDir string
}

type codeRequest struct {
	Payload string `json:"payload"`
}

type responseEnvelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data"`
	Error string `json:"error"`
}

func NewApp(cfg Config, stats *StatisticsStore, compiler CompileProcessor, resultsDir string) *App {
	keys := make(map[string]struct{}, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		keys[key] = struct{}{}
	}

	return &App{
		apiKeys:    keys,
		stats:      stats,
		compiler:   compiler,
		resultsDir: resultsDir,
	}
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /code", a.handleCode)
	mux.HandleFunc("GET /{id}/pdf", a.handlePDF)
	mux.HandleFunc("GET /{id}/png/{pagenum}", a.handlePNG)
	return mux
}

func (a *App) handleCode(w http.ResponseWriter, r *http.Request) {
	apiKey, ok := a.authorize(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, responseEnvelope{OK: false, Data: nil, Error: "unauthorized"})
		return
	}

	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, responseEnvelope{OK: false, Data: nil, Error: "invalid json body"})
		return
	}
	if strings.TrimSpace(req.Payload) == "" {
		writeJSON(w, http.StatusBadRequest, responseEnvelope{OK: false, Data: nil, Error: "payload must not be empty"})
		return
	}

	a.stats.Increment(apiKey)

	result, failure, err := a.compiler.Process(r.Context(), req.Payload)
	if failure != nil {
		writeJSON(w, http.StatusOK, responseEnvelope{OK: false, Data: nil, Error: failure.Message})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, responseEnvelope{OK: false, Data: nil, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, responseEnvelope{OK: true, Data: result, Error: ""})
}

func (a *App) handlePDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(a.resultsDir, id, "main.pdf")
	a.serveStaticFile(w, r, path, "application/pdf")
}

func (a *App) handlePNG(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		http.NotFound(w, r)
		return
	}

	pageNum, err := strconv.Atoi(r.PathValue("pagenum"))
	if err != nil || pageNum <= 0 {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(a.resultsDir, id, fmt.Sprintf("%d.png", pageNum))
	a.serveStaticFile(w, r, path, "image/png")
}

func (a *App) serveStaticFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, responseEnvelope{OK: false, Data: nil, Error: "internal server error"})
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, path)
}

func (a *App) authorize(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}

	apiKey := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if apiKey == "" {
		return "", false
	}
	_, ok := a.apiKeys[apiKey]
	return apiKey, ok
}

func validID(id string) bool {
	return idPattern.MatchString(id)
}

func writeJSON(w http.ResponseWriter, statusCode int, payload responseEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

var lookPath = exec.LookPath

func verifyToolchain() error {
	for _, name := range []string{"xelatex", "pdfinfo", "pdftoppm"} {
		if _, err := lookPath(name); err != nil {
			return fmt.Errorf("required executable %q not found in PATH", name)
		}
	}
	return nil
}
