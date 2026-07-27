package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/data"
)

func TestOperationsHandlerReportsLiveAndAtomicReadiness(t *testing.T) {
	var ready atomic.Bool
	handler := operationsHandler(&ready)

	assertStatus := func(path string, method string, want int, wantBody string) {
		t.Helper()
		request := httptest.NewRequest(method, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, want, response.Body.String())
		}
		if wantBody != "" && !strings.Contains(response.Body.String(), wantBody) {
			t.Fatalf("%s %s body=%q want substring=%q", method, path, response.Body.String(), wantBody)
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing security headers", path)
		}
	}

	assertStatus("/healthz", http.MethodGet, http.StatusOK, `"state":"live"`)
	assertStatus("/readyz", http.MethodGet, http.StatusServiceUnavailable, `"state":"not_ready"`)
	ready.Store(true)
	assertStatus("/readyz", http.MethodGet, http.StatusOK, `"state":"ready"`)
	assertStatus("/readyz", http.MethodHead, http.StatusOK, "")
	assertStatus("/livez", http.MethodPost, http.StatusMethodNotAllowed, "method not allowed")
}

func TestRelayConfigFromEnvUsesDefaultsAndRejectsMalformedSettings(t *testing.T) {
	cfg, err := relayConfigFromEnv(func(string) string { return "" }, "relay-1")
	if err != nil {
		t.Fatalf("relayConfigFromEnv: %v", err)
	}
	if cfg.InstanceID != "relay-1" || cfg.ShardCount != data.RuntimeOutboxShardCount || cfg.LeaseTTL != 15*time.Second || cfg.RenewInterval != 5*time.Second {
		t.Fatalf("config=%+v", cfg)
	}

	values := map[string]string{
		"AUCTION_OUTBOX_SHARDS":                  "16",
		"AUCTION_OUTBOX_RELAY_LEASE_TTL":         "12s",
		"AUCTION_OUTBOX_RELAY_RENEW_INTERVAL":    "3s",
		"AUCTION_OUTBOX_RELAY_OPERATION_TIMEOUT": "4s",
		"AUCTION_OUTBOX_RELAY_RELEASE_TIMEOUT":   "1s",
	}
	cfg, err = relayConfigFromEnv(func(key string) string { return values[key] }, "relay-2")
	if err != nil {
		t.Fatalf("relayConfigFromEnv overrides: %v", err)
	}
	if cfg.ShardCount != data.RuntimeOutboxShardCount || cfg.LeaseTTL != 12*time.Second || cfg.RenewInterval != 3*time.Second || cfg.OperationTimeout != 4*time.Second || cfg.ReleaseTimeout != time.Second {
		t.Fatalf("override config=%+v", cfg)
	}
	values["AUCTION_OUTBOX_SHARDS"] = "4"
	if _, err := relayConfigFromEnv(func(key string) string { return values[key] }, "relay-2"); err == nil {
		t.Fatal("partial outbox shard coverage returned no error")
	}

	if _, err := relayConfigFromEnv(nil, "relay"); err == nil {
		t.Fatal("nil getenv returned no error")
	}
	for key, value := range map[string]string{
		"AUCTION_OUTBOX_SHARDS":          "zero",
		"AUCTION_OUTBOX_RELAY_LEASE_TTL": "never",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := relayConfigFromEnv(func(candidate string) string {
				if candidate == key {
					return value
				}
				return ""
			}, "relay")
			if err == nil {
				t.Fatal("malformed setting returned no error")
			}
		})
	}
}

func TestRunServicePropagatesFatalRelayErrorAndGracefullyCancels(t *testing.T) {
	fatalErr := errors.New("poison runtime fact")
	if err := runService(context.Background(), runnerStub{run: func(context.Context) error { return fatalErr }}, "127.0.0.1:0"); !errors.Is(err, fatalErr) {
		t.Fatalf("fatal error=%v want=%v", err, fatalErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runService(ctx, runnerStub{run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return nil
		}}, "127.0.0.1:0")
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
			t.Fatalf("graceful runService: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runService did not stop after cancellation")
	}
	if err := runService(context.Background(), nil, "127.0.0.1:0"); err == nil {
		t.Fatal("nil runner returned no error")
	}
	if err := runService(context.Background(), runnerStub{run: func(context.Context) error { return nil }}, "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("unexpected stop error=%v", err)
	}
}

func TestScalarSettingsAndInstanceID(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "DURATION":
			return "250ms"
		case "INTEGER":
			return "7"
		default:
			return ""
		}
	}
	if value, err := durationSetting(getenv, "DURATION", time.Second); err != nil || value != 250*time.Millisecond {
		t.Fatalf("duration=%s error=%v", value, err)
	}
	if value, err := intSetting(getenv, "INTEGER", 1); err != nil || value != 7 {
		t.Fatalf("integer=%d error=%v", value, err)
	}
	if value, err := durationSetting(getenv, "MISSING", time.Second); err != nil || value != time.Second {
		t.Fatalf("fallback duration=%s error=%v", value, err)
	}
	if value, err := intSetting(getenv, "MISSING", 3); err != nil || value != 3 {
		t.Fatalf("fallback integer=%d error=%v", value, err)
	}
	if instanceID := defaultInstanceID(); instanceID == "" || strings.ContainsAny(instanceID, ":\r\n") {
		t.Fatalf("invalid default instance ID %q", instanceID)
	}
}

type runnerStub struct {
	run func(context.Context) error
}

func (r runnerStub) Run(ctx context.Context) error { return r.run(ctx) }
