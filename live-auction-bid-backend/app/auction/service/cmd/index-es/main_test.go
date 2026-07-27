package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestElasticsearchConfigValidation(t *testing.T) {
	getenv := func(key string) string {
		if key == "AUCTION_ES_HEARTBEAT_INTERVAL" {
			return "30s"
		}
		return ""
	}
	if _, err := kafkaConfigFromEnv(getenv); err == nil {
		t.Fatal("heartbeat equal to session timeout was accepted")
	}
}

func TestIndexMySQLDSNRejectsMultiStatements(t *testing.T) {
	if _, err := indexMySQLDSN("auction:secret@tcp(127.0.0.1:3306)/live_auction?multiStatements=true"); err == nil {
		t.Fatal("multiStatements DSN was accepted")
	}
	configured, err := indexMySQLDSN("auction:secret@tcp(127.0.0.1:3306)/live_auction")
	if err != nil || !strings.Contains(configured, "parseTime=true") || !strings.Contains(configured, "rejectReadOnly=true") {
		t.Fatalf("configured=%q error=%v", configured, err)
	}
}

func TestFindingDBPoolRejectsIdleAboveOpen(t *testing.T) {
	db, err := sql.Open("mysql", "auction:secret@tcp(127.0.0.1:3306)/live_auction")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	getenv := func(key string) string {
		if key == "AUCTION_ES_DB_MAX_OPEN_CONNS" {
			return "1"
		}
		if key == "AUCTION_ES_DB_MAX_IDLE_CONNS" {
			return "2"
		}
		return ""
	}
	if err := configureFindingDBPool(db, getenv); err == nil {
		t.Fatal("idle connections above max open were accepted")
	}
}

func TestConfigDefaultsAndBounds(t *testing.T) {
	getenv := func(string) string { return "" }
	esConfig, err := elasticsearchConfigFromEnv(getenv)
	if err != nil || esConfig.BaseURL != "http://127.0.0.1:19200" || esConfig.WriteAlias != "auction-lots-current" || esConfig.MaxResponseBytes != 1<<20 {
		t.Fatalf("config=%+v error=%v", esConfig, err)
	}
	consumerConfig, err := consumerConfigFromEnv(getenv)
	if err != nil || consumerConfig.MaxPollRecords != 100 || consumerConfig.RetryAttempts != 5 {
		t.Fatalf("consumer=%+v error=%v", consumerConfig, err)
	}
	invalid := func(key string) string {
		if key == "AUCTION_ES_RETRY_BASE" {
			return "2s"
		}
		if key == "AUCTION_ES_RETRY_MAX" {
			return "1s"
		}
		return ""
	}
	if _, err := consumerConfigFromEnv(invalid); err == nil {
		t.Fatal("retry max below base was accepted")
	}
	if _, err := intSetting(func(string) string { return "zero" }, "TEST_INT", 1); err == nil {
		t.Fatal("invalid integer was accepted")
	}
	if _, err := durationSetting(func(string) string { return "0s" }, "TEST_DURATION", time.Second); err == nil {
		t.Fatal("zero duration was accepted")
	}
}

func TestRunServiceFailsWhenConsumerStopsUnexpectedly(t *testing.T) {
	err := runService(context.Background(), &stubRunner{ready: true}, "127.0.0.1:0", slog.Default())
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("error=%v", err)
	}
}

func TestOperationsHandlerRejectsUnsafeMethod(t *testing.T) {
	var running atomic.Bool
	handler := operationsHandler(&running, &stubRunner{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status=%d headers=%v", response.Code, response.Header())
	}
}

type stubRunner struct{ ready bool }

func (*stubRunner) Run(context.Context) error { return nil }
func (runner *stubRunner) Ready() bool        { return runner.ready }
