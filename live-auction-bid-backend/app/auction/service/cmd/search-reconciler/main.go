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
	"live-auction-bid/backend/app/auction/service/internal/searchrebuild"
	"live-auction-bid/backend/app/auction/service/internal/searchreconcile"
)

type runnerConfig struct {
	Interval         time.Duration
	OperationTimeout time.Duration
	PageSize         int
	Concurrency      int
}

type reconcileRunner struct {
	db         *sql.DB
	reconciler *searchreconcile.Reconciler
	config     runnerConfig
	logger     *slog.Logger
	ready      atomic.Bool
	cursor     string
	now        func() time.Time
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("search reconciler stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("search reconciler environment reader and logger are required")
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_SEARCH_RECONCILE_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()
	db, err := openMySQL(startupCtx, getenv)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Warn("close search reconciler MySQL", slog.Any("error", closeErr))
		}
	}()
	observability.BindDBStatsProvider(db.Stats)
	defer observability.BindDBStatsProvider(nil)

	esIndex, err := newElasticsearchIndex(startupCtx, getenv)
	if err != nil {
		return err
	}
	esReader, err := searchreconcile.NewElasticsearchReader(esIndex)
	if err != nil {
		return err
	}
	embedder := searchindex.NewEmbeddingClientFromEnv(getenv)
	vectorIndex, err := newPGVectorIndex(startupCtx, getenv, embedder)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := vectorIndex.Close(); closeErr != nil {
			logger.Warn("close search reconciler pgvector index", slog.Any("error", closeErr))
		}
	}()
	vectorReader, err := searchreconcile.NewPGVectorReader(vectorIndex)
	if err != nil {
		return err
	}
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "search-reconciler")
	if err != nil {
		return err
	}
	publisher, err := searchreconcile.NewKafkaRepairPublisher(startupCtx, kafkaConfig)
	if err != nil {
		return err
	}
	defer publisher.Close()
	findings, err := searchreconcile.NewSQLFindingStore(db)
	if err != nil {
		return err
	}
	reconciler, err := searchreconcile.New(esReader, vectorReader, publisher, findings)
	if err != nil {
		return err
	}
	config, err := runnerConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	runner := &reconcileRunner{db: db, reconciler: reconciler, config: config, logger: logger, now: time.Now}
	operationsAddr := strings.TrimSpace(getenv("AUCTION_SEARCH_RECONCILE_OPERATIONS_ADDR"))
	if operationsAddr == "" {
		operationsAddr = ":18090"
	}
	logger.Info("search reconciler starting",
		slog.String("operations_addr", operationsAddr), slog.Duration("interval", config.Interval),
		slog.Int("page_size", config.PageSize), slog.Int("concurrency", config.Concurrency),
	)
	return runService(ctx, runner, operationsAddr, logger)
}

func (runner *reconcileRunner) Run(ctx context.Context) error {
	if runner == nil || runner.db == nil || runner.reconciler == nil || runner.logger == nil || runner.now == nil {
		return errors.New("search reconciliation runner is not initialized")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			runner.ready.Store(false)
			return nil
		case <-timer.C:
			if err := runner.runPage(ctx); err != nil {
				runner.ready.Store(false)
				runner.logger.Error("search reconciliation page failed", slog.String("cursor", runner.cursor), slog.Any("error", err))
			} else {
				runner.ready.Store(true)
			}
			timer.Reset(runner.config.Interval)
		}
	}
}

func (runner *reconcileRunner) Ready() bool { return runner != nil && runner.ready.Load() }

func (runner *reconcileRunner) runPage(ctx context.Context) error {
	snapshot, err := searchrebuild.BeginSnapshotAfter(ctx, runner.db, runner.cursor)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			runner.logger.Warn("close search reconciliation snapshot", slog.Any("error", closeErr))
		}
	}()
	page, err := snapshot.Next(ctx, runner.config.PageSize)
	if err != nil {
		return err
	}
	if err := snapshot.Commit(); err != nil {
		return err
	}
	var repairs atomic.Int64
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(runner.config.Concurrency)
	for _, record := range page.Records {
		record := record
		group.Go(func() error {
			operationCtx, cancel := context.WithTimeout(groupCtx, runner.config.OperationTimeout)
			defer cancel()
			result, reconcileErr := runner.reconciler.Reconcile(operationCtx, record)
			if reconcileErr != nil {
				return reconcileErr
			}
			if result.RepairPublished {
				repairs.Add(1)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	if page.Done {
		runner.cursor = ""
	} else {
		runner.cursor = page.LastLotID
	}
	now := runner.now()
	observability.MarkSearchReconcileSuccess(now)
	runner.logger.Info("search reconciliation page complete",
		slog.Int("documents", len(page.Records)), slog.Int("skipped_drafts", page.SkippedDraft), slog.Int64("repairs", repairs.Load()),
		slog.String("next_cursor", runner.cursor), slog.Bool("wrapped", page.Done),
	)
	return nil
}

func runnerConfigFromEnv(getenv func(string) string) (runnerConfig, error) {
	interval, err := durationSetting(getenv, "AUCTION_SEARCH_RECONCILE_INTERVAL", time.Minute)
	if err != nil {
		return runnerConfig{}, err
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_SEARCH_RECONCILE_OPERATION_TIMEOUT", 5*time.Second)
	if err != nil {
		return runnerConfig{}, err
	}
	pageSize, err := intSetting(getenv, "AUCTION_SEARCH_RECONCILE_PAGE_SIZE", 100)
	if err != nil || pageSize > 1000 {
		return runnerConfig{}, errors.New("AUCTION_SEARCH_RECONCILE_PAGE_SIZE must be within [1,1000]")
	}
	concurrency, err := intSetting(getenv, "AUCTION_SEARCH_RECONCILE_CONCURRENCY", 8)
	if err != nil || concurrency > 64 {
		return runnerConfig{}, errors.New("AUCTION_SEARCH_RECONCILE_CONCURRENCY must be within [1,64]")
	}
	return runnerConfig{Interval: interval, OperationTimeout: operationTimeout, PageSize: pageSize, Concurrency: concurrency}, nil
}

func openMySQL(ctx context.Context, getenv func(string) string) (*sql.DB, error) {
	raw := strings.TrimSpace(getenv("AUCTION_MYSQL_DSN"))
	if raw == "" {
		raw = "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"
	}
	config, err := mysql.ParseDSN(raw)
	if err != nil {
		return nil, fmt.Errorf("parse search reconciler MySQL DSN: %w", err)
	}
	if strings.TrimSpace(config.DBName) == "" || config.MultiStatements {
		return nil, errors.New("search reconciler MySQL DSN must name a database and disable multiStatements")
	}
	config.ParseTime = true
	config.RejectReadOnly = true
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping search reconciler MySQL: %w", err)
	}
	verifier, err := mysqlschema.NewVerifier()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifier.VerifyCurrent(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify search reconciler MySQL schema: %w", err)
	}
	return db, nil
}

func newElasticsearchIndex(ctx context.Context, getenv func(string) string) (*searchindex.ElasticsearchIndex, error) {
	requestTimeout, err := durationSetting(getenv, "AUCTION_ES_REQUEST_TIMEOUT", 5*time.Second)
	if err != nil {
		return nil, err
	}
	maxResponseBytes, err := intSetting(getenv, "AUCTION_ES_MAX_RESPONSE_BYTES", 1<<20)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(getenv("AUCTION_ES_URL"))
	if baseURL == "" {
		baseURL = "http://127.0.0.1:19200"
	}
	alias := strings.TrimSpace(getenv("AUCTION_ES_WRITE_ALIAS"))
	if alias == "" {
		alias = "auction-lots-current"
	}
	return searchindex.NewElasticsearchIndex(ctx, searchindex.ElasticsearchConfig{
		BaseURL: baseURL, Username: strings.TrimSpace(getenv("AUCTION_ES_USERNAME")), Password: getenv("AUCTION_ES_PASSWORD"),
		WriteAlias: alias, RequestTimeout: requestTimeout, MaxResponseBytes: int64(maxResponseBytes),
	})
}

func newPGVectorIndex(ctx context.Context, getenv func(string) string, embedder *searchindex.EmbeddingClient) (*searchindex.PGVectorIndex, error) {
	dsn := strings.TrimSpace(getenv("AUCTION_SEARCH_PG_DSN"))
	if dsn == "" {
		dsn = "postgres://auction_search:auction_search_dev@127.0.0.1:15432/live_auction_search?sslmode=disable"
	}
	return searchindex.NewPGVectorIndex(ctx, searchindex.PGVectorConfig{
		DSN: dsn, EmbeddingProvider: embedder.Provider(), EmbeddingModel: embedder.Model(),
		EmbeddingModelVersion: embedder.ModelVersion(), EmbeddingDimensions: embedder.Dimensions(),
		MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: 30 * time.Minute, ConnMaxIdleTime: 2 * time.Minute,
	})
}

type serviceRunner interface {
	Run(ctx context.Context) error
	Ready() bool
}

func runService(ctx context.Context, runner serviceRunner, operationsAddr string, logger *slog.Logger) error {
	if runner == nil || logger == nil {
		return errors.New("search reconciler runner and logger are required")
	}
	listener, err := net.Listen("tcp", operationsAddr)
	if err != nil {
		return fmt.Errorf("listen on search reconciler operations address: %w", err)
	}
	defer func() { _ = listener.Close() }()
	server := &http.Server{
		Handler: operationsHandler(runner), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	serviceCtx, cancelService := context.WithCancel(ctx)
	defer cancelService()
	group, groupCtx := errgroup.WithContext(serviceCtx)
	group.Go(func() error {
		defer cancelService()
		return runner.Run(groupCtx)
	})
	group.Go(func() error {
		defer cancelService()
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		<-groupCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})
	err = group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		logger.Info("search reconciler shutdown complete")
		return nil
	}
	return err
}

func operationsHandler(runner serviceRunner) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, true, "live")
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		ready := runner != nil && runner.Ready()
		status, state := http.StatusOK, "ready"
		if !ready {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		writeStatus(writer, request, status, ready, state)
	})
	return mux
}

func writeStatus(writer http.ResponseWriter, request *http.Request, status int, ok bool, state string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok, "state": state, "path": request.URL.Path})
}

func durationSetting(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return parsed, nil
}

func intSetting(getenv func(string) string, key string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}
