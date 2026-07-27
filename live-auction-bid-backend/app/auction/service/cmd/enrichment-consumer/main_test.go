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

func TestEnrichmentMySQLDSNEnforcesSafeSettings(t *testing.T) {
	dsn, err := enrichmentMySQLDSN("user:pass@tcp(mysql:3306)/auction")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "parseTime=true") || !strings.Contains(dsn, "rejectReadOnly=true") {
		t.Fatalf("safe DSN flags missing: %s", dsn)
	}
	if _, err := enrichmentMySQLDSN("user:pass@tcp(mysql:3306)/?multiStatements=true"); err == nil {
		t.Fatal("DSN without database and with multiStatements was accepted")
	}
}

func TestEnrichmentConfigurationRejectsInvalidBounds(t *testing.T) {
	values := map[string]string{
		"AUCTION_ENRICHMENT_SESSION_TIMEOUT":    "3s",
		"AUCTION_ENRICHMENT_HEARTBEAT_INTERVAL": "3s",
	}
	getenv := func(key string) string { return values[key] }
	if _, err := kafkaConfigFromEnv(getenv); err == nil {
		t.Fatal("heartbeat equal to session timeout was accepted")
	}
	values = map[string]string{
		"AUCTION_ENRICHMENT_RETRY_BASE": "5s",
		"AUCTION_ENRICHMENT_RETRY_MAX":  "1s",
	}
	if _, err := consumerConfigFromEnv(getenv); err == nil {
		t.Fatal("retry max below base was accepted")
	}
}

func TestEnrichmentOperationsReadiness(t *testing.T) {
	runner := &stubRunner{ready: true}
	var running atomic.Bool
	running.Store(true)
	handler := operationsHandler(&running, runner)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "enrichment-consumer") {
		t.Fatalf("ready response = %d %s", response.Code, response.Body.String())
	}
	runner.ready = false
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready response = %d", response.Code)
	}
	post := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, post)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST health response = %d", response.Code)
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
