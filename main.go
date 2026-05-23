package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	configFilePath       = "config.json"
	statisticsFilePath   = "statistics.json"
	resultsDirectoryPath = "results"
	temporaryRootPath    = "/tmp/simple-latex-container"
)

// main is intentionally thin so startup orchestration can stay testable inside
// runApplication instead of being trapped in process-global side effects.
func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	if runError := runApplication(logger); runError != nil {
		logger.Printf("fatal: %v", runError)
		os.Exit(1)
	}
}

// runApplication loads configuration, verifies external dependencies, starts
// background persistence, serves HTTP requests, and performs graceful shutdown.
func runApplication(logger *log.Logger) error {
	config, configError := loadConfig(configFilePath)
	if configError != nil {
		return configError
	}

	if executableError := verifyRequiredExecutables(); executableError != nil {
		return executableError
	}

	statisticsStore, statisticsError := loadStatistics(statisticsFilePath, config.ApiKeys)
	if statisticsError != nil {
		return statisticsError
	}

	if makeDirectoryError := os.MkdirAll(resultsDirectoryPath, 0o755); makeDirectoryError != nil {
		return makeDirectoryError
	}
	if makeDirectoryError := os.MkdirAll(temporaryRootPath, 0o755); makeDirectoryError != nil {
		return makeDirectoryError
	}

	rootContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	go statisticsStore.runAutoSave(rootContext, 5*time.Minute, logger)

	commandRunner := execCommandRunner{}
	compileProcessor := newCompilerService(
		resultsDirectoryPath,
		temporaryRootPath,
		config.PdfToPngDpi,
		commandRunner,
		pdfInfoInspector{commandRunner: commandRunner},
	)
	application := newApplication(config, statisticsStore, compileProcessor, resultsDirectoryPath)

	httpServer := &http.Server{
		Addr:    config.Listen,
		Handler: application.routes(),
	}

	serverErrorChannel := make(chan error, 1)
	go func() {
		listenError := httpServer.ListenAndServe()
		if listenError != nil && !errors.Is(listenError, http.ErrServerClosed) {
			serverErrorChannel <- listenError
			return
		}
		serverErrorChannel <- nil
	}()

	select {
	case serverError := <-serverErrorChannel:
		if serverError != nil {
			return serverError
		}
	case <-rootContext.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	if shutdownError := httpServer.Shutdown(shutdownContext); shutdownError != nil && !errors.Is(shutdownError, http.ErrServerClosed) {
		return shutdownError
	}

	if saveError := statisticsStore.save(); saveError != nil {
		return saveError
	}

	return nil
}
