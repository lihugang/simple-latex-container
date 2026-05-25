package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerAppliesConfiguredTimeouts(testingContext *testing.T) {
	httpServer := newHTTPServer(serviceConfig{Listen: ":8080", HttpTimeoutSeconds: 120}, http.NewServeMux())
	expectedTimeout := 2 * time.Minute

	if httpServer.ReadTimeout != expectedTimeout {
		testingContext.Fatalf("unexpected read timeout: %v", httpServer.ReadTimeout)
	}
	if httpServer.WriteTimeout != expectedTimeout {
		testingContext.Fatalf("unexpected write timeout: %v", httpServer.WriteTimeout)
	}
	if httpServer.ReadHeaderTimeout != expectedTimeout {
		testingContext.Fatalf("unexpected read header timeout: %v", httpServer.ReadHeaderTimeout)
	}
	if httpServer.IdleTimeout != expectedTimeout {
		testingContext.Fatalf("unexpected idle timeout: %v", httpServer.IdleTimeout)
	}
}

func TestNewHTTPServerLeavesTimeoutsUnsetWhenDisabled(testingContext *testing.T) {
	httpServer := newHTTPServer(serviceConfig{Listen: ":8080", HttpTimeoutSeconds: 0}, http.NewServeMux())

	if httpServer.ReadTimeout != 0 {
		testingContext.Fatalf("unexpected read timeout: %v", httpServer.ReadTimeout)
	}
	if httpServer.WriteTimeout != 0 {
		testingContext.Fatalf("unexpected write timeout: %v", httpServer.WriteTimeout)
	}
	if httpServer.ReadHeaderTimeout != 0 {
		testingContext.Fatalf("unexpected read header timeout: %v", httpServer.ReadHeaderTimeout)
	}
	if httpServer.IdleTimeout != 0 {
		testingContext.Fatalf("unexpected idle timeout: %v", httpServer.IdleTimeout)
	}
}
