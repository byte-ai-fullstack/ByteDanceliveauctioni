package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/mysqlschema"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/projectiongate"
	"live-auction-bid/backend/app/auction/service/internal/realtime"
	"live-auction-bid/backend/app/auction/service/internal/redisclient"
	"live-auction-bid/backend/app/auction/service/internal/server"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, log.Default()); err != nil {
		log.Printf("auction-service stopped with error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *log.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("auction-service environment reader and logger are required")
	}
	instanceID := strings.TrimSpace(getenv("AUCTION_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	redisConfig, err := redisclient.FromEnv(getenv, "127.0.0.1:16379", "auction-service-"+instanceID)
	if err != nil {
		return err
	}
	if redisConfig.Password == "" {
		redisConfig.Password = "auction_redis"
	}
	redisPrimary, err := redisclient.NewPrimaryClient(startupCtx, redisConfig)
	if err != nil {
		return err
	}
	storeConfig, err := dataConfigFromEnv(getenv, redisPrimary)
	if err != nil {
		_ = redisPrimary.Close()
		return err
	}
	schemaVerifier, err := mysqlschema.NewVerifier()
	if err != nil {
		_ = redisPrimary.Close()
		return err
	}
	storeConfig.SchemaVerifier = schemaVerifier
	store, err := data.NewStore(startupCtx, storeConfig)
	if err != nil {
		_ = redisPrimary.Close()
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			logger.Printf("close auction store: %v", closeErr)
		}
	}()
	if err := store.StartRuntimeGenerationGuard(ctx); err != nil {
		logger.Printf("runtime generation starts frozen: %v", err)
	}
	go redisclient.RunSentinelSwitchMasterWatcher(ctx, redisConfig, func(event string) {
		logger.Printf("Redis Sentinel primary switch observed: %s", event)
		store.SignalRedisPrimarySwitch()
	})

	projectionSettings, err := projectionGateSettingsFromEnv(getenv)
	if err != nil {
		return err
	}
	var projectionGuard *projectiongate.Guard
	if projectionSettings.Enabled {
		kafkaConfig, err := kafkaclient.FromEnv(
			getenv,
			[]string{"127.0.0.1:19092"},
			"auction-service-projection-gate-"+instanceID,
		)
		if err != nil {
			return err
		}
		kafkaSource, err := projectiongate.NewKafkaSource(kafkaConfig)
		if err != nil {
			return err
		}
		database, err := store.ProjectionGateDB()
		if err != nil {
			kafkaSource.Close()
			return err
		}
		offsetSource, err := projectiongate.NewSQLSource(database)
		if err != nil {
			kafkaSource.Close()
			return err
		}
		projectionGuard, err = projectiongate.NewGuard(
			kafkaSource,
			offsetSource,
			projectionSettings.Config,
			slog.New(slog.NewJSONHandler(logger.Writer(), nil)),
		)
		if err != nil {
			kafkaSource.Close()
			return err
		}
		if err := store.SetRuntimeAdmissionGate(projectionGuard); err != nil {
			kafkaSource.Close()
			return err
		}
		if err := projectionGuard.Refresh(startupCtx); err != nil {
			logger.Printf("end-to-end projection gate starts closed: %v", err)
		}
		projectionCtx, cancelProjection := context.WithCancel(ctx)
		projectionDone := make(chan struct{})
		go func() {
			defer close(projectionDone)
			projectionGuard.Run(projectionCtx)
		}()
		defer func() {
			cancelProjection()
			<-projectionDone
			kafkaSource.Close()
		}()
	}

	accessTTL, err := durationSetting(getenv, "AUCTION_ACCESS_TOKEN_TTL", auth.DefaultAccessTTL)
	if err != nil {
		return err
	}
	refreshTTL, err := durationSetting(getenv, "AUCTION_REFRESH_TOKEN_TTL", auth.DefaultRefreshTTL)
	if err != nil {
		return err
	}
	authManager, err := auth.NewManager(auth.Config{
		Secret:     getenv("AUCTION_JWT_SECRET"),
		Issuer:     stringSetting(getenv, "AUCTION_JWT_ISSUER", "auction-backend"),
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	})
	if err != nil {
		return err
	}
	eventPublisher, closePublisher, err := newCommandPublisher(ctx, getenv, instanceID)
	if err != nil {
		return err
	}
	defer closePublisher()
	paymentProvider, err := auction.NewPaymentProviderFromName(getenv("AUCTION_PAYMENT_PROVIDER"))
	if err != nil {
		return err
	}
	usecase := auction.NewAuctionUsecase(store, store, store, eventPublisher).
		SetPaymentProvider(paymentProvider)
	verboseBidLog, err := parseBoolSetting(getenv, "AUCTION_VERBOSE_BID_LOG", false)
	if err != nil {
		return err
	}
	auctionService := appsvc.NewAuctionService(usecase).SetVerboseBidLog(verboseBidLog)
	commandService := appsvc.NewAuctionCommandService(auctionService)

	grpcAddr := stringSetting(getenv, "AUCTION_GRPC_ADDR", ":19090")
	operationsAddr := stringSetting(getenv, "AUCTION_OPERATIONS_ADDR", ":18086")
	grpcServer := server.NewAuctionGRPCServer(grpcAddr, commandService, authManager.Middleware())
	readiness := server.Readiness{Store: store}
	if projectionGuard != nil {
		readiness.AdmissionGate = projectionGuard
		readiness.ProjectionMetrics = projectionGuard
	}
	operationsServer := server.NewOperationsHTTPServer(operationsAddr, "auction-service", readiness)
	logger.Printf("auction-service starting: grpc=%s operations=%s instance=%s", grpcAddr, operationsAddr, instanceID)
	return runServers(ctx, grpcServer, operationsServer, 10*time.Second)
}

type lifecycleServer interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func runServers(ctx context.Context, grpcServer, operationsServer lifecycleServer, stopTimeout time.Duration) error {
	if grpcServer == nil || operationsServer == nil {
		return errors.New("auction-service gRPC and operations servers are required")
	}
	if stopTimeout <= 0 {
		stopTimeout = 10 * time.Second
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return grpcServer.Start(groupCtx) })
	group.Go(func() error { return operationsServer.Start(groupCtx) })
	group.Go(func() error {
		<-groupCtx.Done()
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
		defer cancel()
		return errors.Join(grpcServer.Stop(stopCtx), operationsServer.Stop(stopCtx))
	})
	err := group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

func newCommandPublisher(ctx context.Context, getenv func(string) string, instanceID string) (*realtime.Publisher, func(), error) {
	urls := strings.TrimSpace(getenv("AUCTION_NATS_URLS"))
	if urls == "" {
		if productionEnvironment(getenv("AUCTION_ENV")) {
			return nil, nil, errors.New("AUCTION_NATS_URLS is required in production")
		}
		return realtime.NewPublisher(), func() {}, nil
	}
	reconnectWait, err := durationSetting(getenv, "AUCTION_NATS_RECONNECT_WAIT", 500*time.Millisecond)
	if err != nil {
		return nil, nil, err
	}
	reconnectJitter, err := durationSetting(getenv, "AUCTION_NATS_RECONNECT_JITTER", 500*time.Millisecond)
	if err != nil {
		return nil, nil, err
	}
	flushTimeout, err := durationSetting(getenv, "AUCTION_NATS_FLUSH_TIMEOUT", 250*time.Millisecond)
	if err != nil {
		return nil, nil, err
	}
	dispatchTimeout, err := durationSetting(getenv, "AUCTION_NATS_DISPATCH_TIMEOUT", 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	bus, err := realtime.NewNATSBus(realtime.NATSBusConfig{
		URL:             urls,
		Name:            stringSetting(getenv, "AUCTION_NATS_CLIENT_NAME", "auction-service-"+instanceID),
		Origin:          instanceID,
		ReconnectWait:   reconnectWait,
		ReconnectJitter: reconnectJitter,
		FlushTimeout:    flushTimeout,
		DispatchTimeout: dispatchTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := bus.StartPublisher(ctx); err != nil {
		_ = bus.Close()
		return nil, nil, err
	}
	return realtime.NewPublisher(bus), func() { _ = bus.Close() }, nil
}

func dataConfigFromEnv(getenv func(string) string, client *redis.Client) (data.Config, error) {
	if client == nil {
		return data.Config{}, errors.New("redis primary client is required")
	}
	guardEnabled, err := parseBoolSetting(getenv, "AUCTION_RUNTIME_GENERATION_GUARD_ENABLED", true)
	if err != nil {
		return data.Config{}, err
	}
	outboxShards, err := intSetting(getenv, "AUCTION_OUTBOX_SHARDS", data.RuntimeOutboxShardCount, 1)
	if err != nil {
		return data.Config{}, err
	}
	pendingLimit, err := intSetting(getenv, "AUCTION_OUTBOX_PENDING_LIMIT", 0, 0)
	if err != nil {
		return data.Config{}, err
	}
	dbMaxOpen, err := intSetting(getenv, "AUCTION_DB_MAX_OPEN_CONNS", 20, 1)
	if err != nil {
		return data.Config{}, err
	}
	dbMaxIdle, err := intSetting(getenv, "AUCTION_DB_MAX_IDLE_CONNS", 10, 0)
	if err != nil {
		return data.Config{}, err
	}
	generationPollInterval, err := durationSetting(getenv, "AUCTION_RUNTIME_GENERATION_POLL_INTERVAL", time.Second)
	if err != nil {
		return data.Config{}, err
	}
	reconcileInterval, err := durationSetting(getenv, "AUCTION_RUNTIME_RECONCILE_INTERVAL", 30*time.Second)
	if err != nil {
		return data.Config{}, err
	}
	dbConnMaxLifetime, err := durationSetting(getenv, "AUCTION_DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return data.Config{}, err
	}
	dbConnMaxIdleTime, err := durationSetting(getenv, "AUCTION_DB_CONN_MAX_IDLE_TIME", 2*time.Minute)
	if err != nil {
		return data.Config{}, err
	}
	return data.Config{
		MySQLDSN:                      stringSetting(getenv, "AUCTION_MYSQL_DSN", "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"),
		RedisClient:                   client,
		RuntimeGenerationGuardEnabled: guardEnabled,
		RuntimeGenerationPollInterval: generationPollInterval,
		RuntimeReconcileInterval:      reconcileInterval,
		OutboxShards:                  outboxShards,
		OutboxPendingLimit:            int64(pendingLimit),
		DBMaxOpenConns:                dbMaxOpen,
		DBMaxIdleConns:                dbMaxIdle,
		DBConnMaxLifetime:             dbConnMaxLifetime,
		DBConnMaxIdleTime:             dbConnMaxIdleTime,
	}, nil
}

type projectionGateSettings struct {
	Enabled bool
	Config  projectiongate.Config
}

func projectionGateSettingsFromEnv(getenv func(string) string) (projectionGateSettings, error) {
	if getenv == nil {
		return projectionGateSettings{}, errors.New("projection gate environment reader is required")
	}
	production := productionEnvironment(getenv("AUCTION_ENV"))
	enabled, err := parseBoolSetting(getenv, "AUCTION_PROJECTION_GATE_ENABLED", production)
	if err != nil {
		return projectionGateSettings{}, err
	}
	if production && !enabled {
		return projectionGateSettings{}, errors.New("AUCTION_PROJECTION_GATE_ENABLED cannot be false in production")
	}
	settings := projectionGateSettings{Enabled: enabled, Config: projectiongate.DefaultConfig()}
	if !enabled {
		return settings, nil
	}
	settings.Config.RefreshInterval, err = durationSetting(
		getenv, "AUCTION_PROJECTION_GATE_REFRESH_INTERVAL", settings.Config.RefreshInterval,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.RefreshTimeout, err = durationSetting(
		getenv, "AUCTION_PROJECTION_GATE_REFRESH_TIMEOUT", settings.Config.RefreshTimeout,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.MaxStaleness, err = durationSetting(
		getenv, "AUCTION_PROJECTION_GATE_MAX_STALENESS", settings.Config.MaxStaleness,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.MaxLagRecords, err = int64Setting(
		getenv, "AUCTION_PROJECTION_GATE_MAX_LAG_RECORDS", settings.Config.MaxLagRecords, 1,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.MaxOldestAge, err = durationSetting(
		getenv, "AUCTION_PROJECTION_GATE_MAX_OLDEST_AGE", settings.Config.MaxOldestAge,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.RuntimeTopicRetention, err = durationSetting(
		getenv, "AUCTION_PROJECTION_GATE_RUNTIME_TOPIC_RETENTION", settings.Config.RuntimeTopicRetention,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.MinRetentionHeadroom, err = durationSetting(
		getenv, "AUCTION_PROJECTION_GATE_MIN_RETENTION_HEADROOM", settings.Config.MinRetentionHeadroom,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	settings.Config.HealthyPollsToOpen, err = intSetting(
		getenv, "AUCTION_PROJECTION_GATE_HEALTHY_POLLS_TO_OPEN", settings.Config.HealthyPollsToOpen, 1,
	)
	if err != nil {
		return projectionGateSettings{}, err
	}
	if err := settings.Config.Validate(); err != nil {
		return projectionGateSettings{}, fmt.Errorf("invalid projection gate configuration: %w", err)
	}
	return settings, nil
}

func stringSetting(getenv func(string) string, key, fallback string) string {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback
	}
	return value
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

func intSetting(getenv func(string) string, key string, fallback, minimum int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		return 0, fmt.Errorf("%s must be an integer >= %d", key, minimum)
	}
	return value, nil
}

func int64Setting(getenv func(string) string, key string, fallback, minimum int64) (int64, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum {
		return 0, fmt.Errorf("%s must be an integer >= %d", key, minimum)
	}
	return value, nil
}

func parseBoolSetting(getenv func(string) string, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func productionEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

func defaultInstanceID() string {
	hostname, _ := os.Hostname()
	if strings.TrimSpace(hostname) == "" {
		hostname = "local"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
