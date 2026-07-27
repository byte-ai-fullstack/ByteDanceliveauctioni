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
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

type projectorRunner interface {
	Run(ctx context.Context) error
	Ready() bool
	PausedPartitions() map[projector.TopicPartition]string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("projector stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("projector environment reader and logger are required")
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_PROJECTOR_STARTUP_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	rawDSN := strings.TrimSpace(getenv("AUCTION_MYSQL_DSN"))
	if rawDSN == "" {
		rawDSN = "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"
	}
	configuredDSN, err := projectorMySQLDSN(rawDSN)
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", configuredDSN)
	if err != nil {
		return fmt.Errorf("open projector MySQL: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Error("close projector MySQL", slog.Any("error", closeErr))
		}
	}()
	if err := configureDBPool(db, getenv); err != nil {
		return err
	}
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping projector MySQL: %w", err)
	}
	schemaVerifier, err := mysqlschema.NewVerifier()
	if err != nil {
		return err
	}
	if err := schemaVerifier.VerifyCurrent(startupCtx, db); err != nil {
		return fmt.Errorf("verify projector MySQL schema: %w", err)
	}
	observability.BindDBStatsProvider(db.Stats)
	defer observability.BindDBStatsProvider(nil)

	retryAttempts, err := intSetting(getenv, "AUCTION_PROJECTOR_RETRY_ATTEMPTS", 5)
	if err != nil {
		return err
	}
	retryBase, err := durationSetting(getenv, "AUCTION_PROJECTOR_RETRY_BASE_DELAY", 20*time.Millisecond)
	if err != nil {
		return err
	}
	retryMax, err := durationSetting(getenv, "AUCTION_PROJECTOR_RETRY_MAX_DELAY", time.Second)
	if err != nil {
		return err
	}
	store, err := projector.NewSQLStore(db, projector.WithRetryPolicy(retryAttempts, retryBase, retryMax))
	if err != nil {
		return err
	}

	instanceID := defaultInstanceID()
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "auction-projector-"+instanceID)
	if err != nil {
		return err
	}
	consumerConfig, err := kafkaConsumerConfig(getenv)
	if err != nil {
		return err
	}
	kafkaConsumer, err := projector.NewKafkaConsumer(startupCtx, kafkaConfig, store, consumerConfig)
	if err != nil {
		return err
	}
	defer kafkaConsumer.Close()
	runner, err := projector.NewConsumer(kafkaConsumer, store, projector.ConsumerConfig{MaxPollRecords: consumerConfig.MaxPollRecords})
	if err != nil {
		return err
	}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_PROJECTOR_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18083"
	}
	logger.Info("projector starting",
		slog.String("operations_addr", operationsAddr),
		slog.String("consumer_group", consumerConfig.GroupID),
		slog.Int("max_poll_records", consumerConfig.MaxPollRecords),
	)
	return runService(ctx, runner, operationsAddr, logger)
}

func kafkaConsumerConfig(getenv func(string) string) (projector.KafkaConsumerConfig, error) {
	groupID := strings.TrimSpace(getenv("AUCTION_PROJECTOR_GROUP_ID"))
	if groupID == "" {
		groupID = "auction-projector-v1"
	}
	sessionTimeout, err := durationSetting(getenv, "AUCTION_PROJECTOR_SESSION_TIMEOUT", 30*time.Second)
	if err != nil {
		return projector.KafkaConsumerConfig{}, err
	}
	heartbeat, err := durationSetting(getenv, "AUCTION_PROJECTOR_HEARTBEAT_INTERVAL", 3*time.Second)
	if err != nil {
		return projector.KafkaConsumerConfig{}, err
	}
	maxPoll, err := intSetting(getenv, "AUCTION_PROJECTOR_MAX_POLL_RECORDS", 500)
	if err != nil {
		return projector.KafkaConsumerConfig{}, err
	}
	return projector.KafkaConsumerConfig{
		GroupID: groupID, SessionTimeout: sessionTimeout, HeartbeatInterval: heartbeat, MaxPollRecords: maxPoll,
	}, nil
}

func configureDBPool(db *sql.DB, getenv func(string) string) error {
	if db == nil {
		return errors.New("projector database is required")
	}
	maxOpen, err := intSetting(getenv, "AUCTION_PROJECTOR_DB_MAX_OPEN_CONNS", 12)
	if err != nil {
		return err
	}
	maxIdle, err := intSetting(getenv, "AUCTION_PROJECTOR_DB_MAX_IDLE_CONNS", 6)
	if err != nil {
		return err
	}
	if maxIdle > maxOpen {
		return errors.New("AUCTION_PROJECTOR_DB_MAX_IDLE_CONNS cannot exceed max open connections")
	}
	maxLifetime, err := durationSetting(getenv, "AUCTION_PROJECTOR_DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return err
	}
	maxIdleTime, err := durationSetting(getenv, "AUCTION_PROJECTOR_DB_CONN_MAX_IDLE_TIME", 2*time.Minute)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLifetime)
	db.SetConnMaxIdleTime(maxIdleTime)
	return nil
}

func projectorMySQLDSN(raw string) (string, error) {
	config, err := mysql.ParseDSN(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse projector MySQL DSN: %w", err)
	}
	if strings.TrimSpace(config.DBName) == "" {
		return "", errors.New("projector MySQL DSN must name a database")
	}
	if config.MultiStatements {
		return "", errors.New("projector MySQL DSN cannot enable multiStatements")
	}
	config.ParseTime = true
	config.RejectReadOnly = true
	return config.FormatDSN(), nil
}

func runService(ctx context.Context, runner projectorRunner, operationsAddr string, logger *slog.Logger) error {
	if runner == nil || logger == nil {
		return errors.New("projector runner and logger are required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on Projector operations address: %w", err)
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
			return errors.New("projector stopped unexpectedly")
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
			return fmt.Errorf("shutdown Projector operations server: %w", err)
		}
		return nil
	})
	err = group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		logger.Info("projector shutdown complete")
		return nil
	}
	return err
}

func operationsHandler(running *atomic.Bool, runner projectorRunner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live", 0)
	})
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live", 0)
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		paused := 0
		isReady := running != nil && running.Load() && runner != nil && runner.Ready()
		if runner != nil {
			paused = len(runner.PausedPartitions())
		}
		status, state := http.StatusOK, "ready"
		if !isReady {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		writeStatus(writer, request, status, isReady, state, paused)
	})
	mux.Handle("/metrics", promhttp.Handler())
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(writer, request)
	})
}

func writeStatus(writer http.ResponseWriter, request *http.Request, status int, ok bool, state string, pausedPartitions int) {
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
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"ok": ok, "service": "projector", "state": state, "paused_partitions": pausedPartitions,
	})
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
