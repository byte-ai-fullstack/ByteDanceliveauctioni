package redisclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrInvalidConfig = errors.New("invalid Redis client config")

type Config struct {
	Addrs            []string
	MasterName       string
	ClientName       string
	Username         string
	Password         string
	SentinelUsername string
	SentinelPassword string
	DB               int
	PoolSize         int
	MinIdleConns     int
	DialTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	TLS              bool
	TLSCAFile        string
	TLSServerName    string
}

// FromEnv creates a standalone or Sentinel configuration without ever selecting Redis Cluster mode implicitly.
func FromEnv(getenv func(string) string, defaultAddr, defaultClientName string) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("%w: getenv function is required", ErrInvalidConfig)
	}
	addressText := strings.TrimSpace(getenv("AUCTION_REDIS_ADDRS"))
	if addressText == "" {
		addressText = strings.TrimSpace(getenv("AUCTION_REDIS_ADDR"))
	}
	if addressText == "" {
		addressText = strings.TrimSpace(defaultAddr)
	}
	cfg := Config{
		Addrs:            splitCSV(addressText),
		MasterName:       strings.TrimSpace(getenv("AUCTION_REDIS_MASTER_NAME")),
		ClientName:       strings.TrimSpace(getenv("AUCTION_REDIS_CLIENT_NAME")),
		Username:         getenv("AUCTION_REDIS_USERNAME"),
		Password:         getenv("AUCTION_REDIS_PASSWORD"),
		SentinelUsername: getenv("AUCTION_REDIS_SENTINEL_USERNAME"),
		SentinelPassword: getenv("AUCTION_REDIS_SENTINEL_PASSWORD"),
		PoolSize:         20,
		MinIdleConns:     2,
		DialTimeout:      5 * time.Second,
		ReadTimeout:      3 * time.Second,
		WriteTimeout:     3 * time.Second,
		TLSCAFile:        strings.TrimSpace(getenv("AUCTION_REDIS_TLS_CA_FILE")),
		TLSServerName:    strings.TrimSpace(getenv("AUCTION_REDIS_TLS_SERVER_NAME")),
	}
	if cfg.ClientName == "" {
		cfg.ClientName = strings.TrimSpace(defaultClientName)
	}
	var err error
	if cfg.DB, err = parseInt(getenv("AUCTION_REDIS_DB"), 0, 0); err != nil {
		return Config{}, err
	}
	if cfg.PoolSize, err = parseInt(getenv("AUCTION_REDIS_POOL_SIZE"), cfg.PoolSize, 1); err != nil {
		return Config{}, err
	}
	if cfg.MinIdleConns, err = parseInt(getenv("AUCTION_REDIS_MIN_IDLE_CONNS"), cfg.MinIdleConns, 0); err != nil {
		return Config{}, err
	}
	if cfg.TLS, err = parseBool(getenv("AUCTION_REDIS_TLS_ENABLED"), false); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects accidental Cluster mode and invalid Sentinel or TLS combinations.
func (c Config) Validate() error {
	if len(c.Addrs) == 0 {
		return fmt.Errorf("%w: at least one address is required", ErrInvalidConfig)
	}
	masterName := strings.TrimSpace(c.MasterName)
	if masterName == "" && len(c.Addrs) != 1 {
		return fmt.Errorf("%w: multiple addresses require a Sentinel master_name; Redis Cluster is not supported", ErrInvalidConfig)
	}
	if strings.ContainsAny(masterName, "\r\n\x00") {
		return fmt.Errorf("%w: Sentinel master_name cannot contain control characters", ErrInvalidConfig)
	}
	if masterName == "" && (c.SentinelUsername != "" || c.SentinelPassword != "") {
		return fmt.Errorf("%w: Sentinel credentials require master_name", ErrInvalidConfig)
	}
	for _, raw := range c.Addrs {
		address := strings.TrimSpace(raw)
		if address == "" || strings.ContainsAny(address, "\r\n\x00") {
			return fmt.Errorf("%w: addresses cannot be empty or contain control characters", ErrInvalidConfig)
		}
		host, portText, err := net.SplitHostPort(address)
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("%w: Redis addresses must use host:port", ErrInvalidConfig)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("%w: Redis port must be within [1,65535]", ErrInvalidConfig)
		}
	}
	if c.ClientName == "" || len(c.ClientName) > 128 || strings.ContainsAny(c.ClientName, "\r\n\x00") {
		return fmt.Errorf("%w: client_name must be 1-128 characters without control characters", ErrInvalidConfig)
	}
	if c.DB < 0 || c.PoolSize <= 0 || c.MinIdleConns < 0 || c.MinIdleConns > c.PoolSize {
		return fmt.Errorf("%w: invalid DB or pool bounds", ErrInvalidConfig)
	}
	if c.DialTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 {
		return fmt.Errorf("%w: Redis timeouts must be positive", ErrInvalidConfig)
	}
	if !c.TLS && (c.TLSCAFile != "" || c.TLSServerName != "") {
		return fmt.Errorf("%w: Redis TLS fields require TLS to be enabled", ErrInvalidConfig)
	}
	return nil
}

// New validates, connects, and pings a standalone or Sentinel-discovered Redis primary.
func New(ctx context.Context, cfg Config) (redis.UniversalClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var tlsConfig *tls.Config
	if cfg.TLS {
		var err error
		tlsConfig, err = newTLSConfig(cfg.TLSCAFile, cfg.TLSServerName)
		if err != nil {
			return nil, err
		}
	}
	client := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:                 deduplicate(cfg.Addrs),
		MasterName:            strings.TrimSpace(cfg.MasterName),
		ClientName:            cfg.ClientName,
		Username:              cfg.Username,
		Password:              cfg.Password,
		SentinelUsername:      cfg.SentinelUsername,
		SentinelPassword:      cfg.SentinelPassword,
		DB:                    cfg.DB,
		PoolSize:              cfg.PoolSize,
		MinIdleConns:          cfg.MinIdleConns,
		DialTimeout:           cfg.DialTimeout,
		ReadTimeout:           cfg.ReadTimeout,
		WriteTimeout:          cfg.WriteTimeout,
		ContextTimeoutEnabled: true,
		TLSConfig:             tlsConfig,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return client, nil
}

// NewPrimaryClient returns the concrete single-primary client used by Lua
// command paths that require Conn() for EVAL followed by WAIT on one connection.
// Config validation guarantees this can only be standalone or Sentinel mode.
func NewPrimaryClient(ctx context.Context, cfg Config) (*redis.Client, error) {
	client, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	primary, ok := client.(*redis.Client)
	if !ok {
		_ = client.Close()
		return nil, fmt.Errorf("%w: Redis client is not a single-primary client", ErrInvalidConfig)
	}
	return primary, nil
}

func newTLSConfig(caFile, serverName string) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if caFile = strings.TrimSpace(caFile); caFile != "" {
		pemBytes, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read Redis TLS CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("%w: Redis TLS CA contains no valid PEM certificate", ErrInvalidConfig)
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: strings.TrimSpace(serverName)}, nil
}

func parseInt(raw string, fallback, minimum int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		return 0, fmt.Errorf("%w: integer setting must be at least %d", ErrInvalidConfig, minimum)
	}
	return value, nil
}

func parseBool(raw string, fallback bool) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%w: boolean setting is invalid", ErrInvalidConfig)
	}
	return value, nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func deduplicate(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
