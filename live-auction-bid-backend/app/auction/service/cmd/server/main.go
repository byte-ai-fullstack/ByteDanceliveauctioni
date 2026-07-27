package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/aiassistant"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	shopbiz "live-auction-bid/backend/app/auction/service/internal/biz/shop"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/gateway"
	"live-auction-bid/backend/app/auction/service/internal/mysqlschema"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/realtime"
	"live-auction-bid/backend/app/auction/service/internal/redisclient"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/server"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"
	"live-auction-bid/backend/app/auction/service/internal/storage"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := os.Getenv("AUCTION_HTTP_ADDR")
	if addr == "" {
		addr = ":18080"
	}
	mysqlDSN := getenv("AUCTION_MYSQL_DSN", "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local")
	redisAddr := getenv("AUCTION_REDIS_ADDR", "127.0.0.1:16379")
	redisPassword := getenv("AUCTION_REDIS_PASSWORD", "auction_redis")
	instanceID := getenv("AUCTION_INSTANCE_ID", defaultInstanceID())
	redisConfig, err := redisclient.FromEnv(os.Getenv, redisAddr, "gateway-"+instanceID)
	if err != nil {
		log.Fatal(err)
	}
	if redisConfig.Password == "" {
		redisConfig.Password = redisPassword
	}
	redisPrimary, err := redisclient.NewPrimaryClient(context.Background(), redisConfig)
	if err != nil {
		log.Fatal(err)
	}
	schemaVerifier, err := mysqlschema.NewVerifier()
	if err != nil {
		log.Fatal(err)
	}

	store, err := data.NewStore(context.Background(), data.Config{
		MySQLDSN:           mysqlDSN,
		RedisAddr:          redisAddr,
		RedisPassword:      redisPassword,
		RedisClient:        redisPrimary,
		SchemaVerifier:     schemaVerifier,
		OutboxShards:       getenvInt("AUCTION_OUTBOX_SHARDS", data.RuntimeOutboxShardCount),
		OutboxPendingLimit: int64(getenvInt("AUCTION_OUTBOX_PENDING_LIMIT", 0)),
		DBMaxOpenConns:     getenvInt("AUCTION_DB_MAX_OPEN_CONNS", 20),
		DBMaxIdleConns:     getenvInt("AUCTION_DB_MAX_IDLE_CONNS", 10),
		DBConnMaxLifetime:  getenvDuration("AUCTION_DB_CONN_MAX_LIFETIME", 30*time.Minute),
		DBConnMaxIdleTime:  getenvDuration("AUCTION_DB_CONN_MAX_IDLE_TIME", 2*time.Minute),
		RedisPoolSize:      getenvInt("AUCTION_REDIS_POOL_SIZE", 0),
		RedisMinIdleConns:  getenvInt("AUCTION_REDIS_MIN_IDLE_CONNS", 0),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			log.Printf("close auction store: %v", closeErr)
		}
	}()
	if err := store.BootstrapReferenceData(context.Background()); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	searchRequirements := searchMonitoringRequirementsFromEnv(os.Getenv)
	observability.SetSearchMonitoringRequirements(searchRequirements.elasticsearch, searchRequirements.pgvector)
	buyerKeywords, buyerVectors, buyerEmbedder := newBuyerSearchFromEnv(ctx)
	if buyerVectors != nil {
		defer func() {
			if closeErr := buyerVectors.Close(); closeErr != nil {
				log.Printf("close buyer pgvector index: %v", closeErr)
			}
		}()
	}
	authManager, err := auth.NewManager(auth.Config{
		Secret:     os.Getenv("AUCTION_JWT_SECRET"),
		Issuer:     getenv("AUCTION_JWT_ISSUER", "auction-backend"),
		AccessTTL:  getenvDuration("AUCTION_ACCESS_TOKEN_TTL", auth.DefaultAccessTTL),
		RefreshTTL: getenvDuration("AUCTION_REFRESH_TOKEN_TTL", auth.DefaultRefreshTTL),
	})
	if err != nil {
		log.Fatal(err)
	}
	imageStorage, err := newImageStorageFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	assetCleanupWorker := data.NewAssetCleanupWorker(store, imageStorage, getenvDuration("AUCTION_TEMP_ASSET_CLEANUP_INTERVAL", time.Hour), 100)
	assetCleanupWorker.Start(ctx)

	userUsecase := userbiz.NewUsecase(store, authManager)
	if err := bootstrapMainAccount(ctx, userUsecase); err != nil {
		log.Fatal(err)
	}

	realtimeConfig, err := realtime.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	drainConfig, err := realtime.DrainConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	snapshotRefreshConfig, err := realtime.SnapshotRefreshConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	hub := realtime.NewHub(nil, realtimeConfig)
	hub.BindAuthManager(authManager)
	eventPublisher, closeRealtimeBus, err := newRealtimePublisherFromEnv(ctx, hub, instanceID)
	if err != nil {
		log.Fatal(err)
	}
	defer closeRealtimeBus()
	paymentProvider, err := auction.NewPaymentProviderFromName(os.Getenv("AUCTION_PAYMENT_PROVIDER"))
	if err != nil {
		log.Fatal(err)
	}
	auctionUsecase := auction.NewAuctionUsecase(store, store, store, eventPublisher).
		SetPaymentProvider(paymentProvider)
	hub.BindRoomAccessValidator(auctionUsecase)
	hub.BindSnapshotProvider(auctionUsecase)
	snapshotRefreshDone := make(chan error, 1)
	go func() {
		snapshotRefreshDone <- hub.RunSnapshotRefresh(ctx, snapshotRefreshConfig)
	}()
	defer func() {
		cancel()
		if err := <-snapshotRefreshDone; err != nil {
			log.Printf("realtime snapshot refresher stopped with error: %v", err)
		}
	}()
	aiAssistant := aiassistant.NewFromEnv(os.Getenv)
	observability.SetAIAssistantMode(aiAssistant.Mode())
	auctionService := appsvc.NewAuctionService(auctionUsecase, hub).
		SetAIAssistant(aiAssistant).
		SetBuyerSearch(buyerKeywords, buyerVectors, buyerEmbedder).
		SetBuyerSearchTimeout(getenvDuration("AUCTION_SEARCH_QUERY_TIMEOUT", 5*time.Second)).
		SetVerboseBidLog(getenvBool("AUCTION_VERBOSE_BID_LOG", false))
	auctionHTTPService, commandHealth, closeCommandClient, err := newAuctionHTTPServiceFromEnv(auctionService)
	if err != nil {
		log.Fatal(err)
	}
	defer closeCommandClient()
	userService := appsvc.NewUserService(userUsecase)
	shopUsecase := shopbiz.NewUsecase(store)
	shopService := appsvc.NewShopService(shopUsecase)
	orderService := appsvc.NewOrderService(shopUsecase, auctionUsecase)
	readiness := server.Readiness{
		Store:          store,
		CommandService: commandHealth,
		Gateway:        hub,
	}
	httpServer := server.NewHTTPServer(addr, auctionHTTPService, auctionService, userService, shopService, orderService, hub, readiness, authManager, authManager.Middleware(), imageStorage, store)

	log.Printf("auction gateway listening on HTTP/WebSocket %s", addr)
	if err := runGatewayHTTPServer(
		ctx,
		signalCtx.Done(),
		httpServer,
		hub,
		drainConfig,
		getenvDuration("AUCTION_WS_DRAIN_TIMEOUT", 75*time.Second),
		getenvDuration("AUCTION_HTTP_STOP_TIMEOUT", 10*time.Second),
	); err != nil {
		log.Printf("auction backend stopped with error: %v", err)
	}
}

func newAuctionHTTPServiceFromEnv(local v1.AuctionServiceHTTPServer) (v1.AuctionServiceHTTPServer, server.HealthChecker, func(), error) {
	target := strings.TrimSpace(os.Getenv("AUCTION_COMMAND_GRPC_TARGET"))
	if target == "" {
		return nil, nil, nil, errors.New("AUCTION_COMMAND_GRPC_TARGET is required; gateway cannot execute auction commands locally")
	}
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create auction command client: %w", err)
	}
	health, err := gateway.NewCommandHealthChecker(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	proxy, err := gateway.NewAuctionProxy(
		local,
		v1.NewAuctionCommandServiceClient(conn),
		getenvDuration("AUCTION_COMMAND_GRPC_TIMEOUT", 3*time.Second),
	)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	log.Printf("auction runtime commands routed through private gRPC")
	return proxy, health, func() { _ = conn.Close() }, nil
}

type lifecycleServer interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func runGatewayHTTPServer(
	ctx context.Context,
	shutdown <-chan struct{},
	httpServer lifecycleServer,
	hub *realtime.Hub,
	drainConfig realtime.DrainConfig,
	drainTimeout time.Duration,
	stopTimeout time.Duration,
) error {
	if httpServer == nil {
		return errors.New("auction HTTP server is required")
	}
	if hub == nil {
		return errors.New("realtime hub is required")
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpServer.Start(ctx) }()

	select {
	case err := <-serverErr:
		if err == nil && ctx.Err() == nil {
			err = errors.New("auction gateway HTTP server stopped unexpectedly")
		}
		return err
	case <-shutdown:
	}

	drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
	drainErr := hub.Drain(drainCtx, drainConfig)
	cancelDrain()

	stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	stopErr := httpServer.Stop(stopCtx)
	cancelStop()

	waitTimeout := stopTimeout
	if waitTimeout <= 0 {
		waitTimeout = time.Second
	}
	waitCtx, cancelWait := context.WithTimeout(context.WithoutCancel(ctx), waitTimeout)
	defer cancelWait()
	var startErr error
	select {
	case startErr = <-serverErr:
	case <-waitCtx.Done():
		startErr = errors.New("auction gateway HTTP server did not stop after shutdown")
	}
	return errors.Join(drainErr, stopErr, startErr)
}

func newRealtimePublisherFromEnv(ctx context.Context, hub *realtime.Hub, instanceID string) (*realtime.Publisher, func(), error) {
	if hub == nil {
		return nil, nil, errors.New("realtime hub is required")
	}
	natsURLs := strings.TrimSpace(os.Getenv("AUCTION_NATS_URLS"))
	if natsURLs == "" {
		if productionEnvironment(os.Getenv("AUCTION_ENV")) {
			return nil, nil, errors.New("AUCTION_NATS_URLS is required in production")
		}
		log.Printf("NATS realtime fanout disabled for single-process development")
		return realtime.NewPublisher(hub), func() {}, nil
	}
	bus, err := realtime.NewNATSBus(realtime.NATSBusConfig{
		URL:             natsURLs,
		Name:            getenv("AUCTION_NATS_CLIENT_NAME", "live-auction-"+instanceID),
		Origin:          instanceID,
		ReconnectWait:   getenvDuration("AUCTION_NATS_RECONNECT_WAIT", 500*time.Millisecond),
		ReconnectJitter: getenvDuration("AUCTION_NATS_RECONNECT_JITTER", 500*time.Millisecond),
		FlushTimeout:    getenvDuration("AUCTION_NATS_FLUSH_TIMEOUT", 250*time.Millisecond),
		DispatchTimeout: getenvDuration("AUCTION_NATS_DISPATCH_TIMEOUT", 2*time.Second),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create NATS realtime bus: %w", err)
	}
	if err := bus.Start(ctx, hub); err != nil {
		_ = bus.Close()
		return nil, nil, fmt.Errorf("start NATS realtime bus: %w", err)
	}
	hub.BindRoomSubscriptionManager(bus)
	log.Printf("NATS realtime fanout enabled: subject_mode=room_presence origin=%s", instanceID)
	return realtime.NewPublisher(hub, bus), func() {
		_ = bus.Close()
	}, nil
}

func productionEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

type searchMonitoringRequirements struct {
	elasticsearch bool
	pgvector      bool
}

func searchMonitoringRequirementsFromEnv(getenv func(string) string) searchMonitoringRequirements {
	if getenv == nil {
		return searchMonitoringRequirements{}
	}
	provider := strings.ToLower(strings.TrimSpace(getenv("AUCTION_SEARCH_PROVIDER")))
	return searchMonitoringRequirements{
		elasticsearch: strings.TrimSpace(getenv("AUCTION_SEARCH_ES_URL")) != "",
		pgvector:      strings.TrimSpace(getenv("AUCTION_SEARCH_PG_DSN")) != "" && (provider == "" || provider == "pgvector"),
	}
}

func newBuyerSearchFromEnv(ctx context.Context) (*searchindex.ElasticsearchIndex, *searchindex.PGVectorIndex, *searchindex.EmbeddingClient) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("AUCTION_SEARCH_PROVIDER")))
	dsn := strings.TrimSpace(os.Getenv("AUCTION_SEARCH_PG_DSN"))
	esURL := strings.TrimSpace(os.Getenv("AUCTION_SEARCH_ES_URL"))
	var keywords *searchindex.ElasticsearchIndex
	if esURL != "" {
		config := searchindex.ElasticsearchConfig{
			BaseURL: esURL, Username: strings.TrimSpace(os.Getenv("AUCTION_SEARCH_ES_USERNAME")), Password: os.Getenv("AUCTION_SEARCH_ES_PASSWORD"),
			WriteAlias:       getenv("AUCTION_SEARCH_ES_ALIAS", "auction-lots-current"),
			RequestTimeout:   getenvDuration("AUCTION_SEARCH_ES_TIMEOUT", 3*time.Second),
			MaxResponseBytes: int64(getenvInt("AUCTION_SEARCH_ES_MAX_RESPONSE_BYTES", 1<<20)),
		}
		index, err := newElasticsearchIndexWithStartupWait(ctx, config)
		if err != nil {
			log.Printf("buyer Elasticsearch retrieval disabled: %v", err)
		} else {
			keywords = index
			log.Printf("buyer Elasticsearch retrieval enabled: alias=%s", config.WriteAlias)
		}
	}
	if provider == "" && dsn == "" {
		if keywords == nil {
			log.Printf("buyer indexed search disabled: Elasticsearch URL and pgvector DSN are not configured")
		}
		return keywords, nil, nil
	}
	if provider != "" && provider != "pgvector" {
		log.Printf("buyer vector retrieval disabled: unsupported provider %s", provider)
		return keywords, nil, nil
	}
	if dsn == "" {
		log.Printf("buyer vector retrieval disabled: pgvector DSN is not configured")
		return keywords, nil, nil
	}
	embedder := searchindex.NewEmbeddingClientFromEnv(os.Getenv)
	if !embedder.Configured() {
		log.Printf("buyer vector retrieval disabled: embedding client is not configured")
		return keywords, nil, nil
	}
	indexCfg := searchindex.PGVectorConfig{
		DSN:                   dsn,
		EmbeddingProvider:     embedder.Provider(),
		EmbeddingModel:        embedder.Model(),
		EmbeddingModelVersion: embedder.ModelVersion(),
		EmbeddingDimensions:   embedder.Dimensions(),
		MaxOpenConns:          getenvInt("AUCTION_SEARCH_PG_MAX_OPEN_CONNS", 5),
		MaxIdleConns:          getenvInt("AUCTION_SEARCH_PG_MAX_IDLE_CONNS", 2),
		ConnMaxLifetime:       getenvDuration("AUCTION_SEARCH_PG_CONN_MAX_LIFETIME", 30*time.Minute),
		ConnMaxIdleTime:       getenvDuration("AUCTION_SEARCH_PG_CONN_MAX_IDLE_TIME", 2*time.Minute),
	}
	index, err := newPGVectorIndexWithStartupWait(ctx, indexCfg)
	if err != nil {
		log.Printf("buyer vector retrieval disabled: %v", err)
		return keywords, nil, nil
	}
	log.Printf("buyer pgvector retrieval enabled: model=%s version=%s dimensions=%d", embedder.Model(), embedder.ModelVersion(), embedder.Dimensions())
	return keywords, index, embedder
}

func newElasticsearchIndexWithStartupWait(ctx context.Context, cfg searchindex.ElasticsearchConfig) (*searchindex.ElasticsearchIndex, error) {
	wait := getenvDuration("AUCTION_SEARCH_STARTUP_WAIT", 30*time.Second)
	retryEvery := 2 * time.Second
	deadline := time.Now().Add(wait)
	var lastErr error
	for attempt := 1; ; attempt++ {
		index, err := searchindex.NewElasticsearchIndex(ctx, cfg)
		if err == nil {
			return index, nil
		}
		lastErr = err
		if wait <= 0 || time.Now().After(deadline) {
			return nil, lastErr
		}
		log.Printf("buyer search waiting for Elasticsearch: attempt=%d error=%v", attempt, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryEvery):
		}
	}
}

func newPGVectorIndexWithStartupWait(ctx context.Context, cfg searchindex.PGVectorConfig) (*searchindex.PGVectorIndex, error) {
	wait := getenvDuration("AUCTION_SEARCH_STARTUP_WAIT", 30*time.Second)
	retryEvery := 2 * time.Second
	deadline := time.Now().Add(wait)
	var lastErr error
	for attempt := 1; ; attempt++ {
		index, err := searchindex.NewPGVectorIndex(ctx, cfg)
		if err == nil {
			return index, nil
		}
		lastErr = err
		if wait <= 0 || time.Now().After(deadline) {
			return nil, lastErr
		}
		log.Printf("buyer search index waiting for pgvector: attempt=%d error=%v", attempt, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryEvery):
		}
	}
}

func defaultInstanceID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "local"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		log.Fatalf("invalid %s duration: %v", key, err)
	}
	return duration
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Fatalf("invalid %s integer: %v", key, err)
	}
	return parsed
}

func bootstrapMainAccount(ctx context.Context, users *userbiz.Usecase) error {
	username := os.Getenv("AUCTION_BOOTSTRAP_MAIN_ACCOUNT_USERNAME")
	password := os.Getenv("AUCTION_BOOTSTRAP_MAIN_ACCOUNT_PASSWORD")
	nickname := os.Getenv("AUCTION_BOOTSTRAP_MAIN_ACCOUNT_NICKNAME")
	if username == "" && password == "" && nickname == "" {
		return nil
	}
	if username == "" || password == "" || nickname == "" {
		return errors.New("bootstrap main account username, password and nickname must be configured together")
	}
	return users.BootstrapMainAccount(ctx, username, password, nickname)
}

func newImageStorageFromEnv() (storage.StorageProvider, error) {
	provider := strings.TrimSpace(os.Getenv("AUCTION_STORAGE_PROVIDER"))
	if provider == "" {
		provider = "tos"
	}
	switch provider {
	case "local":
		return storage.NewLocalStorage(storage.LocalConfig{
			RootDir:       strings.TrimSpace(os.Getenv("AUCTION_LOCAL_STORAGE_DIR")),
			Bucket:        strings.TrimSpace(os.Getenv("AUCTION_LOCAL_STORAGE_BUCKET")),
			PublicBaseURL: strings.TrimSpace(os.Getenv("AUCTION_LOCAL_STORAGE_PUBLIC_BASE_URL")),
		})
	case "tos":
	default:
		return nil, errors.New("unsupported auction storage provider: " + provider)
	}
	return storage.NewTOSStorage(storage.TOSConfig{
		Endpoint:      strings.TrimSpace(os.Getenv("AUCTION_TOS_ENDPOINT")),
		Region:        strings.TrimSpace(os.Getenv("AUCTION_TOS_REGION")),
		Bucket:        strings.TrimSpace(os.Getenv("AUCTION_TOS_BUCKET")),
		AccessKey:     strings.TrimSpace(os.Getenv("AUCTION_TOS_ACCESS_KEY")),
		SecretKey:     strings.TrimSpace(os.Getenv("AUCTION_TOS_SECRET_KEY")),
		PublicBaseURL: strings.TrimSpace(os.Getenv("AUCTION_TOS_PUBLIC_BASE_URL")),
		UseSSL:        getenvBool("AUCTION_TOS_USE_SSL", true),
	})
}

func getenvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "False", "no", "NO", "off", "OFF":
		return false
	default:
		log.Fatalf("invalid %s bool: %s", key, value)
		return fallback
	}
}
