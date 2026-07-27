package main

import (
	"context"
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

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/worker/vectorindex"
)

type vectorRunner interface {
	Run(ctx context.Context) error
	Ready() bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("pgvector index consumer stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("pgvector consumer environment reader and logger are required")
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_VECTOR_STARTUP_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()
	embedder := searchindex.NewEmbeddingClientFromEnv(getenv)
	if !embedder.Configured() {
		return errors.New("pgvector consumer embedding client is not configured")
	}
	dsn := strings.TrimSpace(getenv("AUCTION_SEARCH_PG_DSN"))
	if dsn == "" {
		dsn = "postgres://auction_search:auction_search_dev@127.0.0.1:15432/live_auction_search?sslmode=disable"
	}
	pgConfig, err := pgConfigFromEnv(getenv, dsn, embedder)
	if err != nil {
		return err
	}
	index, err := searchindex.NewPGVectorIndex(startupCtx, pgConfig)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := index.Close(); closeErr != nil {
			logger.Error("close pgvector index", slog.Any("error", closeErr))
		}
	}()
	processor, err := vectorindex.NewProcessor(index, embedder)
	if err != nil {
		return err
	}
	instanceID := strings.TrimSpace(getenv("AUCTION_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "index-pgvector-"+instanceID)
	if err != nil {
		return err
	}
	kafkaClientConfig, err := kafkaConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	kafkaClient, err := vectorindex.NewKafkaClient(startupCtx, kafkaConfig, kafkaClientConfig)
	if err != nil {
		return err
	}
	defer kafkaClient.Close()
	consumerConfig, err := consumerConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	consumer, err := vectorindex.NewConsumer(kafkaClient, processor, consumerConfig)
	if err != nil {
		return err
	}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_VECTOR_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18089"
	}
	logger.Info("pgvector index consumer starting",
		slog.String("operations_addr", operationsAddr), slog.String("consumer_group", kafkaClientConfig.GroupID),
		slog.String("embedding_provider", embedder.Provider()), slog.String("embedding_model", embedder.Model()),
		slog.String("embedding_model_version", embedder.ModelVersion()), slog.Int("embedding_dimensions", embedder.Dimensions()),
	)
	return runService(ctx, consumer, operationsAddr, logger)
}

func pgConfigFromEnv(getenv func(string) string, dsn string, embedder *searchindex.EmbeddingClient) (searchindex.PGVectorConfig, error) {
	if getenv == nil || embedder == nil {
		return searchindex.PGVectorConfig{}, errors.New("pgvector config environment reader and embedder are required")
	}
	maxOpen, err := intSetting(getenv, "AUCTION_VECTOR_DB_MAX_OPEN_CONNS", 8)
	if err != nil {
		return searchindex.PGVectorConfig{}, err
	}
	maxIdle, err := intSetting(getenv, "AUCTION_VECTOR_DB_MAX_IDLE_CONNS", 4)
	if err != nil {
		return searchindex.PGVectorConfig{}, err
	}
	if maxIdle > maxOpen {
		return searchindex.PGVectorConfig{}, errors.New("AUCTION_VECTOR_DB_MAX_IDLE_CONNS cannot exceed max open connections")
	}
	maxLifetime, err := durationSetting(getenv, "AUCTION_VECTOR_DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return searchindex.PGVectorConfig{}, err
	}
	maxIdleTime, err := durationSetting(getenv, "AUCTION_VECTOR_DB_CONN_MAX_IDLE_TIME", 2*time.Minute)
	if err != nil {
		return searchindex.PGVectorConfig{}, err
	}
	return searchindex.PGVectorConfig{
		DSN: dsn, EmbeddingProvider: embedder.Provider(), EmbeddingModel: embedder.Model(),
		EmbeddingModelVersion: embedder.ModelVersion(), EmbeddingDimensions: embedder.Dimensions(),
		MaxOpenConns: maxOpen, MaxIdleConns: maxIdle, ConnMaxLifetime: maxLifetime, ConnMaxIdleTime: maxIdleTime,
	}, nil
}

func kafkaConfigFromEnv(getenv func(string) string) (vectorindex.KafkaClientConfig, error) {
	groupID := strings.TrimSpace(getenv("AUCTION_VECTOR_GROUP_ID"))
	if groupID == "" {
		groupID = "search-pgvector-v1"
	}
	sessionTimeout, err := durationSetting(getenv, "AUCTION_VECTOR_SESSION_TIMEOUT", 30*time.Second)
	if err != nil {
		return vectorindex.KafkaClientConfig{}, err
	}
	heartbeat, err := durationSetting(getenv, "AUCTION_VECTOR_HEARTBEAT_INTERVAL", 3*time.Second)
	if err != nil {
		return vectorindex.KafkaClientConfig{}, err
	}
	if heartbeat >= sessionTimeout {
		return vectorindex.KafkaClientConfig{}, errors.New("AUCTION_VECTOR_HEARTBEAT_INTERVAL must be less than session timeout")
	}
	return vectorindex.KafkaClientConfig{GroupID: groupID, SessionTimeout: sessionTimeout, HeartbeatInterval: heartbeat}, nil
}

func consumerConfigFromEnv(getenv func(string) string) (vectorindex.ConsumerConfig, error) {
	maxPoll, err := intSetting(getenv, "AUCTION_VECTOR_MAX_POLL_RECORDS", 100)
	if err != nil {
		return vectorindex.ConsumerConfig{}, err
	}
	retryAttempts, err := intSetting(getenv, "AUCTION_VECTOR_RETRY_ATTEMPTS", 5)
	if err != nil {
		return vectorindex.ConsumerConfig{}, err
	}
	retryBase, err := durationSetting(getenv, "AUCTION_VECTOR_RETRY_BASE", 200*time.Millisecond)
	if err != nil {
		return vectorindex.ConsumerConfig{}, err
	}
	retryMax, err := durationSetting(getenv, "AUCTION_VECTOR_RETRY_MAX", 10*time.Second)
	if err != nil {
		return vectorindex.ConsumerConfig{}, err
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_VECTOR_OPERATION_TIMEOUT", 10*time.Second)
	if err != nil {
		return vectorindex.ConsumerConfig{}, err
	}
	if retryMax < retryBase {
		return vectorindex.ConsumerConfig{}, errors.New("AUCTION_VECTOR_RETRY_MAX cannot be less than retry base")
	}
	return vectorindex.ConsumerConfig{
		MaxPollRecords: maxPoll, RetryAttempts: retryAttempts, RetryBase: retryBase, RetryMax: retryMax, OperationTimeout: operationTimeout,
	}, nil
}

func runService(ctx context.Context, runner vectorRunner, operationsAddr string, logger *slog.Logger) error {
	if runner == nil || logger == nil {
		return errors.New("pgvector runner and logger are required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on pgvector operations address: %w", err)
	}
	defer func() { _ = listener.Close() }()
	var running atomic.Bool
	server := &http.Server{
		Handler: operationsHandler(&running, runner), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	group, groupCtx := errgroup.WithContext(ctx)
	running.Store(true)
	group.Go(func() error {
		defer running.Store(false)
		err := runner.Run(groupCtx)
		if err == nil && groupCtx.Err() == nil {
			return errors.New("pgvector consumer stopped unexpectedly")
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
		return server.Shutdown(shutdownCtx)
	})
	err = group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		logger.Info("pgvector consumer shutdown complete")
		return nil
	}
	return err
}

func operationsHandler(running *atomic.Bool, runner vectorRunner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		ready := running != nil && running.Load() && runner != nil && runner.Ready()
		status, state := http.StatusOK, "ready"
		if !ready {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		writeStatus(writer, request, status, ready, state)
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
	if request.Method != http.MethodHead {
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "service": "index-pgvector", "state": state})
	}
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
