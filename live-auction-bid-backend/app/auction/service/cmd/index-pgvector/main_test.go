package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOperationsHandlerReportsReadiness(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	runner := &stubRunner{ready: true}
	handler := operationsHandler(&running, runner)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("ready response=%d body=%s", response.Code, response.Body.String())
	}
	runner.ready = false
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready response=%d", response.Code)
	}
}

func TestVectorConfigValidation(t *testing.T) {
	getenv := func(key string) string {
		if key == "AUCTION_VECTOR_HEARTBEAT_INTERVAL" {
			return "30s"
		}
		return ""
	}
	if _, err := kafkaConfigFromEnv(getenv); err == nil {
		t.Fatal("heartbeat equal to session timeout was accepted")
	}
}

func TestRunServiceFailsWhenConsumerStopsUnexpectedly(t *testing.T) {
	err := runService(context.Background(), &stubRunner{ready: true}, "127.0.0.1:0", slog.Default())
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("error=%v", err)
	}
}

type stubRunner struct{ ready bool }

func (*stubRunner) Run(context.Context) error { return nil }
func (runner *stubRunner) Ready() bool        { return runner.ready }
