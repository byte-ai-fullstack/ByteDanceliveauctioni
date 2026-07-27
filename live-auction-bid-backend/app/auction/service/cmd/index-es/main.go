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
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/worker/esindex"
)

type indexRunner interface {
	Run(ctx context.Context) error
	Ready() bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("Elasticsearch index consumer stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("elasticsearch consumer environment reader and logger are required")
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_ES_STARTUP_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()
	esConfig, err := elasticsearchConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	index, err := searchindex.NewElasticsearchIndex(startupCtx, esConfig)
	if err != nil {
		return err
	}
	rawDSN := strings.TrimSpace(getenv("AUCTION_MYSQL_DSN"))
	if rawDSN == "" {
		rawDSN = "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"
	}
	configuredDSN, err := indexMySQLDSN(rawDSN)
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", configuredDSN)
	if err != nil {
		return fmt.Errorf("open Elasticsearch finding MySQL: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Error("close Elasticsearch finding MySQL", slog.Any("error", closeErr))
		}
	}()
	if err := configureFindingDBPool(db, getenv); err != nil {
		return err
	}
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping Elasticsearch finding MySQL: %w", err)
	}
	schemaVerifier, err := mysqlschema.NewVerifier()
	if err != nil {
		return err
	}
	if err := schemaVerifier.VerifyCurrent(startupCtx, db); err != nil {
		return fmt.Errorf("verify Elasticsearch finding MySQL schema: %w", err)
	}
	observability.BindDBStatsProvider(db.Stats)
	defer observability.BindDBStatsProvider(nil)
	findings, err := esindex.NewSQLFindingStore(db)
	if err != nil {
		return err
	}
	processor, err := esindex.NewProcessor(index)
	if err != nil {
		return err
	}
	instanceID := strings.TrimSpace(getenv("AUCTION_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "index-es-"+instanceID)
	if err != nil {
		return err
	}
	kafkaClientConfig, err := kafkaConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	kafkaClient, err := esindex.NewKafkaClient(startupCtx, kafkaConfig, kafkaClientConfig)
	if err != nil {
		return err
	}
	defer kafkaClient.Close()
	consumerConfig, err := consumerConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	consumer, err := esindex.NewConsumer(kafkaClient, processor, findings, consumerConfig)
	if err != nil {
		return err
	}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_ES_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18088"
	}
	logger.Info("Elasticsearch index consumer starting",
		slog.String("operations_addr", operationsAddr), slog.String("consumer_group", kafkaClientConfig.GroupID),
		slog.String("elasticsearch_url", esConfig.BaseURL), slog.String("write_alias", esConfig.WriteAlias),
	)
	return runService(ctx, consumer, operationsAddr, logger)
}

func configureFindingDBPool(db *sql.DB, getenv func(string) string) error {
	if db == nil {
		return errors.New("elasticsearch finding database is required")
	}
	maxOpen, err := intSetting(getenv, "AUCTION_ES_DB_MAX_OPEN_CONNS", 4)
	if err != nil {
		return err
	}
	maxIdle, err := intSetting(getenv, "AUCTION_ES_DB_MAX_IDLE_CONNS", 2)
	if err != nil {
		return err
	}
	if maxIdle > maxOpen {
		return errors.New("AUCTION_ES_DB_MAX_IDLE_CONNS cannot exceed max open connections")
	}
	maxLifetime, err := durationSetting(getenv, "AUCTION_ES_DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return err
	}
	maxIdleTime, err := durationSetting(getenv, "AUCTION_ES_DB_CONN_MAX_IDLE_TIME", 2*time.Minute)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLifetime)
	db.SetConnMaxIdleTime(maxIdleTime)
	return nil
}

func indexMySQLDSN(raw string) (string, error) {
	config, err := mysql.ParseDSN(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse Elasticsearch finding MySQL DSN: %w", err)
	}
	if strings.TrimSpace(config.DBName) == "" {
		return "", errors.New("elasticsearch finding MySQL DSN must name a database")
	}
	if config.MultiStatements {
		return "", errors.New("elasticsearch finding MySQL DSN cannot enable multiStatements")
	}
	config.ParseTime = true
	config.RejectReadOnly = true
	return config.FormatDSN(), nil
}

func elasticsearchConfigFromEnv(getenv func(string) string) (searchindex.ElasticsearchConfig, error) {
	baseURL := strings.TrimSpace(getenv("AUCTION_ES_URL"))
	if baseURL == "" {
		baseURL = "http://127.0.0.1:19200"
	}
	writeAlias := strings.TrimSpace(getenv("AUCTION_ES_WRITE_ALIAS"))
	if writeAlias == "" {
		writeAlias = "auction-lots-current"
	}
	requestTimeout, err := durationSetting(getenv, "AUCTION_ES_REQUEST_TIMEOUT", 5*time.Second)
	if err != nil {
		return searchindex.ElasticsearchConfig{}, err
	}
	maxResponseBytes, err := intSetting(getenv, "AUCTION_ES_MAX_RESPONSE_BYTES", 1<<20)
	if err != nil {
		return searchindex.ElasticsearchConfig{}, err
	}
	return searchindex.ElasticsearchConfig{
		BaseURL: baseURL, Username: strings.TrimSpace(getenv("AUCTION_ES_USERNAME")), Password: getenv("AUCTION_ES_PASSWORD"),
		WriteAlias: writeAlias, RequestTimeout: requestTimeout, MaxResponseBytes: int64(maxResponseBytes),
	}, nil
}

func kafkaConfigFromEnv(getenv func(string) string) (esindex.KafkaClientConfig, error) {
	groupID := strings.TrimSpace(getenv("AUCTION_ES_GROUP_ID"))
	if groupID == "" {
		groupID = "search-es-v1"
	}
	sessionTimeout, err := durationSetting(getenv, "AUCTION_ES_SESSION_TIMEOUT", 30*time.Second)
	if err != nil {
		return esindex.KafkaClientConfig{}, err
	}
	heartbeat, err := durationSetting(getenv, "AUCTION_ES_HEARTBEAT_INTERVAL", 3*time.Second)
	if err != nil {
		return esindex.KafkaClientConfig{}, err
	}
	if heartbeat >= sessionTimeout {
		return esindex.KafkaClientConfig{}, errors.New("AUCTION_ES_HEARTBEAT_INTERVAL must be less than session timeout")
	}
	return esindex.KafkaClientConfig{GroupID: groupID, SessionTimeout: sessionTimeout, HeartbeatInterval: heartbeat}, nil
}

func consumerConfigFromEnv(getenv func(string) string) (esindex.ConsumerConfig, error) {
	maxPoll, err := intSetting(getenv, "AUCTION_ES_MAX_POLL_RECORDS", 100)
	if err != nil {
		return esindex.ConsumerConfig{}, err
	}
	retryAttempts, err := intSetting(getenv, "AUCTION_ES_RETRY_ATTEMPTS", 5)
	if err != nil {
		return esindex.ConsumerConfig{}, err
	}
	retryBase, err := durationSetting(getenv, "AUCTION_ES_RETRY_BASE", 200*time.Millisecond)
	if err != nil {
		return esindex.ConsumerConfig{}, err
	}
	retryMax, err := durationSetting(getenv, "AUCTION_ES_RETRY_MAX", 10*time.Second)
	if err != nil {
		return esindex.ConsumerConfig{}, err
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_ES_OPERATION_TIMEOUT", 10*time.Second)
	if err != nil {
		return esindex.ConsumerConfig{}, err
	}
	if retryMax < retryBase {
		return esindex.ConsumerConfig{}, errors.New("AUCTION_ES_RETRY_MAX cannot be less than retry base")
	}
	return esindex.ConsumerConfig{
		MaxPollRecords: maxPoll, RetryAttempts: retryAttempts, RetryBase: retryBase,
		RetryMax: retryMax, OperationTimeout: operationTimeout,
	}, nil
}

func runService(ctx context.Context, runner indexRunner, operationsAddr string, logger *slog.Logger) error {
	if runner == nil || logger == nil {
		return errors.New("elasticsearch runner and logger are required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on Elasticsearch operations address: %w", err)
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
			return errors.New("elasticsearch consumer stopped unexpectedly")
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
		logger.Info("Elasticsearch consumer shutdown complete")
		return nil
	}
	return err
}

func operationsHandler(running *atomic.Bool, runner indexRunner) http.Handler {
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
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "service": "index-es", "state": state})
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
