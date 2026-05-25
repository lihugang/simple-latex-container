package main

import (
	"context"
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
	"time"
)

var documentIdPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// application wires configuration, statistics, and the compile pipeline into
// HTTP handlers. The type stays intentionally small so the transport layer does
// not absorb compiler or persistence logic.
type application struct {
	allowedApiKeys   map[string]struct{}
	statisticsStore  *statisticsStore
	compileProcessor compileProcessor
	compileTimeout   time.Duration
	resultsDirectory string
}

// codeRequestBody is the request payload for POST /code.
type codeRequestBody struct {
	Payload string `json:"payload"`
}

// responseEnvelope is the shared JSON response shape used by the service.
// Keeping one envelope type avoids subtle drift between success and error paths.
type responseEnvelope struct {
	Ok    bool   `json:"ok"`
	Data  any    `json:"data"`
	Error string `json:"error"`
}

// newApplication builds the HTTP layer from already-initialized dependencies.
func newApplication(config serviceConfig, statisticsStore *statisticsStore, compileProcessor compileProcessor, resultsDirectory string) *application {
	allowedApiKeys := make(map[string]struct{}, len(config.ApiKeys))
	for _, apiKey := range config.ApiKeys {
		allowedApiKeys[apiKey] = struct{}{}
	}

	return &application{
		allowedApiKeys:   allowedApiKeys,
		statisticsStore:  statisticsStore,
		compileProcessor: compileProcessor,
		compileTimeout:   time.Duration(config.CompileTimeoutSeconds) * time.Second,
		resultsDirectory: resultsDirectory,
	}
}

// routes registers all public HTTP endpoints exposed by the service.
func (application *application) routes() http.Handler {
	requestRouter := http.NewServeMux()
	requestRouter.HandleFunc("POST /code", application.handleCode)
	requestRouter.HandleFunc("GET /{id}/pdf", application.handlePdf)
	requestRouter.HandleFunc("GET /{id}/png/{pageNumber}", application.handlePng)
	return requestRouter
}

// handleCode authenticates the request, validates the body, records usage, and
// delegates the actual document pipeline to the compile processor.
func (application *application) handleCode(responseWriter http.ResponseWriter, request *http.Request) {
	apiKey, authorized := application.authorizeRequest(request)
	if !authorized {
		writeJson(responseWriter, http.StatusUnauthorized, responseEnvelope{Ok: false, Data: nil, Error: "unauthorized"})
		return
	}

	var requestBody codeRequestBody
	if decodeError := json.NewDecoder(request.Body).Decode(&requestBody); decodeError != nil {
		writeJson(responseWriter, http.StatusBadRequest, responseEnvelope{Ok: false, Data: nil, Error: "invalid json body"})
		return
	}
	if strings.TrimSpace(requestBody.Payload) == "" {
		writeJson(responseWriter, http.StatusBadRequest, responseEnvelope{Ok: false, Data: nil, Error: "payload must not be empty"})
		return
	}

	application.statisticsStore.incrementUsage(apiKey)

	compileContext := request.Context()
	cancelCompile := func() {}
	if application.compileTimeout > 0 {
		compileContext, cancelCompile = context.WithTimeout(request.Context(), application.compileTimeout)
	}
	defer cancelCompile()

	compileResult, compileFailure, processError := application.compileProcessor.process(compileContext, requestBody.Payload)
	if compileFailure != nil {
		// Compilation failures are part of the API contract, so they stay at the
		// JSON level instead of being promoted to HTTP 500 responses.
		writeJson(responseWriter, http.StatusOK, responseEnvelope{Ok: false, Data: nil, Error: compileFailure.Message})
		return
	}
	if processError != nil {
		if errors.Is(processError, context.DeadlineExceeded) {
			writeJson(responseWriter, http.StatusGatewayTimeout, responseEnvelope{Ok: false, Data: nil, Error: "compile timed out"})
			return
		}
		writeJson(responseWriter, http.StatusInternalServerError, responseEnvelope{Ok: false, Data: nil, Error: processError.Error()})
		return
	}

	writeJson(responseWriter, http.StatusOK, responseEnvelope{Ok: true, Data: compileResult, Error: ""})
}

// handlePdf serves the cached PDF artifact for a document identifier.
func (application *application) handlePdf(responseWriter http.ResponseWriter, request *http.Request) {
	documentId := request.PathValue("id")
	if !isValidDocumentId(documentId) {
		http.NotFound(responseWriter, request)
		return
	}

	pdfFilePath := filepath.Join(application.resultsDirectory, documentId, "main.pdf")
	application.serveStaticFile(responseWriter, request, pdfFilePath, "application/pdf")
}

// handlePng serves a specific PNG page for a document identifier.
func (application *application) handlePng(responseWriter http.ResponseWriter, request *http.Request) {
	documentId := request.PathValue("id")
	if !isValidDocumentId(documentId) {
		http.NotFound(responseWriter, request)
		return
	}

	pageNumber, parseError := strconv.Atoi(request.PathValue("pageNumber"))
	if parseError != nil || pageNumber <= 0 {
		http.NotFound(responseWriter, request)
		return
	}

	pngFilePath := filepath.Join(application.resultsDirectory, documentId, fmt.Sprintf("%d.png", pageNumber))
	application.serveStaticFile(responseWriter, request, pngFilePath, "image/png")
}

// serveStaticFile returns a cached artifact with a one-year immutable cache
// policy. Missing files become 404 responses, while unexpected filesystem
// errors are treated as internal server errors.
func (application *application) serveStaticFile(responseWriter http.ResponseWriter, request *http.Request, filePath string, contentType string) {
	if _, statError := os.Stat(filePath); statError != nil {
		if errors.Is(statError, os.ErrNotExist) {
			http.NotFound(responseWriter, request)
			return
		}
		writeJson(responseWriter, http.StatusInternalServerError, responseEnvelope{Ok: false, Data: nil, Error: "internal server error"})
		return
	}

	responseWriter.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	responseWriter.Header().Set("Content-Type", contentType)
	http.ServeFile(responseWriter, request, filePath)
}

// authorizeRequest accepts only Authorization headers in Bearer form and
// checks the token against the configured in-memory API key set.
func (application *application) authorizeRequest(request *http.Request) (string, bool) {
	authorizationHeader := strings.TrimSpace(request.Header.Get("Authorization"))
	if authorizationHeader == "" {
		return "", false
	}

	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authorizationHeader, bearerPrefix) {
		return "", false
	}

	apiKey := strings.TrimSpace(strings.TrimPrefix(authorizationHeader, bearerPrefix))
	if apiKey == "" {
		return "", false
	}

	_, authorized := application.allowedApiKeys[apiKey]
	return apiKey, authorized
}

// isValidDocumentId enforces the sha256-hex identifier format used by the
// compiler so the file-serving endpoints cannot escape the results directory.
func isValidDocumentId(documentId string) bool {
	return documentIdPattern.MatchString(documentId)
}

// writeJson serializes the response envelope and sets the HTTP status code.
func writeJson(responseWriter http.ResponseWriter, statusCode int, payload responseEnvelope) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(payload)
}

var executableLookup = exec.LookPath

// verifyRequiredExecutables fails fast at startup if one of the required
// external tools is missing from PATH.
func verifyRequiredExecutables() error {
	for _, executableName := range []string{"xelatex", "pdfinfo", "pdftoppm"} {
		if _, lookupError := executableLookup(executableName); lookupError != nil {
			return fmt.Errorf("required executable %q not found in PATH", executableName)
		}
	}
	return nil
}
