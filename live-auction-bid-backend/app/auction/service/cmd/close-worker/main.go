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
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/redisclient"
	"live-auction-bid/backend/app/auction/service/internal/runtimegeneration"
	"live-auction-bid/backend/app/auction/service/internal/worker/closeworker"
)

type closeWorkerRunner interface {
	Run(ctx context.Context) error
	Ready() bool
	Stats() closeworker.Stats
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("close worker stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("close worker environment reader and logger are required")
	}
	instanceID := strings.TrimSpace(getenv("AUCTION_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_CLOSE_WORKER_STARTUP_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	redisConfig, err := redisclient.FromEnv(getenv, "127.0.0.1:16379", "close-worker-"+instanceID)
	if err != nil {
		return err
	}
	primary, err := redisclient.NewPrimaryClient(startupCtx, redisConfig)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := primary.Close(); closeErr != nil {
			logger.Error("close Redis client", slog.Any("error", closeErr))
		}
	}()
	bindRedisMetrics(primary)
	defer observability.BindRedisPoolStatsProvider(nil)
	store, err := data.NewRuntimeCommandStore(primary, data.RuntimeOutboxShardCount)
	if err != nil {
		return err
	}
	generationBackend, err := runtimegeneration.NewRedisBackend(primary)
	if err != nil {
		return err
	}
	generationPollInterval, err := durationSetting(getenv, "AUCTION_RUNTIME_GENERATION_POLL_INTERVAL", time.Second)
	if err != nil {
		return err
	}
	generationGuard, err := runtimegeneration.NewGuard(generationBackend, runtimegeneration.Config{
		PollInterval: generationPollInterval,
	})
	if err != nil {
		return err
	}
	if err := store.BindRuntimeGenerationGuard(generationGuard); err != nil {
		return err
	}
	observability.BindRuntimeGenerationReadyProvider(func() bool { return generationGuard.Status().Ready })
	defer observability.BindRuntimeGenerationReadyProvider(nil)
	if err := generationGuard.Initialize(startupCtx); err != nil {
		logger.Warn("close worker generation starts frozen", slog.Any("error", err))
	}
	go generationGuard.Run(ctx)
	go redisclient.RunSentinelSwitchMasterWatcher(ctx, redisConfig, func(event string) {
		logger.Warn("Redis Sentinel primary switch observed", slog.String("event", event))
		generationGuard.SignalSwitchMaster()
	})
	config, err := workerConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	worker, err := closeworker.New(store, config, logger)
	if err != nil {
		return err
	}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_CLOSE_WORKER_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18085"
	}
	logger.Info("close worker starting",
		slog.String("instance_id", instanceID), slog.String("operations_addr", operationsAddr),
		slog.Duration("interval", config.Interval), slog.Int64("batch_limit", config.BatchLimit), slog.Int("concurrency", config.Concurrency),
	)
	return runService(ctx, worker, operationsAddr, logger)
}

func workerConfigFromEnv(getenv func(string) string) (closeworker.Config, error) {
	if getenv == nil {
		return closeworker.Config{}, errors.New("getenv function is required")
	}
	interval, err := durationSetting(getenv, "AUCTION_CLOSE_WORKER_INTERVAL", 250*time.Millisecond)
	if err != nil {
		return closeworker.Config{}, err
	}
	batchLimit, err := int64Setting(getenv, "AUCTION_CLOSE_WORKER_BATCH_LIMIT", 200)
	if err != nil {
		return closeworker.Config{}, err
	}
	concurrency, err := intSetting(getenv, "AUCTION_CLOSE_WORKER_CONCURRENCY", 8)
	if err != nil {
		return closeworker.Config{}, err
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_CLOSE_WORKER_OPERATION_TIMEOUT", 2*time.Second)
	if err != nil {
		return closeworker.Config{}, err
	}
	return closeworker.Config{Interval: interval, BatchLimit: batchLimit, Concurrency: concurrency, OperationTimeout: operationTimeout}, nil
}

func runService(ctx context.Context, runner closeWorkerRunner, operationsAddr string, logger *slog.Logger) error {
	if runner == nil || logger == nil {
		return errors.New("close worker runner and logger are required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on close worker operations address: %w", err)
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
			return errors.New("close worker stopped unexpectedly")
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
		logger.Info("close worker shutdown complete")
		return nil
	}
	return err
}

func operationsHandler(running *atomic.Bool, runner closeWorkerRunner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live", closeworker.Stats{})
	})
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live", closeworker.Stats{})
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		ready := running != nil && running.Load() && runner != nil && runner.Ready()
		status, state := http.StatusOK, "ready"
		if !ready {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		stats := closeworker.Stats{}
		if runner != nil {
			stats = runner.Stats()
		}
		writeStatus(writer, request, status, ready, state, stats)
	})
	mux.Handle("/metrics", promhttp.Handler())
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(writer, request)
	})
}

func writeStatus(writer http.ResponseWriter, request *http.Request, status int, ok bool, state string, stats closeworker.Stats) {
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
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "service": "close-worker", "state": state, "worker": stats})
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
	value, err := int64Setting(getenv, key, int64(fallback))
	return int(value), err
}

func int64Setting(getenv func(string) string, key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func defaultInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "close-worker"
	}
	return strings.NewReplacer(":", "-", "\r", "-", "\n", "-").Replace(strings.TrimSpace(hostname))
}

func bindRedisMetrics(client interface{ PoolStats() *redis.PoolStats }) {
	observability.BindRedisPoolStatsProvider(func() observability.RedisPoolStats {
		stats := client.PoolStats()
		return observability.RedisPoolStats{
			Hits: stats.Hits, Misses: stats.Misses, Timeouts: stats.Timeouts, TotalConns: stats.TotalConns,
			IdleConns: stats.IdleConns, StaleConns: stats.StaleConns,
		}
	})
}
