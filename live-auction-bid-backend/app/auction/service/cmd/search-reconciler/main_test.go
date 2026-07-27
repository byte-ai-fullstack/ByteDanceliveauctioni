package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunnerConfigValidation(t *testing.T) {
	config, err := runnerConfigFromEnv(func(string) string { return "" })
	if err != nil || config.PageSize != 100 {
		t.Fatalf("config=%+v error=%v", config, err)
	}
	if _, err := runnerConfigFromEnv(func(key string) string {
		if key == "AUCTION_SEARCH_RECONCILE_PAGE_SIZE" {
			return "1001"
		}
		return ""
	}); err == nil {
		t.Fatal("oversized reconciliation page was accepted")
	}
}

func TestOperationsHandlerReportsReadiness(t *testing.T) {
	runner := &stubRunner{ready: true}
	handler := operationsHandler(runner)
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

func TestRunServiceStopsWhenRunnerReturns(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- runService(context.Background(), &stubRunner{}, "127.0.0.1:0", slog.Default())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runService: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runService kept the health server alive after its runner exited")
	}
}

type stubRunner struct{ ready bool }

func (*stubRunner) Run(context.Context) error { return nil }
func (runner *stubRunner) Ready() bool        { return runner.ready }
