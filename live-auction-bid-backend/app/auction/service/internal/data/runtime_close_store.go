package data

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/runtimegeneration"
)

const maxRuntimeCloseScanLimit int64 = 1000

// RuntimeCommandStore is the Redis-only lifecycle adapter used by standalone workers.
// It deliberately has no MySQL handle, so it cannot use a projected timestamp to settle a lot.
type RuntimeCommandStore struct {
	*Store
}

func NewRuntimeCommandStore(client *redis.Client, shardCount int) (*RuntimeCommandStore, error) {
	if client == nil {
		return nil, errors.New("runtime command Redis client is required")
	}
	if shardCount <= 0 {
		shardCount = RuntimeOutboxShardCount
	}
	if shardCount != RuntimeOutboxShardCount {
		return nil, fmt.Errorf("runtime command shard count must be %d", RuntimeOutboxShardCount)
	}
	return &RuntimeCommandStore{Store: &Store{redis: client, outboxShards: shardCount}}, nil
}

// ExecuteStartLot is intentionally unavailable on the Redis-only worker
// adapter because safe start must hold the MySQL lot row lock.
func (store *RuntimeCommandStore) ExecuteStartLot(context.Context, *v1.Lot, string) (auction.RuntimeStartResult, error) {
	return auction.RuntimeStartResult{}, errors.New("runtime start requires the MySQL-backed auction service store")
}

func (store *RuntimeCommandStore) BindRuntimeGenerationGuard(guard *runtimegeneration.Guard) error {
	if store == nil || store.Store == nil || guard == nil {
		return errors.New("runtime command store and generation guard are required")
	}
	store.runtimeGenerationGuard = guard
	return nil
}

// ScanDueRuntimeLotIDs returns candidates using Redis TIME. The close Lua script remains the only authority.
func (store *RuntimeCommandStore) ScanDueRuntimeLotIDs(ctx context.Context, limit int64) ([]string, error) {
	if store == nil || store.Store == nil || store.redis == nil {
		return nil, errors.New("runtime command store is not initialized")
	}
	if store.runtimeGenerationGuard != nil {
		if _, err := store.runtimeGenerationGuard.AllowWrite(); err != nil {
			return nil, fmt.Errorf("runtime close scan is frozen: %w", err)
		}
	}
	if limit <= 0 || limit > maxRuntimeCloseScanLimit {
		return nil, fmt.Errorf("runtime close scan limit must be between 1 and %d", maxRuntimeCloseScanLimit)
	}
	now, err := store.redis.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("read Redis time for runtime close scan: %w", err)
	}
	lotIDs, err := store.redis.ZRangeByScore(ctx, runtimeExpiringKey(), &redis.ZRangeBy{
		Min: "0", Max: strconv.FormatInt(now.UnixMilli(), 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("scan due runtime lots: %w", err)
	}
	return lotIDs, nil
}

func (store *RuntimeCommandStore) PingRuntime(ctx context.Context) error {
	if store == nil || store.Store == nil || store.redis == nil {
		return errors.New("runtime command store is not initialized")
	}
	if err := store.redis.Ping(ctx).Err(); err != nil {
		return err
	}
	if store.runtimeGenerationGuard != nil {
		return store.runtimeGenerationGuard.Ping(ctx)
	}
	return nil
}
