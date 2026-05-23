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

type fakeCommandRunner struct {
	mutex sync.Mutex
	calls []string
	run   func(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error)
}

func (runner *fakeCommandRunner) runCombinedOutput(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error) {
	runner.mutex.Lock()
	runner.calls = append(runner.calls, executableName)
	runner.mutex.Unlock()
	return runner.run(requestContext, workingDirectory, executableName, executableArguments...)
}

type fakePdfInspector struct {
	pageCount int
	readError error
}

func (inspector fakePdfInspector) readPageCount(requestContext context.Context, pdfFilePath string) (int, error) {
	_ = requestContext
	_ = pdfFilePath
	if inspector.readError != nil {
		return 0, inspector.readError
	}
	return inspector.pageCount, nil
}

func TestCompilerProcessCompilesAndCaches(testingContext *testing.T) {
	resultsDirectory := filepath.Join(testingContext.TempDir(), "results")
	temporaryRoot := filepath.Join(testingContext.TempDir(), "temporary")
	if makeDirectoryError := os.MkdirAll(resultsDirectory, 0o755); makeDirectoryError != nil {
		testingContext.Fatal(makeDirectoryError)
	}
	if makeDirectoryError := os.MkdirAll(temporaryRoot, 0o755); makeDirectoryError != nil {
		testingContext.Fatal(makeDirectoryError)
	}

	commandRunner := &fakeCommandRunner{}
	commandRunner.run = func(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error) {
		_ = requestContext
		_ = executableArguments

		switch executableName {
		case "xelatex":
			return []byte("ok"), os.WriteFile(filepath.Join(workingDirectory, "main.pdf"), []byte("pdf"), 0o644)
		case "pdftoppm":
			for pageNumber := 1; pageNumber <= 2; pageNumber++ {
				if writeError := os.WriteFile(filepath.Join(workingDirectory, fmt.Sprintf("page-%d.png", pageNumber)), []byte("png"), 0o644); writeError != nil {
					return nil, writeError
				}
			}
			return []byte("ok"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", executableName)
		}
	}

	compileService := newCompilerService(resultsDirectory, temporaryRoot, commandRunner, fakePdfInspector{pageCount: 2})

	latexPayload := "\\documentclass{article}\\begin{document}Hello\\end{document}"
	result, compileFailure, processError := compileService.process(context.Background(), latexPayload)
	if processError != nil {
		testingContext.Fatalf("process returned error: %v", processError)
	}
	if compileFailure != nil {
		testingContext.Fatalf("unexpected compile failure: %+v", compileFailure)
	}

	expectedDocumentId := calculateSha256Hex(latexPayload)
	if result.Id != expectedDocumentId {
		testingContext.Fatalf("unexpected id: got %q want %q", result.Id, expectedDocumentId)
	}
	if result.PageNumber != 2 {
		testingContext.Fatalf("unexpected page count: %d", result.PageNumber)
	}

	for _, relativePath := range []string{"main.tex", "main.pdf", "1.png", "2.png"} {
		if _, statError := os.Stat(filepath.Join(resultsDirectory, expectedDocumentId, relativePath)); statError != nil {
			testingContext.Fatalf("expected result file %s: %v", relativePath, statError)
		}
	}

	if _, statError := os.Stat(filepath.Join(temporaryRoot, expectedDocumentId)); !os.IsNotExist(statError) {
		testingContext.Fatalf("expected temp dir cleanup, got err=%v", statError)
	}

	callCountBeforeCacheHit := len(commandRunner.calls)
	result, compileFailure, processError = compileService.process(context.Background(), latexPayload)
	if processError != nil || compileFailure != nil {
		testingContext.Fatalf("cached process returned err=%v failure=%+v", processError, compileFailure)
	}
	if len(commandRunner.calls) != callCountBeforeCacheHit {
		testingContext.Fatalf("expected cached request to avoid external commands, calls before=%d after=%d", callCountBeforeCacheHit, len(commandRunner.calls))
	}
	if result.PageNumber != 2 {
		testingContext.Fatalf("unexpected cached page count: %d", result.PageNumber)
	}
}

func TestCompilerProcessReturnsCompileFailure(testingContext *testing.T) {
	resultsDirectory := filepath.Join(testingContext.TempDir(), "results")
	temporaryRoot := filepath.Join(testingContext.TempDir(), "temporary")

	commandRunner := &fakeCommandRunner{}
	commandRunner.run = func(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error) {
		_ = requestContext
		_ = workingDirectory
		_ = executableArguments

		if executableName == "xelatex" {
			return []byte("raw xelatex error"), fmt.Errorf("exit status 1")
		}

		testingContext.Fatalf("unexpected command: %s", executableName)
		return nil, nil
	}

	compileService := newCompilerService(resultsDirectory, temporaryRoot, commandRunner, fakePdfInspector{pageCount: 1})
	_, compileFailure, processError := compileService.process(context.Background(), "bad")
	if processError != nil {
		testingContext.Fatalf("unexpected internal error: %v", processError)
	}
	if compileFailure == nil || compileFailure.Message != "raw xelatex error" {
		testingContext.Fatalf("unexpected compile failure: %+v", compileFailure)
	}
}

func calculateSha256Hex(inputText string) string {
	hashSum := sha256.Sum256([]byte(inputText))
	return hex.EncodeToString(hashSum[:])
}
