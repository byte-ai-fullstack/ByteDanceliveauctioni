package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/worker/domainrelay"
)

func TestDomainRelayConfigurationDefaultsAndValidation(t *testing.T) {
	config, err := relayConfigFromEnv(func(string) string { return "" }, "relay-1")
	if err != nil {
		t.Fatalf("relayConfigFromEnv: %v", err)
	}
	if config.ClaimLimit != 16 || config.Concurrency != 16 || config.LeaseTTL != 30*time.Second || config.OperationTimeout != 5*time.Second || config.MaxAttempts != 8 {
		t.Fatalf("config=%+v", config)
	}
	if _, err := relayConfigFromEnv(nil, "relay"); err == nil {
		t.Fatal("nil getenv was accepted")
	}
	if _, err := relayConfigFromEnv(func(key string) string {
		if key == "AUCTION_DOMAIN_RELAY_CLAIM_LIMIT" {
			return "0"
		}
		return ""
	}, "relay"); err == nil {
		t.Fatal("invalid claim limit was accepted")
	}
	if _, err := durationSetting(func(string) string { return "invalid" }, "DURATION", time.Second); err == nil {
		t.Fatal("invalid duration was accepted")
	}
}

func TestOrderReadyBusConfigurationIsOptionalOutsideProduction(t *testing.T) {
	if bus, err := orderReadyBusFromEnv(func(string) string { return "" }, "relay-1"); err != nil || bus != nil {
		t.Fatalf("development bus=%v error=%v", bus, err)
	}
	if _, err := orderReadyBusFromEnv(nil, "relay-1"); err == nil {
		t.Fatal("nil environment reader was accepted")
	}
	if _, err := orderReadyBusFromEnv(func(key string) string {
		if key == "AUCTION_ENV" {
			return "production"
		}
		return ""
	}, "relay-1"); err == nil {
		t.Fatal("production accepted a missing READY acceleration URL")
	}
	bus, err := orderReadyBusFromEnv(func(key string) string {
		if key == "AUCTION_ENV" {
			return "prod"
		}
		if key == "AUCTION_DOMAIN_RELAY_NATS_URLS" {
			return "nats://domain-relay:secret@nats:4222"
		}
		return ""
	}, "relay-1")
	if err != nil || bus == nil {
		t.Fatalf("production bus=%v error=%v", bus, err)
	}
}

func TestDomainRelayMySQLDSNEnforcesSafeSingleDatabase(t *testing.T) {
	dsn, err := domainRelayMySQLDSN("auction:secret@tcp(127.0.0.1:3306)/live_auction?parseTime=false")
	if err != nil {
		t.Fatalf("domainRelayMySQLDSN: %v", err)
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !config.ParseTime || !config.RejectReadOnly || config.MultiStatements || config.DBName != "live_auction" {
		t.Fatalf("configured DSN=%+v", config)
	}
	if _, err := domainRelayMySQLDSN("auction:secret@tcp(127.0.0.1:3306)/?multiStatements=true"); err == nil {
		t.Fatal("missing database / multi statements was accepted")
	}
}

func TestConfigureDBPoolRejectsInvalidBudget(t *testing.T) {
	if err := configureDBPool(nil, func(string) string { return "" }); err == nil {
		t.Fatal("nil database was accepted")
	}
	db, err := sql.Open("mysql", "auction:secret@tcp(127.0.0.1:3306)/live_auction")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	getenv := func(key string) string {
		switch key {
		case "AUCTION_DOMAIN_RELAY_DB_MAX_OPEN_CONNS":
			return "2"
		case "AUCTION_DOMAIN_RELAY_DB_MAX_IDLE_CONNS":
			return "3"
		default:
			return ""
		}
	}
	if err := configureDBPool(db, getenv); err == nil {
		t.Fatal("idle connections greater than open were accepted")
	}
}

func TestDomainRelayOperationsHandlerReflectsReadiness(t *testing.T) {
	runner := &domainRelayRunnerStub{}
	runner.ready.Store(true)
	var running atomic.Bool
	running.Store(true)
	handler := operationsHandler(&running, runner)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("ready response=%d headers=%v", response.Code, response.Header())
	}
	runner.ready.Store(false)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready response=%d", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["service"] != "domain-relay" {
		t.Fatalf("body=%v error=%v", body, err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/readyz", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST response=%d", response.Code)
	}
}

func TestDomainRelayRunServiceShutsDownWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &domainRelayRunnerStub{started: make(chan struct{})}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() { done <- runService(ctx, runner, "127.0.0.1:0", logger) }()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runService: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runService did not stop")
	}
}

func TestDomainRelayRunServiceFailsWhenRunnerStopsUnexpectedly(t *testing.T) {
	runner := &domainRelayRunnerStub{run: func(context.Context) error { return nil }}
	err := runService(context.Background(), runner, "127.0.0.1:0", slog.Default())
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("error=%v", err)
	}
}

type domainRelayRunnerStub struct {
	ready   atomic.Bool
	started chan struct{}
	run     func(context.Context) error
}

func (runner *domainRelayRunnerStub) Run(ctx context.Context) error {
	if runner.run != nil {
		return runner.run(ctx)
	}
	if runner.started != nil {
		close(runner.started)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (runner *domainRelayRunnerStub) Ready() bool { return runner.ready.Load() }

var _ domainRelayRunner = (*domainrelay.Relay)(nil)
