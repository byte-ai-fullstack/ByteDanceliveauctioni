package realtime

import (
	"testing"
	"time"
)

func TestDrainConfigFromEnv(t *testing.T) {
	values := map[string]string{
		"AUCTION_WS_DRAIN_ADMISSION_DELAY": "2s",
		"AUCTION_WS_DRAIN_BATCH_SIZE":      "250",
		"AUCTION_WS_DRAIN_BATCH_INTERVAL":  "125ms",
		"AUCTION_WS_DRAIN_RETRY_AFTER_MIN": "500ms",
		"AUCTION_WS_DRAIN_RETRY_AFTER_MAX": "20s",
	}
	cfg, err := DrainConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("parse drain config: %v", err)
	}
	if cfg.AdmissionDelay != 2*time.Second || cfg.BatchSize != 250 || cfg.BatchInterval != 125*time.Millisecond || cfg.RetryAfterMin != 500*time.Millisecond || cfg.RetryAfterMax != 20*time.Second {
		t.Fatalf("unexpected drain config: %+v", cfg)
	}
}

func TestDrainConfigRejectsUnsafeValues(t *testing.T) {
	tests := []DrainConfig{
		{AdmissionDelay: -time.Second, BatchSize: 1},
		{BatchSize: 0},
		{BatchSize: 1, BatchInterval: -time.Millisecond},
		{BatchSize: 1, RetryAfterMin: 2 * time.Second, RetryAfterMax: time.Second},
	}
	for _, cfg := range tests {
		if _, err := NormalizeDrainConfig(cfg); err == nil {
			t.Fatalf("expected invalid drain config to fail: %+v", cfg)
		}
	}
}

func TestSnapshotRefreshConfigFromEnv(t *testing.T) {
	values := map[string]string{
		"AUCTION_WS_SNAPSHOT_REFRESH_INTERVAL":    "4s",
		"AUCTION_WS_SNAPSHOT_REFRESH_TIMEOUT":     "750ms",
		"AUCTION_WS_SNAPSHOT_REFRESH_CONCURRENCY": "12",
	}
	cfg, err := SnapshotRefreshConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("parse snapshot refresh config: %v", err)
	}
	if cfg.Interval != 4*time.Second || cfg.RequestTimeout != 750*time.Millisecond || cfg.Concurrency != 12 {
		t.Fatalf("unexpected snapshot refresh config: %+v", cfg)
	}
}

func TestSnapshotRefreshConfigRejectsNonPositiveValues(t *testing.T) {
	for _, cfg := range []SnapshotRefreshConfig{
		{RequestTimeout: time.Second, Concurrency: 1},
		{Interval: time.Second, Concurrency: 1},
		{Interval: time.Second, RequestTimeout: time.Second},
	} {
		if _, err := NormalizeSnapshotRefreshConfig(cfg); err == nil {
			t.Fatalf("expected invalid snapshot refresh config to fail: %+v", cfg)
		}
	}
}

func TestRealtimeConfigFromEnvNormalizesProductionSettings(t *testing.T) {
	values := map[string]string{
		"AUCTION_ENV":                     " Production ",
		"AUCTION_WS_ALLOWED_ORIGINS":      " HTTPS://Example.COM/path, http://localhost:3000, ",
		"AUCTION_JWT_SECRET":              " ticket-secret ",
		"AUCTION_WS_TICKET_TTL":           "45s",
		"AUCTION_WS_ALLOW_MISSING_ORIGIN": "YES",
	}
	cfg, err := ConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Environment != "production" || cfg.TicketTTL != 45*time.Second || !cfg.AllowMissingOrigin || cfg.TicketSecret != "ticket-secret" {
		t.Fatalf("unexpected realtime config: %+v", cfg)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://example.com" || cfg.AllowedOrigins[1] != "http://localhost:3000" {
		t.Fatalf("allowed origins=%v", cfg.AllowedOrigins)
	}
}

func TestRealtimeConfigFromEnvRejectsInvalidOrIncompleteProductionSettings(t *testing.T) {
	tests := []map[string]string{
		{"AUCTION_WS_TICKET_TTL": "not-a-duration"},
		{"AUCTION_ENV": "prod", "AUCTION_JWT_SECRET": "secret"},
		{"AUCTION_ENV": "prod", "AUCTION_WS_ALLOWED_ORIGINS": "https://example.com"},
	}
	for _, values := range tests {
		if _, err := ConfigFromEnv(func(key string) string { return values[key] }); err == nil {
			t.Fatalf("ConfigFromEnv accepted invalid values: %v", values)
		}
	}
}

func TestRealtimeConfigHelpersHandleCSVBooleansAndOrigins(t *testing.T) {
	if values := splitCSV(" first, ,second "); len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("splitCSV=%v", values)
	}
	for _, value := range []string{"1", "TRUE", " yes ", "On"} {
		if !parseBool(value) {
			t.Fatalf("parseBool(%q)=false", value)
		}
	}
	for _, value := range []string{"", "0", "false", "off", "unexpected"} {
		if parseBool(value) {
			t.Fatalf("parseBool(%q)=true", value)
		}
	}
	for _, origin := range []string{"http://localhost:8080", "http://127.0.0.1", "http://[::1]:8080"} {
		if !isLocalhostOrigin(origin) {
			t.Fatalf("localhost origin rejected: %q", origin)
		}
	}
	for _, origin := range []string{"::://", "https://example.com"} {
		if isLocalhostOrigin(origin) {
			t.Fatalf("non-local origin accepted: %q", origin)
		}
	}
}
