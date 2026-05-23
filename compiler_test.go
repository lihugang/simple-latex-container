package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	run   func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
}

func (f *fakeRunner) CombinedOutput(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	return f.run(ctx, dir, name, args...)
}

type fakeInspector struct {
	pageCount int
	err       error
}

func (f fakeInspector) PageCount(ctx context.Context, pdfPath string) (int, error) {
	_ = ctx
	_ = pdfPath
	if f.err != nil {
		return 0, f.err
	}
	return f.pageCount, nil
}

func TestCompilerProcessCompilesAndCaches(t *testing.T) {
	resultsDir := filepath.Join(t.TempDir(), "results")
	tempRoot := filepath.Join(t.TempDir(), "tmp")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	runner.run = func(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
		_ = ctx
		switch name {
		case "xelatex":
			return []byte("ok"), os.WriteFile(filepath.Join(dir, "main.pdf"), []byte("pdf"), 0o644)
		case "pdftoppm":
			for page := 1; page <= 2; page++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("page-%d.png", page)), []byte("png"), 0o644); err != nil {
					return nil, err
				}
			}
			return []byte("ok"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
	}

	compiler := NewCompiler(resultsDir, tempRoot, runner, fakeInspector{pageCount: 2})

	payload := "\\documentclass{article}\\begin{document}Hello\\end{document}"
	result, failure, err := compiler.Process(context.Background(), payload)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if failure != nil {
		t.Fatalf("unexpected compile failure: %+v", failure)
	}

	expectedID := sha256Hex(payload)
	if result.ID != expectedID {
		t.Fatalf("unexpected id: got %q want %q", result.ID, expectedID)
	}
	if result.PageNum != 2 {
		t.Fatalf("unexpected page count: %d", result.PageNum)
	}

	for _, rel := range []string{"main.tex", "main.pdf", "1.png", "2.png"} {
		if _, err := os.Stat(filepath.Join(resultsDir, expectedID, rel)); err != nil {
			t.Fatalf("expected result file %s: %v", rel, err)
		}
	}

	if _, err := os.Stat(filepath.Join(tempRoot, expectedID)); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir cleanup, got err=%v", err)
	}

	before := len(runner.calls)
	result, failure, err = compiler.Process(context.Background(), payload)
	if err != nil || failure != nil {
		t.Fatalf("cached Process returned err=%v failure=%+v", err, failure)
	}
	if len(runner.calls) != before {
		t.Fatalf("expected cached request to avoid external commands, calls before=%d after=%d", before, len(runner.calls))
	}
	if result.PageNum != 2 {
		t.Fatalf("unexpected cached page count: %d", result.PageNum)
	}
}

func TestCompilerProcessReturnsCompileFailure(t *testing.T) {
	resultsDir := filepath.Join(t.TempDir(), "results")
	tempRoot := filepath.Join(t.TempDir(), "tmp")
	runner := &fakeRunner{}
	runner.run = func(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
		_ = ctx
		_ = dir
		_ = args
		if name == "xelatex" {
			return []byte("raw xelatex error"), fmt.Errorf("exit status 1")
		}
		t.Fatalf("unexpected command: %s", name)
		return nil, nil
	}

	compiler := NewCompiler(resultsDir, tempRoot, runner, fakeInspector{pageCount: 1})
	_, failure, err := compiler.Process(context.Background(), "bad")
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if failure == nil || failure.Message != "raw xelatex error" {
		t.Fatalf("unexpected compile failure: %+v", failure)
	}
}

func sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}
