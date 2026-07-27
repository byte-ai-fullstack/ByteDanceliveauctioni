package redisclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFromEnvBuildsSentinelConfigAndKeepsSecretsOpaque(t *testing.T) {
	values := map[string]string{
		"AUCTION_REDIS_ADDRS":             "sentinel-1:26379, sentinel-2:26379",
		"AUCTION_REDIS_MASTER_NAME":       "auction-master",
		"AUCTION_REDIS_PASSWORD":          "redis-secret",
		"AUCTION_REDIS_SENTINEL_PASSWORD": "sentinel-secret",
		"AUCTION_REDIS_POOL_SIZE":         "12",
		"AUCTION_REDIS_MIN_IDLE_CONNS":    "3",
	}
	cfg, err := FromEnv(func(key string) string { return values[key] }, "127.0.0.1:6379", "relay-1")
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if got, want := cfg.Addrs, []string{"sentinel-1:26379", "sentinel-2:26379"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addrs=%v want=%v", got, want)
	}
	if cfg.MasterName != "auction-master" || cfg.ClientName != "relay-1" || cfg.PoolSize != 12 || cfg.MinIdleConns != 3 {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestFromEnvUsesStandaloneFallback(t *testing.T) {
	cfg, err := FromEnv(func(string) string { return "" }, "127.0.0.1:6379", "relay")
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if got, want := cfg.Addrs, []string{"127.0.0.1:6379"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addrs=%v want=%v", got, want)
	}
}

func TestConfigRejectsClusterModeAndUnsafeBounds(t *testing.T) {
	base := Config{
		Addrs:        []string{"redis:6379"},
		ClientName:   "relay",
		PoolSize:     10,
		MinIdleConns: 1,
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
	tests := []Config{
		{},
		{Addrs: []string{"redis-1:6379", "redis-2:6379"}, ClientName: base.ClientName, PoolSize: 10, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, MasterName: "bad\nmaster", ClientName: base.ClientName, PoolSize: 10, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, SentinelPassword: "secret", ClientName: base.ClientName, PoolSize: 10, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: []string{"missing-port"}, ClientName: base.ClientName, PoolSize: 10, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, ClientName: "", PoolSize: 10, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, ClientName: base.ClientName, PoolSize: 0, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, ClientName: base.ClientName, PoolSize: 1, MinIdleConns: 2, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, ClientName: base.ClientName, PoolSize: 10, DialTimeout: 0, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Addrs: base.Addrs, ClientName: base.ClientName, PoolSize: 10, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, TLSCAFile: "/tmp/ca.pem"},
	}
	for _, cfg := range tests {
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config=%+v error=%v want ErrInvalidConfig", cfg, err)
		}
		if _, err := New(context.Background(), cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New config=%+v error=%v want ErrInvalidConfig", cfg, err)
		}
	}
}

func TestFromEnvRejectsMalformedScalarSettings(t *testing.T) {
	tests := map[string]string{
		"AUCTION_REDIS_DB":             "-1",
		"AUCTION_REDIS_POOL_SIZE":      "zero",
		"AUCTION_REDIS_MIN_IDLE_CONNS": "-1",
		"AUCTION_REDIS_TLS_ENABLED":    "sometimes",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			_, err := FromEnv(func(candidate string) string {
				if candidate == key {
					return value
				}
				return ""
			}, "redis:6379", "relay")
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error=%v want ErrInvalidConfig", err)
			}
		})
	}
	if _, err := FromEnv(nil, "redis:6379", "relay"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil getenv error=%v", err)
	}
}

func TestNewRejectsMissingTLSCAWithoutDialing(t *testing.T) {
	cfg := Config{
		Addrs:        []string{"redis:6379"},
		ClientName:   "relay",
		PoolSize:     10,
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		TLS:          true,
		TLSCAFile:    "/does/not/exist",
	}
	if _, err := New(context.Background(), cfg); err == nil {
		t.Fatal("missing TLS CA returned no error")
	}
}

func TestTLSConfigAndAddressDeduplication(t *testing.T) {
	tlsConfig, err := newTLSConfig("", "redis.internal")
	if err != nil {
		t.Fatalf("newTLSConfig: %v", err)
	}
	if tlsConfig.ServerName != "redis.internal" || tlsConfig.MinVersion == 0 {
		t.Fatalf("TLS config=%+v", tlsConfig)
	}

	invalidCA := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalidCA, []byte("not a PEM certificate"), 0o600); err != nil {
		t.Fatalf("write invalid CA: %v", err)
	}
	if _, err := newTLSConfig(invalidCA, ""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid CA error=%v want ErrInvalidConfig", err)
	}

	got := deduplicate([]string{"redis-1:6379", " redis-2:6379 ", "redis-1:6379"})
	want := []string{"redis-1:6379", "redis-2:6379"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deduplicated=%v want=%v", got, want)
	}
}
