package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/runtimegeneration"
)

type Config struct {
	MySQLDSN                      string
	RedisAddr                     string
	RedisPassword                 string
	OutboxShards                  int
	OutboxPendingLimit            int64
	DBMaxOpenConns                int
	DBMaxIdleConns                int
	DBConnMaxLifetime             time.Duration
	DBConnMaxIdleTime             time.Duration
	RedisPoolSize                 int
	RedisMinIdleConns             int
	RedisClient                   *redis.Client
	SchemaVerifier                SchemaVerifier
	RuntimeGenerationGuardEnabled bool
	RuntimeGenerationPollInterval time.Duration
	RuntimeReconcileInterval      time.Duration
}

// SchemaVerifier is owned by the Store consumer so application processes can
// validate schema state without coupling the data package to a migration source.
type SchemaVerifier interface {
	VerifyCurrent(context.Context, *sql.DB) error
}

// RuntimeAdmissionGate is defined by its command-path consumer. A closed gate
// rejects only work that can create or enlarge unprojected runtime state.
type RuntimeAdmissionGate interface {
	Check(context.Context) error
}

// Store is the single production data path for the auction service.
//
// Persistence rules:
// - Redis Lua owns live auction adjudication and runtime state.
// - Kafka Projector is the only writer of durable runtime lot, bid, and order facts.
// - GORM handles pre-start metadata, presentation state, and read models only.
// - There is intentionally no in-memory or database/sql fallback.
type Store struct {
	db                     *gorm.DB
	redis                  *redis.Client
	outboxShards           int
	outboxPendingLimit     int64
	runtimeGenerationGuard *runtimegeneration.Guard
	runtimeAdmissionGate   RuntimeAdmissionGate
}

func NewStore(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.MySQLDSN == "" {
		return nil, errors.New("mysql dsn is required")
	}
	if cfg.RedisClient == nil && cfg.RedisAddr == "" {
		return nil, errors.New("redis addr is required")
	}
	if cfg.SchemaVerifier == nil {
		return nil, errors.New("schema verifier is required")
	}
	var err error
	cfg.OutboxShards, err = normalizeRuntimeOutboxShardCount(cfg.OutboxShards)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if cfg.DBMaxOpenConns <= 0 {
		cfg.DBMaxOpenConns = 20
	}
	if cfg.DBMaxIdleConns <= 0 {
		cfg.DBMaxIdleConns = cfg.DBMaxOpenConns / 2
	}
	if cfg.DBConnMaxLifetime <= 0 {
		cfg.DBConnMaxLifetime = 30 * time.Minute
	}
	if cfg.DBConnMaxIdleTime <= 0 {
		cfg.DBConnMaxIdleTime = 2 * time.Minute
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := cfg.SchemaVerifier.VerifyCurrent(ctx, sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("verify mysql schema: %w", err)
	}

	rdb := cfg.RedisClient
	if rdb == nil {
		redisOptions := &redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}
		if cfg.RedisPoolSize > 0 {
			redisOptions.PoolSize = cfg.RedisPoolSize
		}
		if cfg.RedisMinIdleConns > 0 {
			redisOptions.MinIdleConns = cfg.RedisMinIdleConns
		}
		rdb = redis.NewClient(redisOptions)
	}
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = sqlDB.Close()
		_ = rdb.Close()
		return nil, err
	}

	observability.BindDBStatsProvider(sqlDB.Stats)
	observability.BindRedisPoolStatsProvider(func() observability.RedisPoolStats {
		stats := rdb.PoolStats()
		return observability.RedisPoolStats{
			Hits:       stats.Hits,
			Misses:     stats.Misses,
			Timeouts:   stats.Timeouts,
			TotalConns: stats.TotalConns,
			IdleConns:  stats.IdleConns,
			StaleConns: stats.StaleConns,
		}
	})
	store := &Store{
		db:                 db,
		redis:              rdb,
		outboxShards:       cfg.OutboxShards,
		outboxPendingLimit: cfg.OutboxPendingLimit,
	}
	if cfg.RuntimeGenerationGuardEnabled {
		backend, err := runtimegeneration.NewRedisBackend(rdb)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		reconciler, err := NewRuntimeReconciler(sqlDB, rdb, cfg.OutboxShards)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		guard, err := runtimegeneration.NewGuard(backend, runtimegeneration.Config{
			PollInterval:   cfg.RuntimeGenerationPollInterval,
			VerifyInterval: cfg.RuntimeReconcileInterval,
			Verify:         reconciler.VerifyActive,
		})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		store.runtimeGenerationGuard = guard
		observability.BindRuntimeGenerationReadyProvider(func() bool { return guard.Status().Ready })
	}
	return store, nil
}

// StartRuntimeGenerationGuard performs the initial reconciliation and keeps
// retrying in the background even if the initial check leaves the service frozen.
func (s *Store) StartRuntimeGenerationGuard(ctx context.Context) error {
	if s == nil || s.runtimeGenerationGuard == nil {
		return nil
	}
	initialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := s.runtimeGenerationGuard.Initialize(initialCtx)
	cancel()
	go s.runtimeGenerationGuard.Run(ctx)
	return err
}

func (s *Store) SignalRedisPrimarySwitch() {
	if s != nil && s.runtimeGenerationGuard != nil {
		s.runtimeGenerationGuard.SignalSwitchMaster()
	}
}

func (s *Store) RuntimeGenerationStatus() runtimegeneration.Status {
	if s == nil || s.runtimeGenerationGuard == nil {
		return runtimegeneration.Status{Ready: true, Reason: "generation guard disabled"}
	}
	return s.runtimeGenerationGuard.Status()
}

// ProjectionGateDB returns the Store-owned SQL pool for the read-only
// end-to-end projection gate. The caller must not close the returned pool.
func (s *Store) ProjectionGateDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is not initialized")
	}
	return s.db.DB()
}

func (s *Store) SetRuntimeAdmissionGate(gate RuntimeAdmissionGate) error {
	if s == nil || gate == nil {
		return errors.New("store and runtime admission gate are required")
	}
	s.runtimeAdmissionGate = gate
	return nil
}

func (s *Store) checkRuntimeAdmission(ctx context.Context) error {
	if s == nil || s.runtimeAdmissionGate == nil {
		return nil
	}
	if err := s.runtimeAdmissionGate.Check(ctx); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return contextError
		}
		return fmt.Errorf("%w: retry after the MySQL projection catches up", apperr.ErrOverloaded)
	}
	return nil
}

func normalizeRuntimeOutboxShardCount(value int) (int, error) {
	if value <= 0 {
		return RuntimeOutboxShardCount, nil
	}
	if value != RuntimeOutboxShardCount {
		return 0, fmt.Errorf("runtime outbox shard count must be %d", RuntimeOutboxShardCount)
	}
	return value, nil
}

func (s *Store) Ping(ctx context.Context) error {
	if s.db == nil || s.redis == nil {
		return errors.New("store is not initialized")
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return err
	}
	if err := s.redis.Ping(ctx).Err(); err != nil {
		return err
	}
	if s.runtimeGenerationGuard != nil {
		return s.runtimeGenerationGuard.Ping(ctx)
	}
	return nil
}

func (s *Store) Close() error {
	if s != nil && s.runtimeGenerationGuard != nil {
		observability.BindRuntimeGenerationReadyProvider(nil)
	}
	if s.redis != nil {
		_ = s.redis.Close()
	}
	if s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// BootstrapReferenceData applies idempotent reference-data defaults after the
// versioned schema has been verified. It performs DML only and is owned by the
// gateway process, not by every process that opens a Store.
func (s *Store) BootstrapReferenceData(ctx context.Context) error {
	if err := s.EnsureRBACDefaults(ctx); err != nil {
		return err
	}
	if err := s.ensureVisualDefaults(ctx); err != nil {
		return err
	}
	return s.EnsureShopSeeds(ctx)
}

func (s *Store) ensureVisualDefaults(ctx context.Context) error {
	var users []AuctionUserModel
	if err := s.db.WithContext(ctx).
		Select("id").
		Where("avatar_url = ?", "").
		Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		if err := s.db.WithContext(ctx).Model(&AuctionUserModel{}).
			Where("id = ? AND avatar_url = ?", user.ID, "").
			Update("avatar_url", userbiz.AvatarURLForUserID(user.ID)).Error; err != nil {
			return err
		}
	}

	var rooms []AuctionRoomModel
	if err := s.db.WithContext(ctx).
		Select("id", "created_at_unix_ms").
		Where("live_source_url = ? OR live_started_at_unix_ms = 0", "").
		Find(&rooms).Error; err != nil {
		return err
	}
	nowMs := time.Now().UnixMilli()
	for _, room := range rooms {
		startedAt := room.CreatedAtUnixMs
		if startedAt <= 0 {
			startedAt = nowMs
		}
		if err := s.db.WithContext(ctx).Model(&AuctionRoomModel{}).
			Where("id = ?", room.ID).
			Updates(map[string]any{
				"live_source_url":         gorm.Expr("CASE WHEN live_source_url = '' THEN ? ELSE live_source_url END", auction.LiveSourceURLForRoomID(room.ID)),
				"live_started_at_unix_ms": gorm.Expr("CASE WHEN live_started_at_unix_ms = 0 THEN ? ELSE live_started_at_unix_ms END", startedAt),
			}).Error; err != nil {
			return err
		}
	}
	return nil
}
