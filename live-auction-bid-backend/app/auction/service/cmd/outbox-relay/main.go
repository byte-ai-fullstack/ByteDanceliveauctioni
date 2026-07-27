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
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/redisclient"
	"live-auction-bid/backend/app/auction/service/internal/worker/outboxrelay"
)

type relayRunner interface {
	Run(ctx context.Context) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		slog.Error("outbox relay stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	instanceID := strings.TrimSpace(getenv("AUCTION_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_OUTBOX_RELAY_STARTUP_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	redisConfig, err := redisclient.FromEnv(getenv, "127.0.0.1:16379", "outbox-relay-"+instanceID)
	if err != nil {
		return err
	}
	redisClient, err := redisclient.New(startupCtx, redisConfig)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			slog.Error("close Redis client", slog.Any("error", closeErr))
		}
	}()
	bindRedisMetrics(redisClient)
	defer observability.BindRedisPoolStatsProvider(nil)

	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "auction-outbox-relay-"+instanceID)
	if err != nil {
		return err
	}
	producer, err := outboxrelay.NewKafkaRuntimeProducer(startupCtx, kafkaConfig)
	if err != nil {
		return err
	}
	defer producer.Close()

	relayConfig, err := relayConfigFromEnv(getenv, instanceID)
	if err != nil {
		return err
	}
	relay, err := outboxrelay.New(data.NewRuntimeOutboxQueueFromRedis(redisClient), producer, relayConfig)
	if err != nil {
		return err
	}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_OUTBOX_RELAY_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18082"
	}
	return runService(ctx, relay, operationsAddr)
}

func relayConfigFromEnv(getenv func(string) string, instanceID string) (outboxrelay.Config, error) {
	if getenv == nil {
		return outboxrelay.Config{}, errors.New("getenv function is required")
	}
	shardCount, err := intSetting(getenv, "AUCTION_OUTBOX_SHARDS", data.RuntimeOutboxShardCount)
	if err != nil {
		return outboxrelay.Config{}, err
	}
	if shardCount != data.RuntimeOutboxShardCount {
		return outboxrelay.Config{}, fmt.Errorf("AUCTION_OUTBOX_SHARDS must remain fixed at %d", data.RuntimeOutboxShardCount)
	}
	leaseTTL, err := durationSetting(getenv, "AUCTION_OUTBOX_RELAY_LEASE_TTL", 15*time.Second)
	if err != nil {
		return outboxrelay.Config{}, err
	}
	renewInterval, err := durationSetting(getenv, "AUCTION_OUTBOX_RELAY_RENEW_INTERVAL", 5*time.Second)
	if err != nil {
		return outboxrelay.Config{}, err
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_OUTBOX_RELAY_OPERATION_TIMEOUT", 10*time.Second)
	if err != nil {
		return outboxrelay.Config{}, err
	}
	releaseTimeout, err := durationSetting(getenv, "AUCTION_OUTBOX_RELAY_RELEASE_TIMEOUT", 2*time.Second)
	if err != nil {
		return outboxrelay.Config{}, err
	}
	return outboxrelay.Config{
		InstanceID:       instanceID,
		ShardCount:       shardCount,
		LeaseTTL:         leaseTTL,
		RenewInterval:    renewInterval,
		OperationTimeout: operationTimeout,
		ReleaseTimeout:   releaseTimeout,
	}, nil
}

func runService(ctx context.Context, runner relayRunner, operationsAddr string) error {
	if runner == nil {
		return errors.New("relay runner is required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on Relay operations address: %w", err)
	}
	defer func() { _ = listener.Close() }()

	var ready atomic.Bool
	server := &http.Server{
		Handler:           operationsHandler(&ready),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	group, groupCtx := errgroup.WithContext(ctx)
	ready.Store(true)
	group.Go(func() error {
		defer ready.Store(false)
		err := runner.Run(groupCtx)
		if err == nil && groupCtx.Err() == nil {
			return errors.New("outbox relay stopped unexpectedly")
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
		ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown Relay operations server: %w", err)
		}
		return nil
	})
	err = group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

func operationsHandler(ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		isReady := ready != nil && ready.Load()
		status := http.StatusOK
		state := "ready"
		if !isReady {
			status = http.StatusServiceUnavailable
			state = "not_ready"
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
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "service": "outbox-relay", "state": state})
}

func bindRedisMetrics(client interface{ PoolStats() *redis.PoolStats }) {
	observability.BindRedisPoolStatsProvider(func() observability.RedisPoolStats {
		stats := client.PoolStats()
		return observability.RedisPoolStats{
			Hits:       stats.Hits,
			Misses:     stats.Misses,
			Timeouts:   stats.Timeouts,
			TotalConns: stats.TotalConns,
			IdleConns:  stats.IdleConns,
			StaleConns: stats.StaleConns,
		}
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
