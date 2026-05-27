package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// compileResult is the successful payload returned by POST /code.
// The document identifier is derived from the exact LaTeX source so the same
// input always maps to the same storage directory and cache key.
type compileResult struct {
	Id         string `json:"id"`
	PageNumber int    `json:"pageNumber"`
	CacheHit   bool   `json:"cacheHit"`
}

// compileFailure represents a user-visible LaTeX compilation failure.
// It is separated from internal errors because xelatex failures are part of the
// API contract and should be returned as business-level JSON failures.
type compileFailure struct {
	Message string
}

// compileProcessor abstracts the document processing pipeline so HTTP handler
// tests can inject a fake implementation without executing external commands.
type compileProcessor interface {
	process(requestContext context.Context, latexPayload string) (compileResult, *compileFailure, error)
}

// commandRunner wraps external command execution. Tests replace this interface
// to simulate xelatex, pdfinfo, and pdftoppm behavior deterministically.
type commandRunner interface {
	runCombinedOutput(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error)
}

// pdfInspector extracts metadata from a compiled PDF file.
// The current service only needs page count, but keeping the capability behind
// an interface avoids coupling the compiler pipeline directly to pdfinfo.
type pdfInspector interface {
	readPageCount(requestContext context.Context, pdfFilePath string) (int, error)
}

// execCommandRunner executes real operating-system commands through os/exec.
type execCommandRunner struct{}

// runCombinedOutput executes a command in the requested working directory and
// returns combined stdout and stderr so callers can preserve raw tool output.
func (execCommandRunner) runCombinedOutput(requestContext context.Context, workingDirectory string, executableName string, executableArguments ...string) ([]byte, error) {
	command := exec.CommandContext(requestContext, executableName, executableArguments...)
	command.Dir = workingDirectory
	return command.CombinedOutput()
}

// pdfInfoInspector reads page count information from the pdfinfo command.
type pdfInfoInspector struct {
	commandRunner commandRunner
}

// readPageCount parses the `Pages:` line from pdfinfo output and validates that
// the reported page count is a positive integer.
func (inspector pdfInfoInspector) readPageCount(requestContext context.Context, pdfFilePath string) (int, error) {
	commandOutput, commandError := inspector.commandRunner.runCombinedOutput(requestContext, "", "pdfinfo", pdfFilePath)
	if commandError != nil {
		return 0, fmt.Errorf("run pdfinfo: %w: %s", commandError, strings.TrimSpace(string(commandOutput)))
	}

	for _, outputLine := range strings.Split(string(commandOutput), "\n") {
		if !strings.HasPrefix(outputLine, "Pages:") {
			continue
		}

		pageCountText := strings.TrimSpace(strings.TrimPrefix(outputLine, "Pages:"))
		pageCount, parseError := strconv.Atoi(pageCountText)
		if parseError != nil {
			return 0, fmt.Errorf("parse pdfinfo pages value %q: %w", pageCountText, parseError)
		}
		if pageCount <= 0 {
			return 0, fmt.Errorf("pdfinfo returned invalid page count %d", pageCount)
		}

		return pageCount, nil
	}

	return 0, errors.New("pdfinfo output missing Pages field")
}

// compilerService owns the full document pipeline: cache lookup, temporary
// compilation, PDF inspection, PNG conversion, and atomic publication into the
// permanent results directory.
type compilerService struct {
	resultsDirectory string
	temporaryRoot    string
	pdfToPngDpi      int
	compileSlots     chan struct{}
	commandRunner    commandRunner
	pdfInspector     pdfInspector
	keyedLocker      *documentKeyLocker
}

// newCompilerService constructs a compiler with all dependencies provided
// explicitly so the pipeline can be tested without shelling out.

func newCompilerService(resultsDirectory string, temporaryRoot string, pdfToPngDpi int, maxConcurrentCompiles int, commandRunner commandRunner, pdfInspector pdfInspector) *compilerService {
	var compileSlots chan struct{}
	if maxConcurrentCompiles > 0 {
		compileSlots = make(chan struct{}, maxConcurrentCompiles)
	}

	return &compilerService{
		resultsDirectory: resultsDirectory,
		temporaryRoot:    temporaryRoot,
		pdfToPngDpi:      pdfToPngDpi,
		compileSlots:     compileSlots,
		commandRunner:    commandRunner,
		pdfInspector:     pdfInspector,
		keyedLocker:      newDocumentKeyLocker(),
	}
}

// process calculates the deterministic document identifier, serializes work for
// that identifier, returns cached output when complete artifacts already exist,
// and otherwise runs the full compile pipeline.
func (service *compilerService) process(requestContext context.Context, latexPayload string) (compileResult, *compileFailure, error) {
	hashSum := sha256.Sum256([]byte(latexPayload))
	documentId := hex.EncodeToString(hashSum[:])

	unlockDocument, lockError := service.keyedLocker.lockForDocument(requestContext, documentId)
	if lockError != nil {
		return compileResult{}, nil, lockError
	}
	defer unlockDocument()

	cachedResult, cacheHit, cacheError := service.loadCachedResult(requestContext, documentId)
	if cacheError != nil {
		return compileResult{}, nil, cacheError
	}
	if cacheHit {
		cachedResult.CacheHit = true
		return cachedResult, nil, nil
	}

	releaseCompileSlot, acquireError := service.acquireCompileSlot(requestContext)
	if acquireError != nil {
		return compileResult{}, nil, acquireError
	}
	defer releaseCompileSlot()

	return service.compileDocument(requestContext, documentId, latexPayload)
}

// acquireCompileSlot limits the number of concurrent cache-miss compilations.
// A nil release function means global compile limiting is disabled.
func (service *compilerService) acquireCompileSlot(requestContext context.Context) (func(), error) {
	if service.compileSlots == nil {
		return func() {}, nil
	}

	select {
	case service.compileSlots <- struct{}{}:
		return func() {
			<-service.compileSlots
		}, nil
	case <-requestContext.Done():
		return nil, requestContext.Err()
	}
}

// loadCachedResult validates that the cached artifact set is complete before it
// is reused. The service refuses to trust a partial directory because previous
// failures or interrupted writes could otherwise leak broken output forever.
func (service *compilerService) loadCachedResult(requestContext context.Context, documentId string) (compileResult, bool, error) {
	resultDirectory := filepath.Join(service.resultsDirectory, documentId)
	pdfFilePath := filepath.Join(resultDirectory, "main.pdf")
	if _, statError := os.Stat(pdfFilePath); statError != nil {
		if os.IsNotExist(statError) {
			return compileResult{}, false, nil
		}
		return compileResult{}, false, fmt.Errorf("stat cached pdf: %w", statError)
	}

	pageCount, pageCountError := service.pdfInspector.readPageCount(requestContext, pdfFilePath)
	if pageCountError != nil {
		return compileResult{}, false, nil
	}

	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		pngFilePath := filepath.Join(resultDirectory, fmt.Sprintf("%d.png", pageNumber))
		if _, statError := os.Stat(pngFilePath); statError != nil {
			if os.IsNotExist(statError) {
				return compileResult{}, false, nil
			}
			return compileResult{}, false, fmt.Errorf("stat cached png %d: %w", pageNumber, statError)
		}
	}

	return compileResult{Id: documentId, PageNumber: pageCount}, true, nil
}

// compileDocument performs the full compile pipeline inside a temporary
// directory, then copies the completed artifact set into a staging directory,
// and finally renames the stage into the permanent result directory.
func (service *compilerService) compileDocument(requestContext context.Context, documentId string, latexPayload string) (compileResult, *compileFailure, error) {
	temporaryDirectory := filepath.Join(service.temporaryRoot, documentId)
	if removeError := os.RemoveAll(temporaryDirectory); removeError != nil {
		return compileResult{}, nil, fmt.Errorf("clear temp dir: %w", removeError)
	}
	if makeDirectoryError := os.MkdirAll(temporaryDirectory, 0o755); makeDirectoryError != nil {
		return compileResult{}, nil, fmt.Errorf("create temp dir: %w", makeDirectoryError)
	}
	defer os.RemoveAll(temporaryDirectory)

	temporaryTexFilePath := filepath.Join(temporaryDirectory, "main.tex")
	if writeError := os.WriteFile(temporaryTexFilePath, []byte(latexPayload), 0o644); writeError != nil {
		return compileResult{}, nil, fmt.Errorf("write tex file: %w", writeError)
	}

	latexCommandOutput, latexCommandError := service.commandRunner.runCombinedOutput(
		requestContext,
		temporaryDirectory,
		"xelatex",
		"-interaction=nonstopmode",
		"-no-shell-escape",
		"main.tex",
	)
	if latexCommandError != nil {
		return compileResult{}, &compileFailure{Message: string(latexCommandOutput)}, nil
	}

	temporaryPdfFilePath := filepath.Join(temporaryDirectory, "main.pdf")
	pageCount, pageCountError := service.pdfInspector.readPageCount(requestContext, temporaryPdfFilePath)
	if pageCountError != nil {
		return compileResult{}, nil, fmt.Errorf("inspect compiled pdf: %w", pageCountError)
	}

	conversionOutput, conversionError := service.commandRunner.runCombinedOutput(
		requestContext,
		temporaryDirectory,
		"pdftoppm",
		"-r",
		strconv.Itoa(service.pdfToPngDpi),
		"-png",
		"main.pdf",
		"page",
	)
	if conversionError != nil {
		return compileResult{}, nil, fmt.Errorf("convert pdf to png: %w: %s", conversionError, strings.TrimSpace(string(conversionOutput)))
	}

	stagingDirectory := filepath.Join(service.resultsDirectory, "."+documentId+".stage."+strconv.FormatInt(time.Now().UnixNano(), 10))
	if makeDirectoryError := os.MkdirAll(stagingDirectory, 0o755); makeDirectoryError != nil {
		return compileResult{}, nil, fmt.Errorf("create stage dir: %w", makeDirectoryError)
	}

	shouldDeleteStageDirectory := true
	defer func() {
		if shouldDeleteStageDirectory {
			_ = os.RemoveAll(stagingDirectory)
		}
	}()

	if copyError := copyFile(temporaryTexFilePath, filepath.Join(stagingDirectory, "main.tex")); copyError != nil {
		return compileResult{}, nil, fmt.Errorf("copy tex file: %w", copyError)
	}
	if copyError := copyFile(temporaryPdfFilePath, filepath.Join(stagingDirectory, "main.pdf")); copyError != nil {
		return compileResult{}, nil, fmt.Errorf("copy pdf file: %w", copyError)
	}

	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		temporaryPngFilePath := filepath.Join(temporaryDirectory, fmt.Sprintf("page-%d.png", pageNumber))
		publishedPngFilePath := filepath.Join(stagingDirectory, fmt.Sprintf("%d.png", pageNumber))
		if copyError := copyFile(temporaryPngFilePath, publishedPngFilePath); copyError != nil {
			return compileResult{}, nil, fmt.Errorf("copy png page %d: %w", pageNumber, copyError)
		}
	}

	finalResultDirectory := filepath.Join(service.resultsDirectory, documentId)
	if makeDirectoryError := os.MkdirAll(service.resultsDirectory, 0o755); makeDirectoryError != nil {
		return compileResult{}, nil, fmt.Errorf("create results dir: %w", makeDirectoryError)
	}
	if removeError := os.RemoveAll(finalResultDirectory); removeError != nil {
		return compileResult{}, nil, fmt.Errorf("clear result dir: %w", removeError)
	}
	if renameError := os.Rename(stagingDirectory, finalResultDirectory); renameError != nil {
		return compileResult{}, nil, fmt.Errorf("publish result dir: %w", renameError)
	}
	shouldDeleteStageDirectory = false

	return compileResult{Id: documentId, PageNumber: pageCount, CacheHit: false}, nil, nil
}

// copyFile copies one artifact file from source to destination without trying
// to interpret its contents. The compiler uses it for both text and binary
// files after the temporary build has completed successfully.
func copyFile(sourceFilePath string, destinationFilePath string) error {
	sourceFile, openError := os.Open(sourceFilePath)
	if openError != nil {
		return openError
	}
	defer sourceFile.Close()

	destinationFile, createError := os.Create(destinationFilePath)
	if createError != nil {
		return createError
	}

	if _, copyError := io.Copy(destinationFile, sourceFile); copyError != nil {
		_ = destinationFile.Close()
		return copyError
	}
	if closeError := destinationFile.Close(); closeError != nil {
		return closeError
	}

	return nil
}

// documentKeyLocker provides a mutex per document identifier so only one
// goroutine compiles or publishes artifacts for the same LaTeX payload at once.
type documentKeyLocker struct {
	mutex       sync.Mutex
	lockEntries map[string]*documentLockEntry
}

// documentLockEntry stores the actual lock and a reference counter so unused
// per-document lock entries can be discarded when no goroutine still references them.
type documentLockEntry struct {
	lockChannel    chan struct{}
	referenceCount int
}

// newDocumentKeyLocker creates the per-document lock registry.
func newDocumentKeyLocker() *documentKeyLocker {
	return &documentKeyLocker{lockEntries: make(map[string]*documentLockEntry)}
}

// lockForDocument returns an unlock function for the given document identifier.
// The outer registry mutex protects creation and cleanup of lock entries, while
// the inner mutex serializes work for one specific document. Waiting for the
// per-document lock is cancellable through the provided context.
func (locker *documentKeyLocker) lockForDocument(requestContext context.Context, documentId string) (func(), error) {
	locker.mutex.Lock()
	lockEntry := locker.lockEntries[documentId]
	if lockEntry == nil {
		lockEntry = &documentLockEntry{lockChannel: make(chan struct{}, 1)}
		lockEntry.lockChannel <- struct{}{}
		locker.lockEntries[documentId] = lockEntry
	}
	lockEntry.referenceCount++
	locker.mutex.Unlock()

	select {
	case <-lockEntry.lockChannel:
	case <-requestContext.Done():
		locker.mutex.Lock()
		lockEntry.referenceCount--
		if lockEntry.referenceCount == 0 {
			delete(locker.lockEntries, documentId)
		}
		locker.mutex.Unlock()
		return nil, requestContext.Err()
	}

	return func() {
		lockEntry.lockChannel <- struct{}{}

		locker.mutex.Lock()
		defer locker.mutex.Unlock()

		lockEntry.referenceCount--
		if lockEntry.referenceCount == 0 {
			delete(locker.lockEntries, documentId)
		}
	}, nil
}
