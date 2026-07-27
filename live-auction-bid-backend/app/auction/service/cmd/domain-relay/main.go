package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/mysqlschema"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/realtime"
	"live-auction-bid/backend/app/auction/service/internal/worker/domainrelay"
)

type domainRelayRunner interface {
	Run(ctx context.Context) error
	Ready() bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("domain relay stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("domain relay environment reader and logger are required")
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_STARTUP_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	rawDSN := strings.TrimSpace(getenv("AUCTION_MYSQL_DSN"))
	if rawDSN == "" {
		rawDSN = "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"
	}
	configuredDSN, err := domainRelayMySQLDSN(rawDSN)
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", configuredDSN)
	if err != nil {
		return fmt.Errorf("open domain relay MySQL: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Error("close domain relay MySQL", slog.Any("error", closeErr))
		}
	}()
	if err := configureDBPool(db, getenv); err != nil {
		return err
	}
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping domain relay MySQL: %w", err)
	}
	schemaVerifier, err := mysqlschema.NewVerifier()
	if err != nil {
		return err
	}
	if err := schemaVerifier.VerifyCurrent(startupCtx, db); err != nil {
		return fmt.Errorf("verify domain relay MySQL schema: %w", err)
	}
	observability.BindDBStatsProvider(db.Stats)
	defer observability.BindDBStatsProvider(nil)

	store, err := domainrelay.NewSQLStore(db)
	if err != nil {
		return err
	}
	instanceID := strings.TrimSpace(getenv("AUCTION_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "auction-domain-relay-"+instanceID)
	if err != nil {
		return err
	}
	producer, err := domainrelay.NewKafkaProducer(startupCtx, kafkaConfig)
	if err != nil {
		return err
	}
	defer producer.Close()
	relayConfig, err := relayConfigFromEnv(getenv, instanceID)
	if err != nil {
		return err
	}
	relayOptions := make([]domainrelay.Option, 0, 1)
	readyBus, err := orderReadyBusFromEnv(getenv, instanceID)
	if err != nil {
		return err
	}
	if readyBus != nil {
		if err := readyBus.StartPublisher(startupCtx); err != nil {
			// Core NATS only accelerates the private READY notification. Kafka
			// publication and MySQL durability must remain available without it.
			logger.Warn("domain relay READY acceleration disabled after NATS startup failure", slog.Any("error", err))
			_ = readyBus.Close()
		} else {
			defer func() {
				if closeErr := readyBus.Close(); closeErr != nil {
					logger.Warn("close domain relay NATS publisher", slog.Any("error", closeErr))
				}
			}()
			relayOptions = append(relayOptions, domainrelay.WithOrderReadyPublisher(readyBus))
		}
	}
	runner, err := domainrelay.New(store, producer, relayConfig, relayOptions...)
	if err != nil {
		return err
	}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_DOMAIN_RELAY_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18084"
	}
	logger.Info("domain relay starting",
		slog.String("operations_addr", operationsAddr),
		slog.Int("claim_limit", relayConfig.ClaimLimit),
		slog.Int("concurrency", relayConfig.Concurrency),
		slog.Int("max_attempts", relayConfig.MaxAttempts),
	)
	return runService(ctx, runner, operationsAddr, logger)
}

func orderReadyBusFromEnv(getenv func(string) string, instanceID string) (*realtime.NATSBus, error) {
	if getenv == nil {
		return nil, errors.New("getenv function is required")
	}
	natsURL := strings.TrimSpace(getenv("AUCTION_DOMAIN_RELAY_NATS_URLS"))
	if natsURL == "" {
		if productionEnvironment(getenv("AUCTION_ENV")) {
			return nil, errors.New("AUCTION_DOMAIN_RELAY_NATS_URLS is required in production")
		}
		return nil, nil
	}
	return realtime.NewNATSBus(realtime.NATSBusConfig{
		URL:    natsURL,
		Name:   "live-auction-domain-relay-" + strings.TrimSpace(instanceID),
		Origin: "domain-relay",
	})
}

func productionEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

func relayConfigFromEnv(getenv func(string) string, instanceID string) (domainrelay.Config, error) {
	if getenv == nil {
		return domainrelay.Config{}, errors.New("getenv function is required")
	}
	claimLimit, err := intSetting(getenv, "AUCTION_DOMAIN_RELAY_CLAIM_LIMIT", 16)
	if err != nil {
		return domainrelay.Config{}, err
	}
	concurrency, err := intSetting(getenv, "AUCTION_DOMAIN_RELAY_CONCURRENCY", 16)
	if err != nil {
		return domainrelay.Config{}, err
	}
	leaseTTL, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_LEASE_TTL", 30*time.Second)
	if err != nil {
		return domainrelay.Config{}, err
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_OPERATION_TIMEOUT", 5*time.Second)
	if err != nil {
		return domainrelay.Config{}, err
	}
	idleInterval, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_IDLE_INTERVAL", 250*time.Millisecond)
	if err != nil {
		return domainrelay.Config{}, err
	}
	retryBase, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_RETRY_BASE", 100*time.Millisecond)
	if err != nil {
		return domainrelay.Config{}, err
	}
	retryMax, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_RETRY_MAX", 10*time.Second)
	if err != nil {
		return domainrelay.Config{}, err
	}
	statsInterval, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_STATS_INTERVAL", 5*time.Second)
	if err != nil {
		return domainrelay.Config{}, err
	}
	maxAttempts, err := intSetting(getenv, "AUCTION_DOMAIN_RELAY_MAX_ATTEMPTS", 8)
	if err != nil {
		return domainrelay.Config{}, err
	}
	return domainrelay.Config{
		InstanceID:       instanceID,
		ClaimLimit:       claimLimit,
		Concurrency:      concurrency,
		LeaseTTL:         leaseTTL,
		OperationTimeout: operationTimeout,
		IdleInterval:     idleInterval,
		RetryBase:        retryBase,
		RetryMax:         retryMax,
		StatsInterval:    statsInterval,
		MaxAttempts:      maxAttempts,
	}, nil
}

func configureDBPool(db *sql.DB, getenv func(string) string) error {
	if db == nil {
		return errors.New("domain relay database is required")
	}
	maxOpen, err := intSetting(getenv, "AUCTION_DOMAIN_RELAY_DB_MAX_OPEN_CONNS", 8)
	if err != nil {
		return err
	}
	maxIdle, err := intSetting(getenv, "AUCTION_DOMAIN_RELAY_DB_MAX_IDLE_CONNS", 4)
	if err != nil {
		return err
	}
	if maxIdle > maxOpen {
		return errors.New("AUCTION_DOMAIN_RELAY_DB_MAX_IDLE_CONNS cannot exceed max open connections")
	}
	maxLifetime, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return err
	}
	maxIdleTime, err := durationSetting(getenv, "AUCTION_DOMAIN_RELAY_DB_CONN_MAX_IDLE_TIME", 2*time.Minute)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLifetime)
	db.SetConnMaxIdleTime(maxIdleTime)
	return nil
}

func domainRelayMySQLDSN(raw string) (string, error) {
	config, err := mysql.ParseDSN(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse domain relay MySQL DSN: %w", err)
	}
	if strings.TrimSpace(config.DBName) == "" {
		return "", errors.New("domain relay MySQL DSN must name a database")
	}
	if config.MultiStatements {
		return "", errors.New("domain relay MySQL DSN cannot enable multiStatements")
	}
	config.ParseTime = true
	config.RejectReadOnly = true
	return config.FormatDSN(), nil
}

func runService(ctx context.Context, runner domainRelayRunner, operationsAddr string, logger *slog.Logger) error {
	if runner == nil || logger == nil {
		return errors.New("domain relay runner and logger are required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on domain relay operations address: %w", err)
	}
	defer func() { _ = listener.Close() }()
	var running atomic.Bool
	server := &http.Server{
		Handler:           operationsHandler(&running, runner),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	group, groupCtx := errgroup.WithContext(ctx)
	running.Store(true)
	group.Go(func() error {
		defer running.Store(false)
		err := runner.Run(groupCtx)
		if err == nil && groupCtx.Err() == nil {
			return errors.New("domain relay stopped unexpectedly")
		}
		return err
	})
	group.Go(func() error {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		<-groupCtx.Done()
		running.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown domain relay operations server: %w", err)
		}
		return nil
	})
	err = group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		logger.Info("domain relay shutdown complete")
		return nil
	}
	return err
}

func operationsHandler(running *atomic.Bool, runner domainRelayRunner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		isReady := running != nil && running.Load() && runner != nil && runner.Ready()
		status, state := http.StatusOK, "ready"
		if !isReady {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		writeStatus(writer, request, status, isReady, state)
	})
	mux.Handle("/metrics", promhttp.Handler())
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(writer, request)
	})
}

func writeStatus(writer http.ResponseWriter, request *http.Request, status int, ok bool, state string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if request.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "service": "domain-relay", "state": state})
}

func durationSetting(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return value, nil
}

func intSetting(getenv func(string) string, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func defaultInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "local"
	}
	hostname = strings.NewReplacer(":", "-", "\r", "-", "\n", "-").Replace(strings.TrimSpace(hostname))
	return hostname + "-" + strconv.Itoa(os.Getpid())
}
