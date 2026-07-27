package realtime

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ScopePublic = "public"
	ScopeAdmin  = "admin"

	defaultTicketTTL = 60 * time.Second

	defaultDrainAdmissionDelay = 10 * time.Second
	defaultDrainBatchSize      = 500
	defaultDrainBatchInterval  = 150 * time.Millisecond
	defaultDrainRetryAfterMin  = time.Second
	defaultDrainRetryAfterMax  = 30 * time.Second

	defaultSnapshotRefreshInterval    = 5 * time.Second
	defaultSnapshotRefreshTimeout     = 2 * time.Second
	defaultSnapshotRefreshConcurrency = 8
)

type Config struct {
	Environment        string
	AllowedOrigins     []string
	AllowMissingOrigin bool
	TicketTTL          time.Duration
	TicketSecret       string
}

// DrainConfig controls the bounded, staggered migration of WebSocket clients
// away from a Gateway instance that is leaving service.
type DrainConfig struct {
	AdmissionDelay time.Duration
	BatchSize      int
	BatchInterval  time.Duration
	RetryAfterMin  time.Duration
	RetryAfterMax  time.Duration
}

type SnapshotRefreshConfig struct {
	Interval       time.Duration
	RequestTimeout time.Duration
	Concurrency    int
}

func DefaultSnapshotRefreshConfig() SnapshotRefreshConfig {
	return SnapshotRefreshConfig{
		Interval:       defaultSnapshotRefreshInterval,
		RequestTimeout: defaultSnapshotRefreshTimeout,
		Concurrency:    defaultSnapshotRefreshConcurrency,
	}
}

func SnapshotRefreshConfigFromEnv(getenv func(string) string) (SnapshotRefreshConfig, error) {
	cfg := DefaultSnapshotRefreshConfig()
	var err error
	if cfg.Interval, err = drainDuration(getenv, "AUCTION_WS_SNAPSHOT_REFRESH_INTERVAL", cfg.Interval); err != nil {
		return SnapshotRefreshConfig{}, err
	}
	if cfg.RequestTimeout, err = drainDuration(getenv, "AUCTION_WS_SNAPSHOT_REFRESH_TIMEOUT", cfg.RequestTimeout); err != nil {
		return SnapshotRefreshConfig{}, err
	}
	if value := strings.TrimSpace(getenv("AUCTION_WS_SNAPSHOT_REFRESH_CONCURRENCY")); value != "" {
		cfg.Concurrency, err = strconv.Atoi(value)
		if err != nil {
			return SnapshotRefreshConfig{}, errors.New("AUCTION_WS_SNAPSHOT_REFRESH_CONCURRENCY must be an integer")
		}
	}
	return NormalizeSnapshotRefreshConfig(cfg)
}

func NormalizeSnapshotRefreshConfig(cfg SnapshotRefreshConfig) (SnapshotRefreshConfig, error) {
	if cfg.Interval <= 0 {
		return SnapshotRefreshConfig{}, errors.New("websocket snapshot refresh interval must be positive")
	}
	if cfg.RequestTimeout <= 0 {
		return SnapshotRefreshConfig{}, errors.New("websocket snapshot refresh timeout must be positive")
	}
	if cfg.Concurrency <= 0 {
		return SnapshotRefreshConfig{}, errors.New("websocket snapshot refresh concurrency must be positive")
	}
	return cfg, nil
}

func DefaultDrainConfig() DrainConfig {
	return DrainConfig{
		AdmissionDelay: defaultDrainAdmissionDelay,
		BatchSize:      defaultDrainBatchSize,
		BatchInterval:  defaultDrainBatchInterval,
		RetryAfterMin:  defaultDrainRetryAfterMin,
		RetryAfterMax:  defaultDrainRetryAfterMax,
	}
}

func DrainConfigFromEnv(getenv func(string) string) (DrainConfig, error) {
	cfg := DefaultDrainConfig()
	var err error
	if cfg.AdmissionDelay, err = drainDuration(getenv, "AUCTION_WS_DRAIN_ADMISSION_DELAY", cfg.AdmissionDelay); err != nil {
		return DrainConfig{}, err
	}
	if cfg.BatchInterval, err = drainDuration(getenv, "AUCTION_WS_DRAIN_BATCH_INTERVAL", cfg.BatchInterval); err != nil {
		return DrainConfig{}, err
	}
	if cfg.RetryAfterMin, err = drainDuration(getenv, "AUCTION_WS_DRAIN_RETRY_AFTER_MIN", cfg.RetryAfterMin); err != nil {
		return DrainConfig{}, err
	}
	if cfg.RetryAfterMax, err = drainDuration(getenv, "AUCTION_WS_DRAIN_RETRY_AFTER_MAX", cfg.RetryAfterMax); err != nil {
		return DrainConfig{}, err
	}
	if value := strings.TrimSpace(getenv("AUCTION_WS_DRAIN_BATCH_SIZE")); value != "" {
		cfg.BatchSize, err = strconv.Atoi(value)
		if err != nil {
			return DrainConfig{}, errors.New("AUCTION_WS_DRAIN_BATCH_SIZE must be an integer")
		}
	}
	return NormalizeDrainConfig(cfg)
}

func NormalizeDrainConfig(cfg DrainConfig) (DrainConfig, error) {
	if cfg.AdmissionDelay < 0 {
		return DrainConfig{}, errors.New("websocket drain admission delay cannot be negative")
	}
	if cfg.BatchSize <= 0 {
		return DrainConfig{}, errors.New("websocket drain batch size must be positive")
	}
	if cfg.BatchInterval < 0 {
		return DrainConfig{}, errors.New("websocket drain batch interval cannot be negative")
	}
	if cfg.RetryAfterMin < 0 || cfg.RetryAfterMax < cfg.RetryAfterMin {
		return DrainConfig{}, errors.New("websocket drain retry-after range is invalid")
	}
	return cfg, nil
}

func drainDuration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, errors.New(key + " must be a duration")
	}
	return duration, nil
}

func DefaultConfig() Config {
	return Config{
		Environment:        "dev",
		AllowMissingOrigin: true,
		TicketTTL:          defaultTicketTTL,
	}
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()
	cfg.Environment = strings.TrimSpace(getenv("AUCTION_ENV"))
	cfg.AllowedOrigins = splitCSV(getenv("AUCTION_WS_ALLOWED_ORIGINS"))
	cfg.TicketSecret = strings.TrimSpace(getenv("AUCTION_JWT_SECRET"))
	if value := strings.TrimSpace(getenv("AUCTION_WS_TICKET_TTL")); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, err
		}
		cfg.TicketTTL = duration
	}
	if value := strings.TrimSpace(getenv("AUCTION_WS_ALLOW_MISSING_ORIGIN")); value != "" {
		cfg.AllowMissingOrigin = parseBool(value)
	} else {
		cfg.AllowMissingOrigin = !isProdEnv(cfg.Environment)
	}
	return NormalizeConfig(cfg)
}

func NormalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.Environment) == "" {
		cfg.Environment = "dev"
	}
	cfg.Environment = strings.ToLower(strings.TrimSpace(cfg.Environment))
	if cfg.TicketTTL <= 0 {
		cfg.TicketTTL = defaultTicketTTL
	}
	origins := make([]string, 0, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		normalized, ok := normalizeOrigin(origin)
		if ok {
			origins = append(origins, normalized)
		}
	}
	cfg.AllowedOrigins = origins
	if isProdEnv(cfg.Environment) && len(cfg.AllowedOrigins) == 0 {
		return Config{}, errors.New("AUCTION_WS_ALLOWED_ORIGINS is required in prod")
	}
	if isProdEnv(cfg.Environment) && strings.TrimSpace(cfg.TicketSecret) == "" {
		return Config{}, errors.New("AUCTION_JWT_SECRET is required for websocket tickets in prod")
	}
	return cfg, nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isProdEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

func normalizeOrigin(origin string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}

func isLocalhostOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		host = parsed.Host
		if splitHost, _, err := net.SplitHostPort(parsed.Host); err == nil {
			host = splitHost
		}
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func normalizeScope(scope string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", ScopePublic:
		return ScopePublic, true
	case ScopeAdmin:
		return ScopeAdmin, true
	default:
		return "", false
	}
}
