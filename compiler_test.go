package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	mutex sync.Mutex
	calls []string
	args  map[string][][]string
	run   func(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error)
}

func (runner *fakeCommandRunner) runCombinedOutput(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error) {
	runner.mutex.Lock()
	runner.calls = append(runner.calls, executableName)
	if runner.args == nil {
		runner.args = make(map[string][][]string)
	}
	argumentCopy := append([]string(nil), executableArguments...)
	runner.args[executableName] = append(runner.args[executableName], argumentCopy)
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

	compileService := newCompilerService(resultsDirectory, temporaryRoot, 450, 0, commandRunner, fakePdfInspector{pageCount: 2})

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
	if result.CacheHit {
		testingContext.Fatal("expected first compile result to report cacheHit=false")
	}

	expectedPdftoppmArguments := []string{"-r", "450", "-png", "main.pdf", "page"}
	if !reflect.DeepEqual(commandRunner.args["pdftoppm"][0], expectedPdftoppmArguments) {
		testingContext.Fatalf("unexpected pdftoppm args: got %v want %v", commandRunner.args["pdftoppm"][0], expectedPdftoppmArguments)
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
	if !result.CacheHit {
		testingContext.Fatal("expected cached compile result to report cacheHit=true")
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

	compileService := newCompilerService(resultsDirectory, temporaryRoot, 450, 0, commandRunner, fakePdfInspector{pageCount: 1})
	_, compileFailure, processError := compileService.process(context.Background(), "bad")
	if processError != nil {
		testingContext.Fatalf("unexpected internal error: %v", processError)
	}
	if compileFailure == nil || compileFailure.Message != "raw xelatex error" {
		testingContext.Fatalf("unexpected compile failure: %+v", compileFailure)
	}
}

func TestCompilerProcessLimitsGlobalConcurrentCompiles(testingContext *testing.T) {
	resultsDirectory := filepath.Join(testingContext.TempDir(), "results")
	temporaryRoot := filepath.Join(testingContext.TempDir(), "temporary")
	if makeDirectoryError := os.MkdirAll(resultsDirectory, 0o755); makeDirectoryError != nil {
		testingContext.Fatal(makeDirectoryError)
	}
	if makeDirectoryError := os.MkdirAll(temporaryRoot, 0o755); makeDirectoryError != nil {
		testingContext.Fatal(makeDirectoryError)
	}

	commandRunner := &fakeCommandRunner{}
	releaseCompile := make(chan struct{})
	compileStarted := make(chan string, 2)
	var activeCompiles int32
	var maxActiveCompiles int32

	commandRunner.run = func(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error) {
		_ = executableArguments

		switch executableName {
		case "xelatex":
			currentActiveCompiles := atomic.AddInt32(&activeCompiles, 1)
			for {
				observedMax := atomic.LoadInt32(&maxActiveCompiles)
				if currentActiveCompiles <= observedMax || atomic.CompareAndSwapInt32(&maxActiveCompiles, observedMax, currentActiveCompiles) {
					break
				}
			}

			compileStarted <- filepath.Base(workingDirectory)
			select {
			case <-releaseCompile:
			case <-requestContext.Done():
				atomic.AddInt32(&activeCompiles, -1)
				return nil, requestContext.Err()
			}
			atomic.AddInt32(&activeCompiles, -1)
			return []byte("ok"), os.WriteFile(filepath.Join(workingDirectory, "main.pdf"), []byte("pdf"), 0o644)
		case "pdftoppm":
			if writeError := os.WriteFile(filepath.Join(workingDirectory, "page-1.png"), []byte("png"), 0o644); writeError != nil {
				return nil, writeError
			}
			return []byte("ok"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", executableName)
		}
	}

	compileService := newCompilerService(resultsDirectory, temporaryRoot, 450, 1, commandRunner, fakePdfInspector{pageCount: 1})

	resultsChannel := make(chan error, 2)
	for _, latexPayload := range []string{"first", "second"} {
		go func(payload string) {
			_, _, processError := compileService.process(context.Background(), payload)
			resultsChannel <- processError
		}(latexPayload)
	}

	select {
	case <-compileStarted:
	case <-time.After(2 * time.Second):
		testingContext.Fatal("expected first compile to start")
	}

	select {
	case <-compileStarted:
		testingContext.Fatal("second compile started before the first one released the global slot")
	case <-time.After(150 * time.Millisecond):
	}

	releaseCompile <- struct{}{}

	select {
	case <-compileStarted:
	case <-time.After(2 * time.Second):
		testingContext.Fatal("expected second compile to start after the first slot was released")
	}

	releaseCompile <- struct{}{}

	for range 2 {
		if processError := <-resultsChannel; processError != nil {
			testingContext.Fatalf("process returned error: %v", processError)
		}
	}

	if actualValue := atomic.LoadInt32(&maxActiveCompiles); actualValue != 1 {
		testingContext.Fatalf("unexpected max active compiles: got %d want 1", actualValue)
	}
}

func calculateSha256Hex(inputText string) string {
	hashSum := sha256.Sum256([]byte(inputText))
	return hex.EncodeToString(hashSum[:])
}
