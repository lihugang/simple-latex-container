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
)

type fakeCompiler struct {
	result  CompileResult
	failure *CompileFailure
	err     error
	payload string
}

func (f *fakeCompiler) Process(ctx context.Context, payload string) (CompileResult, *CompileFailure, error) {
	_ = ctx
	f.payload = payload
	return f.result, f.failure, f.err
}

func TestHandleCodeSuccess(t *testing.T) {
	dir := t.TempDir()
	stats, err := LoadStatistics(filepath.Join(dir, "statistics.json"), []string{"secret"})
	if err != nil {
		t.Fatal(err)
	}

	compiler := &fakeCompiler{result: CompileResult{ID: "abc", PageNum: 2}}
	app := NewApp(Config{APIKeys: []string{"secret"}}, stats, compiler, filepath.Join(dir, "results"))

	req := httptest.NewRequest(http.MethodPost, "/code", bytes.NewBufferString(`{"payload":"\\documentclass{article}"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	app.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var resp responseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok response, got %+v", resp)
	}
	if compiler.payload == "" {
		t.Fatal("expected compiler to be called")
	}
	if got, want := stats.counts["secret"].Count, uint64(1); got != want {
		t.Fatalf("unexpected count: got %d want %d", got, want)
	}
}

func TestHandleCodeUnauthorized(t *testing.T) {
	dir := t.TempDir()
	stats, err := LoadStatistics(filepath.Join(dir, "statistics.json"), []string{"secret"})
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp(Config{APIKeys: []string{"secret"}}, stats, &fakeCompiler{}, filepath.Join(dir, "results"))
	req := httptest.NewRequest(http.MethodPost, "/code", bytes.NewBufferString(`{"payload":"x"}`))
	rec := httptest.NewRecorder()

	app.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := stats.counts["secret"].Count; got != 0 {
		t.Fatalf("expected stats to remain unchanged, got %d", got)
	}
}

func TestStaticFileHandlers(t *testing.T) {
	dir := t.TempDir()
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	resultDir := filepath.Join(dir, "results", id)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "main.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "1.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := LoadStatistics(filepath.Join(dir, "statistics.json"), []string{"secret"})
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp(Config{APIKeys: []string{"secret"}}, stats, &fakeCompiler{}, filepath.Join(dir, "results"))

	pdfReq := httptest.NewRequest(http.MethodGet, "/"+id+"/pdf", nil)
	pdfRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(pdfRec, pdfReq)
	if pdfRec.Code != http.StatusOK {
		t.Fatalf("unexpected pdf status: %d", pdfRec.Code)
	}
	if got := pdfRec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected cache-control: %q", got)
	}

	pngReq := httptest.NewRequest(http.MethodGet, "/"+id+"/png/1", nil)
	pngRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(pngRec, pngReq)
	if pngRec.Code != http.StatusOK {
		t.Fatalf("unexpected png status: %d", pngRec.Code)
	}
}
