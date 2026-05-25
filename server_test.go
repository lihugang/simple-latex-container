package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeCompileProcessor struct {
	result       compileResult
	failure      *compileFailure
	processError error
	payload      string
}

func (processor *fakeCompileProcessor) process(requestContext context.Context, latexPayload string) (compileResult, *compileFailure, error) {
	_ = requestContext
	processor.payload = latexPayload
	return processor.result, processor.failure, processor.processError
}

func TestHandleCodeSuccess(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	statisticsStore, loadError := loadStatistics(filepath.Join(temporaryDirectory, "statistics.json"), []string{"secret"})
	if loadError != nil {
		testingContext.Fatal(loadError)
	}

	compileProcessor := &fakeCompileProcessor{result: compileResult{Id: "abc", PageNumber: 2}}
	application := newApplication(serviceConfig{ApiKeys: []string{"secret"}}, statisticsStore, compileProcessor, filepath.Join(temporaryDirectory, "results"))

	request := httptest.NewRequest(http.MethodPost, "/code", bytes.NewBufferString(`{"payload":"\\documentclass{article}"}`))
	request.Header.Set("Authorization", "Bearer secret")
	responseRecorder := httptest.NewRecorder()

	application.routes().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		testingContext.Fatalf("unexpected status: %d", responseRecorder.Code)
	}

	var responseBody responseEnvelope
	if unmarshalError := json.Unmarshal(responseRecorder.Body.Bytes(), &responseBody); unmarshalError != nil {
		testingContext.Fatalf("unmarshal response: %v", unmarshalError)
	}
	if !responseBody.Ok {
		testingContext.Fatalf("expected ok response, got %+v", responseBody)
	}
	if compileProcessor.payload == "" {
		testingContext.Fatal("expected compiler to be called")
	}
	if actualValue, expectedValue := statisticsStore.counts["secret"].Count, uint64(1); actualValue != expectedValue {
		testingContext.Fatalf("unexpected count: got %d want %d", actualValue, expectedValue)
	}
}

func TestHandleCodeUnauthorized(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	statisticsStore, loadError := loadStatistics(filepath.Join(temporaryDirectory, "statistics.json"), []string{"secret"})
	if loadError != nil {
		testingContext.Fatal(loadError)
	}

	application := newApplication(serviceConfig{ApiKeys: []string{"secret"}}, statisticsStore, &fakeCompileProcessor{}, filepath.Join(temporaryDirectory, "results"))
	request := httptest.NewRequest(http.MethodPost, "/code", bytes.NewBufferString(`{"payload":"x"}`))
	responseRecorder := httptest.NewRecorder()

	application.routes().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusUnauthorized {
		testingContext.Fatalf("unexpected status: %d", responseRecorder.Code)
	}
	if actualValue := statisticsStore.counts["secret"].Count; actualValue != 0 {
		testingContext.Fatalf("expected stats to remain unchanged, got %d", actualValue)
	}
}

func TestStaticFileHandlers(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	documentId := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	resultDirectory := filepath.Join(temporaryDirectory, "results", documentId)
	if makeDirectoryError := os.MkdirAll(resultDirectory, 0o755); makeDirectoryError != nil {
		testingContext.Fatal(makeDirectoryError)
	}
	if writeError := os.WriteFile(filepath.Join(resultDirectory, "main.pdf"), []byte("pdf"), 0o644); writeError != nil {
		testingContext.Fatal(writeError)
	}
	if writeError := os.WriteFile(filepath.Join(resultDirectory, "1.png"), []byte("png"), 0o644); writeError != nil {
		testingContext.Fatal(writeError)
	}

	statisticsStore, loadError := loadStatistics(filepath.Join(temporaryDirectory, "statistics.json"), []string{"secret"})
	if loadError != nil {
		testingContext.Fatal(loadError)
	}

	application := newApplication(serviceConfig{ApiKeys: []string{"secret"}}, statisticsStore, &fakeCompileProcessor{}, filepath.Join(temporaryDirectory, "results"))

	pdfRequest := httptest.NewRequest(http.MethodGet, "/"+documentId+"/pdf", nil)
	pdfResponseRecorder := httptest.NewRecorder()
	application.routes().ServeHTTP(pdfResponseRecorder, pdfRequest)
	if pdfResponseRecorder.Code != http.StatusOK {
		testingContext.Fatalf("unexpected pdf status: %d", pdfResponseRecorder.Code)
	}
	if actualValue := pdfResponseRecorder.Header().Get("Cache-Control"); actualValue != "public, max-age=31536000, immutable" {
		testingContext.Fatalf("unexpected cache-control: %q", actualValue)
	}

	pngRequest := httptest.NewRequest(http.MethodGet, "/"+documentId+"/png/1", nil)
	pngResponseRecorder := httptest.NewRecorder()
	application.routes().ServeHTTP(pngResponseRecorder, pngRequest)
	if pngResponseRecorder.Code != http.StatusOK {
		testingContext.Fatalf("unexpected png status: %d", pngResponseRecorder.Code)
	}
}

func TestHandleCodeCompileTimeout(testingContext *testing.T) {
	temporaryDirectory := testingContext.TempDir()
	statisticsStore, loadError := loadStatistics(filepath.Join(temporaryDirectory, "statistics.json"), []string{"secret"})
	if loadError != nil {
		testingContext.Fatal(loadError)
	}

	blockingProcessor := compileProcessorFunc(func(requestContext context.Context, latexPayload string) (compileResult, *compileFailure, error) {
		_ = latexPayload
		<-requestContext.Done()
		return compileResult{}, nil, requestContext.Err()
	})

	application := newApplication(serviceConfig{ApiKeys: []string{"secret"}, CompileTimeoutSeconds: 1}, statisticsStore, blockingProcessor, filepath.Join(temporaryDirectory, "results"))

	request := httptest.NewRequest(http.MethodPost, "/code", bytes.NewBufferString(`{"payload":"\\documentclass{article}"}`))
	request.Header.Set("Authorization", "Bearer secret")
	responseRecorder := httptest.NewRecorder()

	startTime := time.Now()
	application.routes().ServeHTTP(responseRecorder, request)
	if elapsedTime := time.Since(startTime); elapsedTime < 900*time.Millisecond {
		testingContext.Fatalf("expected compile timeout to wait for context deadline, elapsed=%v", elapsedTime)
	}

	if responseRecorder.Code != http.StatusGatewayTimeout {
		testingContext.Fatalf("unexpected status: %d", responseRecorder.Code)
	}

	var responseBody responseEnvelope
	if unmarshalError := json.Unmarshal(responseRecorder.Body.Bytes(), &responseBody); unmarshalError != nil {
		testingContext.Fatalf("unmarshal response: %v", unmarshalError)
	}
	if responseBody.Error != "compile timed out" {
		testingContext.Fatalf("unexpected timeout error: %q", responseBody.Error)
	}
}

type compileProcessorFunc func(requestContext context.Context, latexPayload string) (compileResult, *compileFailure, error)

func (processor compileProcessorFunc) process(requestContext context.Context, latexPayload string) (compileResult, *compileFailure, error) {
	return processor(requestContext, latexPayload)
}
