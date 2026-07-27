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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

func TestProjectorConfigurationDefaultsAndValidation(t *testing.T) {
	getenv := func(string) string { return "" }
	config, err := kafkaConsumerConfig(getenv)
	if err != nil {
		t.Fatalf("kafkaConsumerConfig: %v", err)
	}
	if config.GroupID != "auction-projector-v1" || config.MaxPollRecords != 500 || config.SessionTimeout != 30*time.Second {
		t.Fatalf("config=%+v", config)
	}
	if _, err := kafkaConsumerConfig(func(key string) string {
		if key == "AUCTION_PROJECTOR_MAX_POLL_RECORDS" {
			return "0"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid max poll records was accepted")
	}
	if _, err := durationSetting(func(string) string { return "bad" }, "DURATION", time.Second); err == nil {
		t.Fatal("invalid duration was accepted")
	}
}

func TestProjectorMySQLDSNEnforcesSingleDatabaseAndSafeStatements(t *testing.T) {
	dsn, err := projectorMySQLDSN("auction:secret@tcp(127.0.0.1:3306)/live_auction?parseTime=false")
	if err != nil {
		t.Fatalf("projectorMySQLDSN: %v", err)
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !config.ParseTime || !config.RejectReadOnly || config.MultiStatements || config.DBName != "live_auction" {
		t.Fatalf("configured DSN=%+v", config)
	}
	if _, err := projectorMySQLDSN("auction:secret@tcp(127.0.0.1:3306)/?multiStatements=true"); err == nil {
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
		case "AUCTION_PROJECTOR_DB_MAX_OPEN_CONNS":
			return "2"
		case "AUCTION_PROJECTOR_DB_MAX_IDLE_CONNS":
			return "3"
		default:
			return ""
		}
	}
	if err := configureDBPool(db, getenv); err == nil {
		t.Fatal("idle connections greater than open were accepted")
	}
}

func TestProjectorOperationsHandlerReflectsPausedPartitions(t *testing.T) {
	runner := &runnerStub{ready: true}
	var running atomic.Bool
	running.Store(true)
	handler := operationsHandler(&running, runner)

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("ready response code=%d headers=%v", response.Code, response.Header())
	}

	runner.mu.Lock()
	runner.ready = false
	runner.paused = map[projector.TopicPartition]string{{Topic: "topic", Partition: 1}: "version_gap"}
	runner.mu.Unlock()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready response code=%d", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["paused_partitions"] != float64(1) {
		t.Fatalf("body=%v", body)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/readyz", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST response code=%d", response.Code)
	}
}

func TestRunServiceShutsDownWithRootContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &runnerStub{ready: true, started: make(chan struct{})}
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

func TestRunServiceFailsWhenProjectorStopsUnexpectedly(t *testing.T) {
	runner := &runnerStub{run: func(context.Context) error { return nil }}
	err := runService(context.Background(), runner, "127.0.0.1:0", slog.Default())
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("error=%v", err)
	}
}

type runnerStub struct {
	mu      sync.Mutex
	ready   bool
	paused  map[projector.TopicPartition]string
	started chan struct{}
	run     func(context.Context) error
}

func (runner *runnerStub) Run(ctx context.Context) error {
	if runner.run != nil {
		return runner.run(ctx)
	}
	if runner.started != nil {
		close(runner.started)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (runner *runnerStub) Ready() bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.ready
}

func (runner *runnerStub) PausedPartitions() map[projector.TopicPartition]string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	result := make(map[projector.TopicPartition]string, len(runner.paused))
	for key, value := range runner.paused {
		result[key] = value
	}
	return result
}
