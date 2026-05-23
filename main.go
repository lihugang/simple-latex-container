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

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	if err := run(logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	cfg, err := LoadConfig("config.json")
	if err != nil {
		return err
	}

	if err := verifyToolchain(); err != nil {
		return err
	}

	stats, err := LoadStatistics("statistics.json", cfg.APIKeys)
	if err != nil {
		return err
	}

	if err := os.MkdirAll("results", 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll("/tmp/simple-latex-container", 0o755); err != nil {
		return err
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go stats.RunAutoSave(rootCtx, 5*time.Minute, logger)

	runner := ExecCommandRunner{}
	compiler := NewCompiler("results", "/tmp/simple-latex-container", runner, PDFInfoInspector{Runner: runner})
	app := NewApp(cfg, stats, compiler, "results")

	server := &http.Server{
		Addr:    cfg.Listen,
		Handler: app.Routes(),
	}

	errCh := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-rootCtx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	if err := stats.Save(); err != nil {
		return err
	}

	return nil
}
