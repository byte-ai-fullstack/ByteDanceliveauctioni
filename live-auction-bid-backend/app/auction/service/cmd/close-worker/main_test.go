package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/worker/closeworker"
)

func TestWorkerConfigFromEnvDefaultsOverridesAndValidation(t *testing.T) {
	config, err := workerConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.Interval != 250*time.Millisecond || config.BatchLimit != 200 || config.Concurrency != 8 || config.OperationTimeout != 2*time.Second {
		t.Fatalf("defaults=%+v", config)
	}
	values := map[string]string{
		"AUCTION_CLOSE_WORKER_INTERVAL":          "100ms",
		"AUCTION_CLOSE_WORKER_BATCH_LIMIT":       "50",
		"AUCTION_CLOSE_WORKER_CONCURRENCY":       "4",
		"AUCTION_CLOSE_WORKER_OPERATION_TIMEOUT": "750ms",
	}
	config, err = workerConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.Interval != 100*time.Millisecond || config.BatchLimit != 50 || config.Concurrency != 4 || config.OperationTimeout != 750*time.Millisecond {
		t.Fatalf("overrides=%+v", config)
	}
	if _, err := workerConfigFromEnv(nil); err == nil {
		t.Fatal("nil getenv was accepted")
	}
	for key, value := range map[string]string{
		"AUCTION_CLOSE_WORKER_INTERVAL":    "never",
		"AUCTION_CLOSE_WORKER_BATCH_LIMIT": "0",
		"AUCTION_CLOSE_WORKER_CONCURRENCY": "many",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := workerConfigFromEnv(func(candidate string) string {
				if candidate == key {
					return value
				}
				return ""
			})
			if err == nil {
				t.Fatal("malformed setting was accepted")
			}
		})
	}
}

func TestOperationsHandlerExposesLiveAndWorkerReadiness(t *testing.T) {
	runner := &runnerStub{stats: closeworker.Stats{LastError: "redis unavailable"}}
	var running atomic.Bool
	running.Store(true)
	handler := operationsHandler(&running, runner)
	assert := func(path, method string, want int, body string) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != want || (body != "" && !strings.Contains(response.Body.String(), body)) {
			t.Fatalf("%s %s status=%d body=%q", method, path, response.Code, response.Body.String())
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing security headers", path)
		}
	}
	assert("/healthz", http.MethodGet, http.StatusOK, `"state":"live"`)
	assert("/readyz", http.MethodGet, http.StatusServiceUnavailable, "redis unavailable")
	runner.ready.Store(true)
	assert("/readyz", http.MethodGet, http.StatusOK, `"state":"ready"`)
	assert("/readyz", http.MethodHead, http.StatusOK, "")
	assert("/livez", http.MethodPost, http.StatusMethodNotAllowed, "method not allowed")
}

func TestRunServicePropagatesFatalErrorAndStopsOnContext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fatal := errors.New("fatal worker error")
	if err := runService(context.Background(), &runnerStub{run: func(context.Context) error { return fatal }}, "127.0.0.1:0", logger); !errors.Is(err, fatal) {
		t.Fatalf("fatal error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runService(ctx, &runnerStub{run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return nil
		}}, "127.0.0.1:0", logger)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runService did not stop")
	}
	if err := runService(context.Background(), nil, "127.0.0.1:0", logger); err == nil {
		t.Fatal("nil runner was accepted")
	}
	if err := runService(context.Background(), &runnerStub{}, "127.0.0.1:0", nil); err == nil {
		t.Fatal("nil logger was accepted")
	}
	if err := runService(context.Background(), &runnerStub{run: func(context.Context) error { return nil }}, "127.0.0.1:0", logger); err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("unexpected stop error=%v", err)
	}
}

func TestScalarSettingsAndDefaultInstanceID(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "DURATION":
			return "125ms"
		case "INTEGER":
			return "9"
		default:
			return ""
		}
	}
	if got, err := durationSetting(getenv, "DURATION", time.Second); err != nil || got != 125*time.Millisecond {
		t.Fatalf("duration=%s error=%v", got, err)
	}
	if got, err := intSetting(getenv, "INTEGER", 1); err != nil || got != 9 {
		t.Fatalf("integer=%d error=%v", got, err)
	}
	if got, err := int64Setting(getenv, "MISSING", 7); err != nil || got != 7 {
		t.Fatalf("fallback=%d error=%v", got, err)
	}
	if got := defaultInstanceID(); got == "" || strings.ContainsAny(got, ":\r\n") {
		t.Fatalf("instance ID=%q", got)
	}
}

type runnerStub struct {
	ready atomic.Bool
	run   func(context.Context) error
	stats closeworker.Stats
}

func (runner *runnerStub) Run(ctx context.Context) error {
	if runner.run != nil {
		return runner.run(ctx)
	}
	<-ctx.Done()
	return nil
}

func (runner *runnerStub) Ready() bool              { return runner.ready.Load() }
func (runner *runnerStub) Stats() closeworker.Stats { return runner.stats }
